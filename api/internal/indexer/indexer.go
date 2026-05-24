package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"penguinyieldvault/api/internal/store"
)

// Event topic hashes (keccak256 of event signatures)
var (
	// ERC-4626 standard events
	topicDeposit  = crypto.Keccak256Hash([]byte("Deposit(address,address,uint256,uint256)"))
	topicWithdraw = crypto.Keccak256Hash([]byte("Withdraw(address,address,address,uint256,uint256)"))

	// Vault events
	topicStrategySettled        = crypto.Keccak256Hash([]byte("StrategySettled(address,uint256,uint256,uint256,uint256,uint256,uint256)"))
	topicStrategySettledDetails = crypto.Keccak256Hash([]byte("StrategySettledDetails(address,uint256,uint256,uint256,uint256,uint256)"))
	topicCapitalAllocated       = crypto.Keccak256Hash([]byte("CapitalAllocated(address,uint256,uint256,uint256)"))
	topicPerformanceFeeMinted   = crypto.Keccak256Hash([]byte("PerformanceFeeMinted(address,address,uint256,uint256)"))

	// StrategyRouter events
	topicStrategyExecuted = crypto.Keccak256Hash([]byte("StrategyExecuted(address,int256,uint256,uint256,uint256,uint256,bytes32)"))
	topicStrategyManaged  = crypto.Keccak256Hash([]byte("StrategyManaged(address,int256,uint256,uint256,uint256,uint256,bytes32)"))
)

// Config holds the indexer configuration.
type Config struct {
	RPCURL          string
	VaultAddress    common.Address
	RouterAddress   common.Address
	PollIntervalSec int
	StartBlock      int64
}

// Indexer monitors blockchain events and stores them in the database.
type Indexer struct {
	cfg   Config
	store *store.Store
}

// New creates a new Indexer.
func New(cfg Config, s *store.Store) *Indexer {
	return &Indexer{cfg: cfg, store: s}
}

// Run starts the indexer loop. It blocks until the context is cancelled.
func (idx *Indexer) Run(ctx context.Context) error {
	client, err := ethclient.DialContext(ctx, idx.cfg.RPCURL)
	if err != nil {
		return fmt.Errorf("dial rpc: %w", err)
	}
	defer client.Close()

	lastBlock, err := idx.store.GetLastIndexedBlock()
	if err != nil {
		return fmt.Errorf("get last indexed block: %w", err)
	}
	if lastBlock == 0 && idx.cfg.StartBlock > 0 {
		lastBlock = idx.cfg.StartBlock
	}

	log.Printf("indexer: starting from block %d, polling every %ds", lastBlock, idx.cfg.PollIntervalSec)

	ticker := time.NewTicker(time.Duration(idx.cfg.PollIntervalSec) * time.Second)
	defer ticker.Stop()

	// Do an immediate first poll
	lastBlock, err = idx.poll(ctx, client, lastBlock)
	if err != nil {
		log.Printf("indexer: initial poll error: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			newLast, err := idx.poll(ctx, client, lastBlock)
			if err != nil {
				log.Printf("indexer: poll error: %v", err)
				continue
			}
			lastBlock = newLast
		}
	}
}

func (idx *Indexer) poll(ctx context.Context, client *ethclient.Client, fromBlock int64) (int64, error) {
	head, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return fromBlock, fmt.Errorf("get head: %w", err)
	}
	headNum := head.Number.Int64()

	if fromBlock >= headNum {
		return fromBlock, nil
	}

	// Process in chunks of 2000 blocks
	const chunkSize int64 = 2000
	current := fromBlock + 1

	for current <= headNum {
		to := current + chunkSize - 1
		if to > headNum {
			to = headNum
		}

		if err := idx.processRange(ctx, client, current, to); err != nil {
			return current - 1, fmt.Errorf("process range %d-%d: %w", current, to, err)
		}

		if err := idx.store.SetLastIndexedBlock(to); err != nil {
			return current - 1, fmt.Errorf("set last block: %w", err)
		}

		log.Printf("indexer: indexed blocks %d to %d (head: %d)", current, to, headNum)
		current = to + 1
	}

	// Take a TVL snapshot at the latest block
	if err := idx.takeTVLSnapshot(ctx, client, headNum); err != nil {
		log.Printf("indexer: tvl snapshot error: %v", err)
	}

	return headNum, nil
}

func (idx *Indexer) processRange(ctx context.Context, client *ethclient.Client, from, to int64) error {
	addresses := []common.Address{idx.cfg.VaultAddress, idx.cfg.RouterAddress}

	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(from),
		ToBlock:   big.NewInt(to),
		Addresses: addresses,
	}

	logs, err := client.FilterLogs(ctx, query)
	if err != nil {
		return fmt.Errorf("filter logs: %w", err)
	}

	for _, vLog := range logs {
		if err := idx.processLog(ctx, client, vLog); err != nil {
			log.Printf("indexer: process log error (tx %s): %v", vLog.TxHash.Hex(), err)
		}
	}

	return nil
}

func (idx *Indexer) processLog(ctx context.Context, client *ethclient.Client, vLog types.Log) error {
	if len(vLog.Topics) == 0 {
		return nil
	}

	blockTime, err := idx.getBlockTimestamp(ctx, client, vLog.BlockNumber)
	if err != nil {
		return fmt.Errorf("get block time: %w", err)
	}

	topic := vLog.Topics[0]

	switch topic {
	case topicDeposit:
		return idx.handleDeposit(vLog, blockTime)
	case topicWithdraw:
		return idx.handleWithdraw(vLog, blockTime)
	case topicStrategySettled:
		return idx.handleStrategySettled(vLog, blockTime)
	case topicStrategySettledDetails:
		return idx.handleStrategySettledDetails(vLog, blockTime)
	case topicCapitalAllocated:
		return idx.handleCapitalAllocated(vLog, blockTime)
	case topicPerformanceFeeMinted:
		return idx.handlePerformanceFeeMinted(vLog, blockTime)
	case topicStrategyExecuted:
		return idx.handleStrategyExecuted(vLog, blockTime)
	case topicStrategyManaged:
		return idx.handleStrategyManaged(vLog, blockTime)
	}

	return nil
}

func (idx *Indexer) handleDeposit(vLog types.Log, timestamp int64) error {
	if len(vLog.Topics) < 3 {
		return nil
	}
	// Deposit(address indexed sender, address indexed owner, uint256 assets, uint256 shares)
	owner := common.BytesToAddress(vLog.Topics[2].Bytes()).Hex()

	abiDef, _ := abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"sender","type":"address"},{"indexed":true,"name":"owner","type":"address"},{"indexed":false,"name":"assets","type":"uint256"},{"indexed":false,"name":"shares","type":"uint256"}],"name":"Deposit","type":"event"}]`))
	event, err := abiDef.Unpack("Deposit", vLog.Data)
	if err != nil {
		return fmt.Errorf("unpack deposit: %w", err)
	}

	assets := event[0].(*big.Int)
	shares := event[1].(*big.Int)

	return idx.store.InsertUserTransaction(store.UserTransaction{
		TxHash:      vLog.TxHash.Hex(),
		BlockNumber: int64(vLog.BlockNumber),
		Timestamp:   timestamp,
		Type:        "Deposit",
		User:        owner,
		Assets:      assets.String(),
		Shares:      shares.String(),
	})
}

func (idx *Indexer) handleWithdraw(vLog types.Log, timestamp int64) error {
	if len(vLog.Topics) < 3 {
		return nil
	}
	// Withdraw(address indexed sender, address indexed receiver, address indexed owner, uint256 assets, uint256 shares)
	owner := common.BytesToAddress(vLog.Topics[3].Bytes()).Hex()

	abiDef, _ := abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"sender","type":"address"},{"indexed":true,"name":"receiver","type":"address"},{"indexed":true,"name":"owner","type":"address"},{"indexed":false,"name":"assets","type":"uint256"},{"indexed":false,"name":"shares","type":"uint256"}],"name":"Withdraw","type":"event"}]`))
	event, err := abiDef.Unpack("Withdraw", vLog.Data)
	if err != nil {
		return fmt.Errorf("unpack withdraw: %w", err)
	}

	assets := event[0].(*big.Int)
	shares := event[1].(*big.Int)

	return idx.store.InsertUserTransaction(store.UserTransaction{
		TxHash:      vLog.TxHash.Hex(),
		BlockNumber: int64(vLog.BlockNumber),
		Timestamp:   timestamp,
		Type:        "Withdraw",
		User:        owner,
		Assets:      assets.String(),
		Shares:      shares.String(),
	})
}

func (idx *Indexer) handleStrategySettled(vLog types.Log, timestamp int64) error {
	if len(vLog.Topics) < 2 {
		return nil
	}
	strategy := common.BytesToAddress(vLog.Topics[1].Bytes()).Hex()

	abiDef, _ := abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"strategy","type":"address"},{"indexed":false,"name":"principal","type":"uint256"},{"indexed":false,"name":"returnedAssets","type":"uint256"},{"indexed":false,"name":"profit","type":"uint256"},{"indexed":false,"name":"loss","type":"uint256"},{"indexed":false,"name":"strategyDebtAfter","type":"uint256"},{"indexed":false,"name":"totalStrategyDebtAfter","type":"uint256"}],"name":"StrategySettled","type":"event"}]`))
	event, err := abiDef.Unpack("StrategySettled", vLog.Data)
	if err != nil {
		return fmt.Errorf("unpack strategy settled: %w", err)
	}

	principal := event[0].(*big.Int)
	returnedAssets := event[1].(*big.Int)
	profit := event[2].(*big.Int)
	loss := event[3].(*big.Int)

	return idx.store.InsertSettlement(
		vLog.TxHash.Hex(),
		int64(vLog.BlockNumber),
		timestamp,
		strategy,
		principal.String(),
		returnedAssets.String(),
		profit.String(),
		loss.String(),
		"0", "0",
	)
}

func (idx *Indexer) handleStrategySettledDetails(vLog types.Log, timestamp int64) error {
	// StrategySettledDetails has additional fee info; we log it as a bot operation
	if len(vLog.Topics) < 2 {
		return nil
	}
	strategy := common.BytesToAddress(vLog.Topics[1].Bytes()).Hex()

	abiDef, _ := abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"strategy","type":"address"},{"indexed":false,"name":"grossProfit","type":"uint256"},{"indexed":false,"name":"netProfit","type":"uint256"},{"indexed":false,"name":"feeAssets","type":"uint256"},{"indexed":false,"name":"lossOffset","type":"uint256"},{"indexed":false,"name":"pendingLossAfter","type":"uint256"}],"name":"StrategySettledDetails","type":"event"}]`))
	event, err := abiDef.Unpack("StrategySettledDetails", vLog.Data)
	if err != nil {
		return fmt.Errorf("unpack settled details: %w", err)
	}

	grossProfit := event[0].(*big.Int)
	netProfit := event[1].(*big.Int)
	feeAssets := event[2].(*big.Int)
	lossOffset := event[3].(*big.Int)
	pendingLossAfter := event[4].(*big.Int)

	details := map[string]string{
		"grossProfit":      grossProfit.String(),
		"netProfit":        netProfit.String(),
		"feeAssets":        feeAssets.String(),
		"lossOffset":       lossOffset.String(),
		"pendingLossAfter": pendingLossAfter.String(),
	}
	detailsJSON, _ := json.Marshal(details)

	// Update the corresponding strategy_settlements row with gross_profit and fee_assets
	// The StrategySettledDetails event is emitted in the same transaction as StrategySettled,
	// so we can match by tx_hash to enrich the settlement record.
	if err := idx.store.UpdateSettlementDetails(vLog.TxHash.Hex(), grossProfit.String(), feeAssets.String()); err != nil {
		log.Printf("indexer: failed to update settlement details for tx %s: %v", vLog.TxHash.Hex(), err)
	}

	return idx.store.InsertBotOperation(store.BotOperation{
		TxHash:      vLog.TxHash.Hex(),
		BlockNumber: int64(vLog.BlockNumber),
		Timestamp:   timestamp,
		Type:        "StrategySettledDetails",
		Source:      strategy,
		Profit:      netProfit.String(),
		Loss:        "0",
		Details:     string(detailsJSON),
	})
}

func (idx *Indexer) handleCapitalAllocated(vLog types.Log, timestamp int64) error {
	if len(vLog.Topics) < 2 {
		return nil
	}
	strategy := common.BytesToAddress(vLog.Topics[1].Bytes()).Hex()

	abiDef, _ := abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"strategy","type":"address"},{"indexed":false,"name":"amount","type":"uint256"},{"indexed":false,"name":"strategyDebtAfter","type":"uint256"},{"indexed":false,"name":"totalStrategyDebtAfter","type":"uint256"}],"name":"CapitalAllocated","type":"event"}]`))
	event, err := abiDef.Unpack("CapitalAllocated", vLog.Data)
	if err != nil {
		return fmt.Errorf("unpack capital allocated: %w", err)
	}

	amount := event[0].(*big.Int)

	details := map[string]string{
		"amount":         amount.String(),
		"debtAfter":      event[1].(*big.Int).String(),
		"totalDebtAfter": event[2].(*big.Int).String(),
	}
	detailsJSON, _ := json.Marshal(details)

	return idx.store.InsertBotOperation(store.BotOperation{
		TxHash:      vLog.TxHash.Hex(),
		BlockNumber: int64(vLog.BlockNumber),
		Timestamp:   timestamp,
		Type:        "CapitalAllocated",
		Source:      strategy,
		Profit:      "0",
		Loss:        "0",
		Details:     string(detailsJSON),
	})
}

func (idx *Indexer) handlePerformanceFeeMinted(vLog types.Log, timestamp int64) error {
	if len(vLog.Topics) < 3 {
		return nil
	}
	strategy := common.BytesToAddress(vLog.Topics[1].Bytes()).Hex()
	recipient := common.BytesToAddress(vLog.Topics[2].Bytes()).Hex()

	abiDef, _ := abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"strategy","type":"address"},{"indexed":true,"name":"recipient","type":"address"},{"indexed":false,"name":"feeAssets","type":"uint256"},{"indexed":false,"name":"mintedShares","type":"uint256"}],"name":"PerformanceFeeMinted","type":"event"}]`))
	event, err := abiDef.Unpack("PerformanceFeeMinted", vLog.Data)
	if err != nil {
		return fmt.Errorf("unpack performance fee: %w", err)
	}

	feeAssets := event[0].(*big.Int)
	mintedShares := event[1].(*big.Int)

	details := map[string]string{
		"feeAssets":    feeAssets.String(),
		"mintedShares": mintedShares.String(),
		"recipient":    recipient,
	}
	detailsJSON, _ := json.Marshal(details)

	return idx.store.InsertBotOperation(store.BotOperation{
		TxHash:      vLog.TxHash.Hex(),
		BlockNumber: int64(vLog.BlockNumber),
		Timestamp:   timestamp,
		Type:        "PerformanceFeeMinted",
		Source:      strategy,
		Profit:      "0",
		Loss:        "0",
		Details:     string(detailsJSON),
	})
}

func (idx *Indexer) handleStrategyExecuted(vLog types.Log, timestamp int64) error {
	if len(vLog.Topics) < 2 {
		return nil
	}
	source := common.BytesToAddress(vLog.Topics[1].Bytes()).Hex()

	abiDef, _ := abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"source","type":"address"},{"indexed":false,"name":"realizedPnl","type":"int256"},{"indexed":false,"name":"amountIn","type":"uint256"},{"indexed":false,"name":"returnedAssets","type":"uint256"},{"indexed":false,"name":"profit","type":"uint256"},{"indexed":false,"name":"loss","type":"uint256"},{"indexed":false,"name":"executionContextHash","type":"bytes32"}],"name":"StrategyExecuted","type":"event"}]`))
	event, err := abiDef.Unpack("StrategyExecuted", vLog.Data)
	if err != nil {
		return fmt.Errorf("unpack strategy executed: %w", err)
	}

	realizedPnl := event[0].(*big.Int)
	amountIn := event[1].(*big.Int)
	returnedAssets := event[2].(*big.Int)
	profit := event[3].(*big.Int)
	loss := event[4].(*big.Int)

	details := map[string]string{
		"realizedPnl":    realizedPnl.String(),
		"amountIn":       amountIn.String(),
		"returnedAssets": returnedAssets.String(),
	}
	detailsJSON, _ := json.Marshal(details)

	return idx.store.InsertBotOperation(store.BotOperation{
		TxHash:      vLog.TxHash.Hex(),
		BlockNumber: int64(vLog.BlockNumber),
		Timestamp:   timestamp,
		Type:        "StrategyExecuted",
		Source:      source,
		Profit:      profit.String(),
		Loss:        loss.String(),
		Details:     string(detailsJSON),
	})
}

func (idx *Indexer) handleStrategyManaged(vLog types.Log, timestamp int64) error {
	if len(vLog.Topics) < 2 {
		return nil
	}
	source := common.BytesToAddress(vLog.Topics[1].Bytes()).Hex()

	abiDef, _ := abi.JSON(strings.NewReader(`[{"anonymous":false,"inputs":[{"indexed":true,"name":"source","type":"address"},{"indexed":false,"name":"realizedPnl","type":"int256"},{"indexed":false,"name":"principalSettled","type":"uint256"},{"indexed":false,"name":"returnedAssets","type":"uint256"},{"indexed":false,"name":"profit","type":"uint256"},{"indexed":false,"name":"loss","type":"uint256"},{"indexed":false,"name":"executionContextHash","type":"bytes32"}],"name":"StrategyManaged","type":"event"}]`))
	event, err := abiDef.Unpack("StrategyManaged", vLog.Data)
	if err != nil {
		return fmt.Errorf("unpack strategy managed: %w", err)
	}

	realizedPnl := event[0].(*big.Int)
	principalSettled := event[1].(*big.Int)
	returnedAssets := event[2].(*big.Int)
	profit := event[3].(*big.Int)
	loss := event[4].(*big.Int)

	details := map[string]string{
		"realizedPnl":      realizedPnl.String(),
		"principalSettled": principalSettled.String(),
		"returnedAssets":   returnedAssets.String(),
	}
	detailsJSON, _ := json.Marshal(details)

	return idx.store.InsertBotOperation(store.BotOperation{
		TxHash:      vLog.TxHash.Hex(),
		BlockNumber: int64(vLog.BlockNumber),
		Timestamp:   timestamp,
		Type:        "StrategyManaged",
		Source:      source,
		Profit:      profit.String(),
		Loss:        loss.String(),
		Details:     string(detailsJSON),
	})
}

// takeTVLSnapshot reads current vault state and saves a TVL snapshot.
func (idx *Indexer) takeTVLSnapshot(ctx context.Context, client *ethclient.Client, blockNum int64) error {
	vaultABI, _ := abi.JSON(strings.NewReader(`[
		{"inputs":[],"name":"totalAssets","outputs":[{"type":"uint256"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"idleAssets","outputs":[{"type":"uint256"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"totalStrategyDebt","outputs":[{"type":"uint256"}],"stateMutability":"view","type":"function"}
	]`))

	totalAssets, err := idx.callView(ctx, client, idx.cfg.VaultAddress, vaultABI, "totalAssets")
	if err != nil {
		return fmt.Errorf("call totalAssets: %w", err)
	}
	idleAssets, err := idx.callView(ctx, client, idx.cfg.VaultAddress, vaultABI, "idleAssets")
	if err != nil {
		return fmt.Errorf("call idleAssets: %w", err)
	}
	strategyDebt, err := idx.callView(ctx, client, idx.cfg.VaultAddress, vaultABI, "totalStrategyDebt")
	if err != nil {
		return fmt.Errorf("call totalStrategyDebt: %w", err)
	}

	header, err := client.HeaderByNumber(ctx, big.NewInt(blockNum))
	if err != nil {
		return fmt.Errorf("get block header: %w", err)
	}

	return idx.store.InsertTVLSnapshot(store.TVLSnapshot{
		Timestamp:    int64(header.Time),
		TotalAssets:  totalAssets.String(),
		IdleAssets:   idleAssets.String(),
		StrategyDebt: strategyDebt.String(),
		BlockNumber:  blockNum,
	})
}

func (idx *Indexer) callView(ctx context.Context, client *ethclient.Client, addr common.Address, abiDef abi.ABI, method string) (*big.Int, error) {
	data, err := abiDef.Pack(method)
	if err != nil {
		return nil, err
	}
	result, err := client.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
	if err != nil {
		return nil, err
	}
	outputs, err := abiDef.Unpack(method, result)
	if err != nil {
		return nil, err
	}
	if len(outputs) == 0 {
		return big.NewInt(0), nil
	}
	val, ok := outputs[0].(*big.Int)
	if !ok {
		return big.NewInt(0), fmt.Errorf("unexpected type for %s", method)
	}
	return val, nil
}

var blockTimestampCache = make(map[uint64]int64)

func (idx *Indexer) getBlockTimestamp(ctx context.Context, client *ethclient.Client, blockNum uint64) (int64, error) {
	if ts, ok := blockTimestampCache[blockNum]; ok {
		return ts, nil
	}
	header, err := client.HeaderByNumber(ctx, big.NewInt(int64(blockNum)))
	if err != nil {
		return 0, err
	}
	ts := int64(header.Time)
	blockTimestampCache[blockNum] = ts
	// Keep cache bounded
	if len(blockTimestampCache) > 10000 {
		for k := range blockTimestampCache {
			delete(blockTimestampCache, k)
			break
		}
	}
	return ts, nil
}
