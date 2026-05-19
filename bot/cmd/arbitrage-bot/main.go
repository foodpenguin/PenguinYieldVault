package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	feeDenominator = 1_000_000
	bpsDenominator = 10_000
)

var (
	q96 = new(big.Int).Lsh(big.NewInt(1), 96)
)

// PoolSnapshot holds the live state of a single V3 pool.
type PoolSnapshot struct {
	Address      common.Address
	Token0       common.Address
	Token1       common.Address
	Fee          uint32
	Liquidity    *big.Int
	SqrtPriceX96 *big.Int
	Tick         int32
}

// PairConfig defines a trading pair and its pool list for arbitrage scanning.
type PairConfig struct {
	Name  string
	Token common.Address
	Pools []common.Address
}

// Opportunity is the best arbitrage candidate found in a scan.
type Opportunity struct {
	PairName     string
	Token        common.Address
	BuyPool      PoolSnapshot
	SellPool     PoolSnapshot
	AmountInWETH *big.Int
	TokenOut     *big.Int
	WETHBack     *big.Int
	GrossProfit  *big.Int
	GasCost      *big.Int
	NetProfit    *big.Int
}

// ExecuteWithSnapshotParams maps to the AtomicExecutor tuple parameter.
type ExecuteWithSnapshotParams struct {
	Source            common.Address `abi:"source"`
	StrategyParams    []byte         `abi:"strategyParams"`
	Pool              common.Address `abi:"pool"`
	TokenIn           common.Address `abi:"tokenIn"`
	TokenOut          common.Address `abi:"tokenOut"`
	SecondaryPool     common.Address `abi:"secondaryPool"`
	SecondaryTokenIn  common.Address `abi:"secondaryTokenIn"`
	SecondaryTokenOut common.Address `abi:"secondaryTokenOut"`
	AmountIn          *big.Int       `abi:"amountIn"`
	MinAmountOut      *big.Int       `abi:"minAmountOut"`
	MinReturnedAssets *big.Int       `abi:"minReturnedAssets"`
	MinProfit         *big.Int       `abi:"minProfit"`
	MaxLossBps        uint16         `abi:"maxLossBps"`
	Deadline          *big.Int       `abi:"deadline"`
}

// Config holds all bot runtime configuration.
type Config struct {
	RPCURL                         string
	PrivateKeyHex                  string
	EnableArbitrageBot             bool
	EnableRebalanceBot             bool
	VaultAddress                   common.Address
	AtomicExecutor                 common.Address
	Source                         common.Address
	StrategyRouter                 common.Address
	RebalanceSource                common.Address
	RebalancePool                  common.Address
	PositionManager                common.Address
	ChainID                        *big.Int
	AmountInWei                    *big.Int
	MinProfitWei                   *big.Int
	MinNetProfitWei                *big.Int
	MaxLossBps                     uint64
	ArbitrageMaxTradeBps           uint64
	DummyPayoutWei                 *big.Int
	SlippageBps                    uint64
	DeadlineSec                    uint64
	LoopIntervalSec                uint64
	MaxGasLimit                    uint64
	RebalanceGasLimit              uint64
	RebalanceATRLength             int
	RebalanceATRPeriodSec          uint64
	RebalanceATRMultiplier         float64
	RebalanceRangeBpsMin           uint64
	RebalanceRangeBpsMax           uint64
	RebalanceAdxLength             int
	RebalanceAdxPeriodSec          uint64
	RebalanceAdxTrendThreshold     float64
	RebalanceExpectedFeeDays       float64
	RebalanceDailyVolumeAsset      float64
	RebalanceTriggerZoneBps        uint64
	RebalanceCostMultiplierBps     uint64
	RebalancePaybackThresholdHours float64
	RebalanceCooldownSec           uint64
	RebalanceStateFile             string
	RebalanceMinSwapOut            *big.Int
	RebalanceAmount0Min            *big.Int
	RebalanceAmount1Min            *big.Int
	RebalancePrincipalToSettle     *big.Int
	RebalanceMaxLossBps            uint64
	RebalanceMinReturnedAssets     *big.Int
	RebalanceBurnPosition          bool
	DryRun                         bool
	StrategyMode                   string
	RawStrategyParams              []byte
	WETH                           common.Address
	METH                           common.Address
	UNI                            common.Address
	USDC                           common.Address
	PoolsMETH                      []common.Address
	PoolsUNI                       []common.Address
	PoolsUSDC                      []common.Address
}

var (
	poolABIJSON = `[
		{"inputs":[],"name":"token0","outputs":[{"internalType":"address","name":"","type":"address"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"token1","outputs":[{"internalType":"address","name":"","type":"address"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"fee","outputs":[{"internalType":"uint24","name":"","type":"uint24"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"tickSpacing","outputs":[{"internalType":"int24","name":"","type":"int24"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"liquidity","outputs":[{"internalType":"uint128","name":"","type":"uint128"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"slot0","outputs":[{"internalType":"uint160","name":"sqrtPriceX96","type":"uint160"},{"internalType":"int24","name":"tick","type":"int24"},{"internalType":"uint16","name":"observationIndex","type":"uint16"},{"internalType":"uint16","name":"observationCardinality","type":"uint16"},{"internalType":"uint16","name":"observationCardinalityNext","type":"uint16"},{"internalType":"uint8","name":"feeProtocol","type":"uint8"},{"internalType":"bool","name":"unlocked","type":"bool"}],"stateMutability":"view","type":"function"}
	]`

	atomicExecutorABIJSON = `[
		{"inputs":[{"components":[{"internalType":"address","name":"source","type":"address"},{"internalType":"bytes","name":"strategyParams","type":"bytes"},{"internalType":"address","name":"pool","type":"address"},{"internalType":"address","name":"tokenIn","type":"address"},{"internalType":"address","name":"tokenOut","type":"address"},{"internalType":"address","name":"secondaryPool","type":"address"},{"internalType":"address","name":"secondaryTokenIn","type":"address"},{"internalType":"address","name":"secondaryTokenOut","type":"address"},{"internalType":"uint256","name":"amountIn","type":"uint256"},{"internalType":"uint256","name":"minAmountOut","type":"uint256"},{"internalType":"uint256","name":"minReturnedAssets","type":"uint256"},{"internalType":"uint256","name":"minProfit","type":"uint256"},{"internalType":"uint16","name":"maxLossBps","type":"uint16"},{"internalType":"uint256","name":"deadline","type":"uint256"}],"internalType":"struct AtomicExecutor.ExecuteWithSnapshotParams","name":"params","type":"tuple"}],"name":"executeWithSnapshot","outputs":[{"internalType":"int256","name":"realizedPnl","type":"int256"},{"internalType":"uint256","name":"profit","type":"uint256"},{"components":[{"internalType":"address","name":"pool","type":"address"},{"internalType":"address","name":"tokenIn","type":"address"},{"internalType":"address","name":"tokenOut","type":"address"},{"internalType":"uint24","name":"fee","type":"uint24"},{"internalType":"uint128","name":"liquidity","type":"uint128"},{"internalType":"uint160","name":"sqrtPriceX96","type":"uint160"},{"internalType":"int24","name":"tick","type":"int24"},{"internalType":"uint256","name":"reserveInVirtual","type":"uint256"},{"internalType":"uint256","name":"reserveOutVirtual","type":"uint256"},{"internalType":"uint256","name":"quoteAmountOut","type":"uint256"}],"internalType":"struct OnchainStateLens.V3PoolSnapshot","name":"buySnapshot","type":"tuple"},{"components":[{"internalType":"address","name":"pool","type":"address"},{"internalType":"address","name":"tokenIn","type":"address"},{"internalType":"address","name":"tokenOut","type":"address"},{"internalType":"uint24","name":"fee","type":"uint24"},{"internalType":"uint128","name":"liquidity","type":"uint128"},{"internalType":"uint160","name":"sqrtPriceX96","type":"uint160"},{"internalType":"int24","name":"tick","type":"int24"},{"internalType":"uint256","name":"reserveInVirtual","type":"uint256"},{"internalType":"uint256","name":"reserveOutVirtual","type":"uint256"},{"internalType":"uint256","name":"quoteAmountOut","type":"uint256"}],"internalType":"struct OnchainStateLens.V3PoolSnapshot","name":"sellSnapshot","type":"tuple"}],"stateMutability":"nonpayable","type":"function"}
	]`

	vaultABIJSON = `[
		{"inputs":[],"name":"totalAssets","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
		{"inputs":[{"internalType":"address","name":"strategy","type":"address"}],"name":"availableForStrategy","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
		{"inputs":[{"internalType":"address","name":"strategy","type":"address"}],"name":"strategyDebt","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"}
	]`
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}

	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, cfg.RPCURL)
	if err != nil {
		log.Fatalf("RPC connect failed: %v", err)
	}
	defer client.Close()

	if cfg.ChainID == nil {
		cfg.ChainID, err = client.NetworkID(ctx)
		if err != nil {
			log.Fatalf("read chain id failed: %v", err)
		}
	}

	privateKey, err := crypto.HexToECDSA(strings.TrimPrefix(cfg.PrivateKeyHex, "0x"))
	if err != nil {
		log.Fatalf("invalid PRIVATE_KEY_HEX: %v", err)
	}
	signerAddr := crypto.PubkeyToAddress(privateKey.PublicKey)

	poolABI, err := abi.JSON(strings.NewReader(poolABIJSON))
	if err != nil {
		log.Fatalf("parse pool ABI failed: %v", err)
	}
	atomicABI, err := abi.JSON(strings.NewReader(atomicExecutorABIJSON))
	if err != nil {
		log.Fatalf("parse AtomicExecutor ABI failed: %v", err)
	}

	var vaultABI *abi.ABI
	if cfg.VaultAddress != (common.Address{}) {
		parsed, err := abi.JSON(strings.NewReader(vaultABIJSON))
		if err != nil {
			log.Fatalf("parse Vault ABI failed: %v", err)
		}
		vaultABI = &parsed
	}

	pairs := []PairConfig{
		{Name: "METH/ETH", Token: cfg.METH, Pools: cfg.PoolsMETH},
		{Name: "UNI/ETH", Token: cfg.UNI, Pools: cfg.PoolsUNI},
		{Name: "USDC/ETH", Token: cfg.USDC, Pools: cfg.PoolsUSDC},
	}

	log.Printf("starting arbitrage bot, signer=%s, chainID=%s, dryRun=%v", signerAddr.Hex(), cfg.ChainID.String(), cfg.DryRun)
	for _, pair := range pairs {
		log.Printf("watching pair: %s (%d pools)", pair.Name, len(pair.Pools))
	}

	var rebalanceRunner *RebalanceRunner
	if cfg.EnableRebalanceBot {
		rebalanceRunner, err = NewRebalanceRunner(cfg)
		if err != nil {
		log.Fatalf("init rebalance module failed: %v", err)
		}
		log.Printf(
			"rebalance module enabled: source=%s, router=%s, pool=%s, atrLen=%d, atrPeriod=%ds, atrK=%.2f",
			cfg.RebalanceSource.Hex(),
			cfg.StrategyRouter.Hex(),
			cfg.RebalancePool.Hex(),
			cfg.RebalanceATRLength,
			cfg.RebalanceATRPeriodSec,
			cfg.RebalanceATRMultiplier,
		)
	}

	ticker := time.NewTicker(time.Duration(cfg.LoopIntervalSec) * time.Second)
	defer ticker.Stop()

	for {
		if cfg.EnableArbitrageBot {
			err = runArbitrageCycle(ctx, client, poolABI, atomicABI, vaultABI, cfg, privateKey, signerAddr, pairs)
			if err != nil {
				log.Printf("arbitrage scan cycle failed: %v", err)
			}
		}

		if rebalanceRunner != nil {
			err = rebalanceRunner.RunCycle(ctx, client, poolABI, cfg, privateKey, signerAddr)
			if err != nil {
				log.Printf("rebalance cycle failed: %v", err)
			}
		}
		<-ticker.C
	}
}

func runArbitrageCycle(
	ctx context.Context,
	client *ethclient.Client,
	poolABI abi.ABI,
	atomicABI abi.ABI,
	vaultABI *abi.ABI,
	cfg Config,
	privateKey *ecdsa.PrivateKey,
	signerAddr common.Address,
	pairs []PairConfig,
) error {
	amountIn := new(big.Int).Set(cfg.AmountInWei)
	if vaultABI != nil && cfg.VaultAddress != (common.Address{}) {
		capped, err := resolveArbitrageAmount(ctx, client, *vaultABI, cfg)
		if err != nil {
			log.Printf("arb amount cap read failed: %v", err)
		} else if capped.Sign() > 0 {
			amountIn = capped
		}
	}
	if amountIn.Sign() <= 0 {
		log.Printf("arb skip: available amount is 0")
		return nil
	}

	for _, pair := range pairs {
		snapshots, err := fetchSnapshots(ctx, client, poolABI, pair.Pools)
		if err != nil {
			log.Printf("%s pool read failed: %v", pair.Name, err)
			continue
		}

		opp, ok := findBestOpportunity(cfg.WETH, pair.Token, pair.Name, amountIn, snapshots)
		if !ok {
			log.Printf("%s no viable dual-pool path", pair.Name)
			continue
		}

		gasCost, tipCap, feeCap, gasLimit, err := estimateGasCost(ctx, client, signerAddr, cfg.MaxGasLimit)
		if err != nil {
			log.Printf("%s gas estimate failed: %v", pair.Name, err)
			continue
		}

		opp.GasCost = gasCost
		opp.NetProfit = new(big.Int).Sub(opp.GrossProfit, gasCost)
		if opp.NetProfit.Sign() <= 0 || opp.NetProfit.Cmp(cfg.MinNetProfitWei) < 0 {
			log.Printf("%s skip: gross=%s, gas=%s, net=%s (< minNet %s)", pair.Name, opp.GrossProfit, gasCost, opp.NetProfit, cfg.MinNetProfitWei)
			continue
		}

		sig, err := signOpportunity(privateKey, *opp)
		if err != nil {
			log.Printf("%s opportunity signature failed: %v", pair.Name, err)
			continue
		}
		log.Printf("%s opportunity found: buy=%s sell=%s gross=%s net=%s sig=%s", pair.Name, opp.BuyPool.Address.Hex(), opp.SellPool.Address.Hex(), opp.GrossProfit, opp.NetProfit, sig)

		params, contextHash, err := buildExecuteParams(cfg, *opp)
		if err != nil {
			log.Printf("%s build strategy params failed: %v", pair.Name, err)
			continue
		}
		log.Printf("%s contextHash=%s", pair.Name, contextHash.Hex())

		if cfg.DryRun {
			log.Printf("%s dry-run: tx not sent", pair.Name)
			continue
		}

		txHash, err := sendExecuteTx(ctx, client, atomicABI, cfg, privateKey, signerAddr, params, gasLimit, tipCap, feeCap)
		if err != nil {
			log.Printf("%s send tx failed: %v", pair.Name, err)
			continue
		}
		log.Printf("%s arb tx sent: %s", pair.Name, txHash.Hex())
	}

	return nil
}

func fetchSnapshots(ctx context.Context, client *ethclient.Client, poolABI abi.ABI, pools []common.Address) ([]PoolSnapshot, error) {
	results := make([]PoolSnapshot, 0, len(pools))
	for _, pool := range pools {
		snap, err := fetchSnapshot(ctx, client, poolABI, pool)
		if err != nil {
			return nil, fmt.Errorf("pool %s: %w", pool.Hex(), err)
		}
		results = append(results, snap)
	}
	return results, nil
}

func fetchSnapshot(ctx context.Context, client *ethclient.Client, poolABI abi.ABI, pool common.Address) (PoolSnapshot, error) {
	token0Raw, err := callMethod(ctx, client, poolABI, pool, "token0")
	if err != nil {
		return PoolSnapshot{}, err
	}
	token1Raw, err := callMethod(ctx, client, poolABI, pool, "token1")
	if err != nil {
		return PoolSnapshot{}, err
	}
	feeRaw, err := callMethod(ctx, client, poolABI, pool, "fee")
	if err != nil {
		return PoolSnapshot{}, err
	}
	liqRaw, err := callMethod(ctx, client, poolABI, pool, "liquidity")
	if err != nil {
		return PoolSnapshot{}, err
	}
	slot0Raw, err := callMethod(ctx, client, poolABI, pool, "slot0")
	if err != nil {
		return PoolSnapshot{}, err
	}

	token0, ok := token0Raw[0].(common.Address)
	if !ok {
		return PoolSnapshot{}, errors.New("token0 type assertion failed")
	}
	token1, ok := token1Raw[0].(common.Address)
	if !ok {
		return PoolSnapshot{}, errors.New("token1 type assertion failed")
	}
	fee, err := asUint32(feeRaw[0])
	if err != nil {
		return PoolSnapshot{}, fmt.Errorf("fee type assertion failed: %w", err)
	}
	liquidity, ok := liqRaw[0].(*big.Int)
	if !ok {
		return PoolSnapshot{}, errors.New("liquidity type assertion failed")
	}
	sqrtPriceX96, ok := slot0Raw[0].(*big.Int)
	if !ok {
		return PoolSnapshot{}, errors.New("slot0.sqrtPriceX96 type assertion failed")
	}
	tick, err := asInt32(slot0Raw[1])
	if err != nil {
		return PoolSnapshot{}, fmt.Errorf("slot0.tick type assertion failed: %w", err)
	}

	return PoolSnapshot{
		Address:      pool,
		Token0:       token0,
		Token1:       token1,
		Fee:          fee,
		Liquidity:    new(big.Int).Set(liquidity),
		SqrtPriceX96: new(big.Int).Set(sqrtPriceX96),
		Tick:         tick,
	}, nil
}

func callMethod(ctx context.Context, client *ethclient.Client, contractABI abi.ABI, to common.Address, method string, args ...interface{}) ([]interface{}, error) {
	data, err := contractABI.Pack(method, args...)
	if err != nil {
		return nil, err
	}
	res, err := client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return nil, err
	}
	return contractABI.Unpack(method, res)
}

func resolveArbitrageAmount(ctx context.Context, client *ethclient.Client, vaultABI abi.ABI, cfg Config) (*big.Int, error) {
	if cfg.ArbitrageMaxTradeBps == 0 || cfg.VaultAddress == (common.Address{}) {
		return new(big.Int).Set(cfg.AmountInWei), nil
	}

	totalRaw, err := callMethod(ctx, client, vaultABI, cfg.VaultAddress, "totalAssets")
	if err != nil || len(totalRaw) != 1 {
		return nil, fmt.Errorf("read vault totalAssets failed: %w", err)
	}
	availableRaw, err := callMethod(ctx, client, vaultABI, cfg.VaultAddress, "availableForStrategy", cfg.Source)
	if err != nil || len(availableRaw) != 1 {
		return nil, fmt.Errorf("read vault availableForStrategy failed: %w", err)
	}

	totalAssets, err := asBigInt(totalRaw[0])
	if err != nil {
		return nil, fmt.Errorf("totalAssets type assertion failed: %w", err)
	}
	available, err := asBigInt(availableRaw[0])
	if err != nil {
		return nil, fmt.Errorf("availableForStrategy type assertion failed: %w", err)
	}

	maxByTotal := new(big.Int).Mul(totalAssets, big.NewInt(int64(cfg.ArbitrageMaxTradeBps)))
	maxByTotal.Div(maxByTotal, big.NewInt(bpsDenominator))
	maxAllowed := minBigInt(available, maxByTotal)
	if maxAllowed.Sign() <= 0 {
		return big.NewInt(0), nil
	}

	if cfg.AmountInWei.Cmp(maxAllowed) > 0 {
		return maxAllowed, nil
	}
	return new(big.Int).Set(cfg.AmountInWei), nil
}

func minBigInt(a *big.Int, b *big.Int) *big.Int {
	if a == nil {
		return new(big.Int).Set(b)
	}
	if b == nil {
		return new(big.Int).Set(a)
	}
	if a.Cmp(b) <= 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

func findBestOpportunity(
	weth common.Address,
	token common.Address,
	pairName string,
	amountInWETH *big.Int,
	snaps []PoolSnapshot,
) (*Opportunity, bool) {
	var best *Opportunity
	for i := 0; i < len(snaps); i++ {
		for j := 0; j < len(snaps); j++ {
			if i == j {
				continue
			}

			buy := snaps[i]
			sell := snaps[j]

			buyOut, err := quoteFromSnapshot(buy, weth, amountInWETH)
			if err != nil || buyOut.Sign() <= 0 {
				continue
			}

			wethBack, err := quoteFromSnapshot(sell, token, buyOut)
			if err != nil || wethBack.Sign() <= 0 {
				continue
			}

			gross := new(big.Int).Sub(wethBack, amountInWETH)
			if gross.Sign() <= 0 {
				continue
			}

			candidate := &Opportunity{
				PairName:     pairName,
				Token:        token,
				BuyPool:      buy,
				SellPool:     sell,
				AmountInWETH: new(big.Int).Set(amountInWETH),
				TokenOut:     new(big.Int).Set(buyOut),
				WETHBack:     new(big.Int).Set(wethBack),
				GrossProfit:  new(big.Int).Set(gross),
			}

			if best == nil || candidate.GrossProfit.Cmp(best.GrossProfit) > 0 {
				best = candidate
			}
		}
	}

	if best == nil {
		return nil, false
	}
	return best, true
}

func quoteFromSnapshot(s PoolSnapshot, tokenIn common.Address, amountIn *big.Int) (*big.Int, error) {
	if amountIn.Sign() <= 0 {
		return nil, errors.New("amountIn must be > 0")
	}
	if tokenIn != s.Token0 && tokenIn != s.Token1 {
		return nil, errors.New("tokenIn not in pool")
	}
	tokenInIsToken0 := tokenIn == s.Token0
	return quoteV3AmountOut(amountIn, s.SqrtPriceX96, s.Liquidity, s.Fee, tokenInIsToken0)
}

// quoteV3AmountOut mirrors the OnchainStateLens logic: liquidity + fee + virtual reserves.
func quoteV3AmountOut(amountIn, sqrtPriceX96, liquidity *big.Int, fee uint32, tokenInIsToken0 bool) (*big.Int, error) {
	if amountIn.Sign() <= 0 {
		return nil, errors.New("amountIn must be > 0")
	}
	if sqrtPriceX96.Sign() <= 0 {
		return nil, errors.New("invalid sqrtPriceX96")
	}
	if liquidity.Sign() <= 0 {
		return nil, errors.New("liquidity must be > 0")
	}
	if fee >= feeDenominator {
		return nil, errors.New("fee out of range")
	}

	amountInAfterFee := new(big.Int).Mul(amountIn, big.NewInt(int64(feeDenominator-fee)))
	amountInAfterFee.Div(amountInAfterFee, big.NewInt(feeDenominator))
	if amountInAfterFee.Sign() <= 0 {
		return big.NewInt(0), nil
	}

	reserve0 := new(big.Int).Mul(liquidity, q96)
	reserve0.Div(reserve0, sqrtPriceX96)

	reserve1 := new(big.Int).Mul(liquidity, sqrtPriceX96)
	reserve1.Div(reserve1, q96)

	if reserve0.Sign() <= 0 || reserve1.Sign() <= 0 {
		return nil, errors.New("virtual reserve calculation failed")
	}

	if tokenInIsToken0 {
		numerator := new(big.Int).Mul(reserve1, amountInAfterFee)
		denominator := new(big.Int).Add(reserve0, amountInAfterFee)
		return numerator.Div(numerator, denominator), nil
	}

	numerator := new(big.Int).Mul(reserve0, amountInAfterFee)
	denominator := new(big.Int).Add(reserve1, amountInAfterFee)
	return numerator.Div(numerator, denominator), nil
}

func estimateGasCost(ctx context.Context, client *ethclient.Client, from common.Address, maxGas uint64) (*big.Int, *big.Int, *big.Int, uint64, error) {
	header, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	tipCap, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	if header.BaseFee == nil {
		return nil, nil, nil, 0, errors.New("node did not provide baseFee")
	}

	feeCap := new(big.Int).Add(new(big.Int).Mul(header.BaseFee, big.NewInt(2)), tipCap)
	gasCost := new(big.Int).Mul(new(big.Int).SetUint64(maxGas), feeCap)
	return gasCost, tipCap, feeCap, maxGas, nil
}

func signOpportunity(privateKey *ecdsa.PrivateKey, opp Opportunity) (string, error) {
	packed := []byte(fmt.Sprintf(
		"%s|%s|%s|%s|%s|%s|%s",
		opp.PairName,
		opp.BuyPool.Address.Hex(),
		opp.SellPool.Address.Hex(),
		opp.AmountInWETH.String(),
		opp.GrossProfit.String(),
		opp.NetProfit.String(),
		time.Now().UTC().Format(time.RFC3339),
	))
	hash := crypto.Keccak256Hash(packed)
	sig, err := crypto.Sign(hash.Bytes(), privateKey)
	if err != nil {
		return "", err
	}

	pub, err := crypto.SigToPub(hash.Bytes(), sig)
	if err != nil {
		return "", err
	}
	if crypto.PubkeyToAddress(*pub) != crypto.PubkeyToAddress(privateKey.PublicKey) {
		return "", errors.New("signature verification address mismatch")
	}

	return hexutil.Encode(sig), nil
}

func buildExecuteParams(cfg Config, opp Opportunity) (ExecuteWithSnapshotParams, common.Hash, error) {
	minAmountOut := new(big.Int).Mul(opp.TokenOut, big.NewInt(int64(bpsDenominator-cfg.SlippageBps)))
	minAmountOut.Div(minAmountOut, big.NewInt(bpsDenominator))

	minReturnedAssets := new(big.Int).Mul(opp.AmountInWETH, big.NewInt(int64(bpsDenominator-cfg.MaxLossBps)))
	minReturnedAssets.Div(minReturnedAssets, big.NewInt(bpsDenominator))

	deadline := big.NewInt(time.Now().Unix() + int64(cfg.DeadlineSec))
	params := ExecuteWithSnapshotParams{
		Source:            cfg.Source,
		StrategyParams:    nil,
		Pool:              opp.BuyPool.Address,
		TokenIn:           cfg.WETH,
		TokenOut:          opp.Token,
		SecondaryPool:     opp.SellPool.Address,
		SecondaryTokenIn:  opp.Token,
		SecondaryTokenOut: cfg.WETH,
		AmountIn:          new(big.Int).Set(opp.AmountInWETH),
		MinAmountOut:      minAmountOut,
		MinReturnedAssets: minReturnedAssets,
		MinProfit:         new(big.Int).Set(cfg.MinProfitWei),
		MaxLossBps:        uint16(cfg.MaxLossBps),
		Deadline:          deadline,
	}

	contextHash, err := computeExecutionContextHash(params, opp.BuyPool, opp.SellPool)
	if err != nil {
		return ExecuteWithSnapshotParams{}, common.Hash{}, err
	}

	strategyParams, err := buildStrategyParams(cfg, opp, params, contextHash)
	if err != nil {
		return ExecuteWithSnapshotParams{}, common.Hash{}, err
	}
	params.StrategyParams = strategyParams

	return params, contextHash, nil
}

func computeExecutionContextHash(params ExecuteWithSnapshotParams, buySnap PoolSnapshot, sellSnap PoolSnapshot) (common.Hash, error) {
	primaryArgs, err := primaryContextHashArguments()
	if err != nil {
		return common.Hash{}, err
	}
	primaryPacked, err := primaryArgs.Pack(
		params.Source,
		params.Pool,
		params.TokenIn,
		params.TokenOut,
		params.AmountIn,
		params.MinAmountOut,
		params.MinReturnedAssets,
		params.MinProfit,
		params.MaxLossBps,
		params.Deadline,
		new(big.Int).SetUint64(uint64(buySnap.Fee)),
		buySnap.Liquidity,
		buySnap.SqrtPriceX96,
		big.NewInt(int64(buySnap.Tick)),
	)
	if err != nil {
		return common.Hash{}, err
	}

	secondaryArgs, err := secondaryContextHashArguments()
	if err != nil {
		return common.Hash{}, err
	}
	secondaryPacked, err := secondaryArgs.Pack(
		params.SecondaryPool,
		params.SecondaryTokenIn,
		params.SecondaryTokenOut,
		new(big.Int).SetUint64(uint64(sellSnap.Fee)),
		sellSnap.Liquidity,
		sellSnap.SqrtPriceX96,
		big.NewInt(int64(sellSnap.Tick)),
	)
	if err != nil {
		return common.Hash{}, err
	}

	primaryHash := crypto.Keccak256Hash(primaryPacked)
	secondaryHash := crypto.Keccak256Hash(secondaryPacked)

	combined := make([]byte, 0, 64)
	combined = append(combined, primaryHash.Bytes()...)
	combined = append(combined, secondaryHash.Bytes()...)
	return crypto.Keccak256Hash(combined), nil
}

func primaryContextHashArguments() (abi.Arguments, error) {
	addressT, err := abi.NewType("address", "", nil)
	if err != nil {
		return nil, err
	}
	uint256T, err := abi.NewType("uint256", "", nil)
	if err != nil {
		return nil, err
	}
	uint16T, err := abi.NewType("uint16", "", nil)
	if err != nil {
		return nil, err
	}
	uint24T, err := abi.NewType("uint24", "", nil)
	if err != nil {
		return nil, err
	}
	uint128T, err := abi.NewType("uint128", "", nil)
	if err != nil {
		return nil, err
	}
	uint160T, err := abi.NewType("uint160", "", nil)
	if err != nil {
		return nil, err
	}
	int24T, err := abi.NewType("int24", "", nil)
	if err != nil {
		return nil, err
	}

	return abi.Arguments{
		{Type: addressT},
		{Type: addressT},
		{Type: addressT},
		{Type: addressT},
		{Type: uint256T},
		{Type: uint256T},
		{Type: uint256T},
		{Type: uint256T},
		{Type: uint16T},
		{Type: uint256T},
		{Type: uint24T},
		{Type: uint128T},
		{Type: uint160T},
		{Type: int24T},
	}, nil
}

func secondaryContextHashArguments() (abi.Arguments, error) {
	addressT, err := abi.NewType("address", "", nil)
	if err != nil {
		return nil, err
	}
	uint24T, err := abi.NewType("uint24", "", nil)
	if err != nil {
		return nil, err
	}
	uint128T, err := abi.NewType("uint128", "", nil)
	if err != nil {
		return nil, err
	}
	uint160T, err := abi.NewType("uint160", "", nil)
	if err != nil {
		return nil, err
	}
	int24T, err := abi.NewType("int24", "", nil)
	if err != nil {
		return nil, err
	}

	return abi.Arguments{
		{Type: addressT},
		{Type: addressT},
		{Type: addressT},
		{Type: uint24T},
		{Type: uint128T},
		{Type: uint160T},
		{Type: int24T},
	}, nil
}

func buildStrategyParams(cfg Config, opp Opportunity, execParams ExecuteWithSnapshotParams, contextHash common.Hash) ([]byte, error) {
	switch strings.ToLower(cfg.StrategyMode) {
	case "amm":
		args, err := ammStrategyArguments()
		if err != nil {
			return nil, err
		}

		minFinalAmountOut := new(big.Int).Add(new(big.Int).Set(execParams.AmountIn), execParams.MinProfit)
		return args.Pack(
			contextHash,
			opp.BuyPool.Address,
			opp.SellPool.Address,
			execParams.TokenIn,
			execParams.TokenOut,
			new(big.Int).SetUint64(uint64(opp.BuyPool.Fee)),
			new(big.Int).SetUint64(uint64(opp.SellPool.Fee)),
			new(big.Int).Set(execParams.AmountIn),
			new(big.Int).Set(execParams.MinAmountOut),
			minFinalAmountOut,
			new(big.Int).Set(execParams.Deadline),
		)
	case "dummy":
		uint256T, err := abi.NewType("uint256", "", nil)
		if err != nil {
			return nil, err
		}
		bytes32T, err := abi.NewType("bytes32", "", nil)
		if err != nil {
			return nil, err
		}
		args := abi.Arguments{{Type: uint256T}, {Type: bytes32T}}
		return args.Pack(cfg.DummyPayoutWei, contextHash)
	case "raw":
		if len(cfg.RawStrategyParams) == 0 {
			return nil, errors.New("STRATEGY_MODE=raw but RAW_STRATEGY_PARAMS_HEX is empty")
		}
		return cfg.RawStrategyParams, nil
	default:
		return nil, fmt.Errorf("unknown STRATEGY_MODE: %s", cfg.StrategyMode)
	}
}

func ammStrategyArguments() (abi.Arguments, error) {
	bytes32T, err := abi.NewType("bytes32", "", nil)
	if err != nil {
		return nil, err
	}
	addressT, err := abi.NewType("address", "", nil)
	if err != nil {
		return nil, err
	}
	uint24T, err := abi.NewType("uint24", "", nil)
	if err != nil {
		return nil, err
	}
	uint256T, err := abi.NewType("uint256", "", nil)
	if err != nil {
		return nil, err
	}

	return abi.Arguments{
		{Type: bytes32T}, // expectedContextHash
		{Type: addressT}, // buyPool
		{Type: addressT}, // sellPool
		{Type: addressT}, // tokenIn
		{Type: addressT}, // tokenMid
		{Type: uint24T},  // buyFee
		{Type: uint24T},  // sellFee
		{Type: uint256T}, // amountIn
		{Type: uint256T}, // minBuyAmountOut
		{Type: uint256T}, // minFinalAmountOut
		{Type: uint256T}, // deadline
	}, nil
}

func sendExecuteTx(
	ctx context.Context,
	client *ethclient.Client,
	atomicABI abi.ABI,
	cfg Config,
	privateKey *ecdsa.PrivateKey,
	from common.Address,
	params ExecuteWithSnapshotParams,
	gasLimit uint64,
	tipCap *big.Int,
	feeCap *big.Int,
) (common.Hash, error) {
	data, err := atomicABI.Pack("executeWithSnapshot", params)
	if err != nil {
		return common.Hash{}, err
	}

	estimated, err := client.EstimateGas(ctx, ethereum.CallMsg{
		From:      from,
		To:        &cfg.AtomicExecutor,
		GasFeeCap: feeCap,
		GasTipCap: tipCap,
		Data:      data,
	})
	if err == nil && estimated > 0 {
		// Multiply estimated gas by 1.2 to reduce edge-case failures.
		gasLimit = estimated * 12 / 10
	}

	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, err
	}

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   cfg.ChainID,
		Nonce:     nonce,
		GasTipCap: tipCap,
		GasFeeCap: feeCap,
		Gas:       gasLimit,
		To:        &cfg.AtomicExecutor,
		Value:     big.NewInt(0),
		Data:      data,
	})

	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(cfg.ChainID), privateKey)
	if err != nil {
		return common.Hash{}, err
	}

	if err = client.SendTransaction(ctx, signedTx); err != nil {
		return common.Hash{}, err
	}

	return signedTx.Hash(), nil
}

func loadConfig() (Config, error) {
	_ = loadDotEnv("../../.env")
	_ = loadDotEnv("../.env")
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, fmt.Errorf("read .env failed: %w", err)
	}

	rebalanceSourceFallback := strings.TrimSpace(os.Getenv("SOURCE_ADDRESS"))

	cfg := Config{
		RPCURL:                         os.Getenv("RPC_URL"),
		PrivateKeyHex:                  os.Getenv("PRIVATE_KEY_HEX"),
		EnableArbitrageBot:             mustBoolWithDefault("ENABLE_ARBITRAGE_BOT", true),
		EnableRebalanceBot:             mustBoolWithDefault("ENABLE_REBALANCE_BOT", false),
		VaultAddress:                   common.HexToAddress(os.Getenv("VAULT_ADDRESS")),
		AtomicExecutor:                 common.HexToAddress(os.Getenv("ATOMIC_EXECUTOR_ADDRESS")),
		Source:                         common.HexToAddress(os.Getenv("SOURCE_ADDRESS")),
		StrategyRouter:                 common.HexToAddress(os.Getenv("STRATEGY_ROUTER_ADDRESS")),
		RebalanceSource:                common.HexToAddress(mustStringWithDefault("REBALANCE_SOURCE_ADDRESS", rebalanceSourceFallback)),
		RebalancePool:                  common.HexToAddress(mustStringWithDefault("REBALANCE_POOL_ADDRESS", "0x6418EEC70f50913ff0d756B48d32Ce7C02b47C47")),
		PositionManager:                common.HexToAddress(mustStringWithDefault("POSITION_MANAGER_ADDRESS", "0x1238536071E1c677A632429e3655c799b22cDA52")),
		AmountInWei:                    mustBigIntWithDefault("AMOUNT_IN_WEI", "100000000000000000"),
		MinProfitWei:                   mustBigIntWithDefault("MIN_PROFIT_WEI", "10000000000000000"),
		MinNetProfitWei:                mustBigIntWithDefault("MIN_NET_PROFIT_WEI", "1000000000000000"),
		MaxLossBps:                     mustUintWithDefault("MAX_LOSS_BPS", 300),
		ArbitrageMaxTradeBps:           mustUintWithDefault("ARBITRAGE_MAX_TRADE_BPS", 1000),
		DummyPayoutWei:                 mustBigIntWithDefault("DUMMY_PAYOUT_WEI", "20000000000000000"),
		SlippageBps:                    mustUintWithDefault("SLIPPAGE_BPS", 50),
		DeadlineSec:                    mustUintWithDefault("DEADLINE_SEC", 30),
		LoopIntervalSec:                mustUintWithDefault("LOOP_INTERVAL_SEC", 5),
		MaxGasLimit:                    mustUintWithDefault("MAX_GAS_LIMIT", 700000),
		RebalanceGasLimit:              mustUintWithDefault("REBALANCE_GAS_LIMIT", 1200000),
		RebalanceATRLength:             mustIntWithDefault("REBALANCE_ATR_LENGTH", 14),
		RebalanceATRPeriodSec:          mustUintWithDefault("REBALANCE_ATR_PERIOD_SEC", 3600),
		RebalanceATRMultiplier:         mustFloatWithDefault("REBALANCE_ATR_MULTIPLIER", 2.0),
		RebalanceRangeBpsMin:           mustUintWithDefault("REBALANCE_RANGE_BPS_MIN", 1000),
		RebalanceRangeBpsMax:           mustUintWithDefault("REBALANCE_RANGE_BPS_MAX", 1500),
		RebalanceAdxLength:             mustIntWithDefault("REBALANCE_ADX_LENGTH", 14),
		RebalanceAdxPeriodSec:          mustUintWithDefault("REBALANCE_ADX_PERIOD_SEC", 86400),
		RebalanceAdxTrendThreshold:     mustFloatWithDefault("REBALANCE_ADX_TREND_THRESHOLD", 25.0),
		RebalanceExpectedFeeDays:       mustFloatWithDefault("REBALANCE_EXPECTED_FEE_DAYS", 7.0),
		RebalanceDailyVolumeAsset:      mustFloatWithDefault("REBALANCE_DAILY_VOLUME_ASSET", 0.0),
		RebalanceTriggerZoneBps:        mustUintWithDefault("REBALANCE_TRIGGER_ZONE_BPS", 2000),
		RebalanceCostMultiplierBps:     mustUintWithDefault("REBALANCE_COST_MULTIPLIER_BPS", 15000),
		RebalancePaybackThresholdHours: mustFloatWithDefault("REBALANCE_PAYBACK_THRESHOLD_HOURS", 72.0),
		RebalanceCooldownSec:           mustUintWithDefault("REBALANCE_COOLDOWN_SEC", 3600),
		RebalanceStateFile:             mustStringWithDefault("REBALANCE_STATE_FILE", "./rebalance_state.json"),
		RebalanceMinSwapOut:            mustBigIntWithDefault("REBALANCE_MIN_SWAP_OUT", "1"),
		RebalanceAmount0Min:            mustBigIntWithDefault("REBALANCE_AMOUNT0_MIN", "0"),
		RebalanceAmount1Min:            mustBigIntWithDefault("REBALANCE_AMOUNT1_MIN", "0"),
		RebalancePrincipalToSettle:     mustBigIntWithDefault("REBALANCE_PRINCIPAL_TO_SETTLE", "0"),
		RebalanceMaxLossBps:            mustUintWithDefault("REBALANCE_MAX_LOSS_BPS", 10000),
		RebalanceMinReturnedAssets:     mustBigIntWithDefault("REBALANCE_MIN_RETURNED_ASSETS", "0"),
		RebalanceBurnPosition:          mustBoolWithDefault("REBALANCE_BURN_POSITION", true),
		DryRun:                         mustBoolWithDefault("DRY_RUN", true),
		StrategyMode:                   strings.ToLower(mustStringWithDefault("STRATEGY_MODE", "amm")),
		RawStrategyParams:              mustBytesWithDefault("RAW_STRATEGY_PARAMS_HEX", ""),
		WETH:                           common.HexToAddress(mustStringWithDefault("WETH_ADDRESS", "0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14")),
		METH:                           common.HexToAddress(mustStringWithDefault("METH_ADDRESS", "0x4f7A67464B5976d7547c860109e4432d50AfB38e")),
		UNI:                            common.HexToAddress(mustStringWithDefault("UNI_ADDRESS", "0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984")),
		USDC:                           common.HexToAddress(mustStringWithDefault("USDC_ADDRESS", "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")),
		PoolsMETH: []common.Address{
			common.HexToAddress("0x84F491DD1e1Bb2b251bEA2CAb9ac6849E94bfBC5"),
			common.HexToAddress("0x972894Ed8c33AC5041795a8022fca3908cfe7a8C"),
		},
		PoolsUNI: []common.Address{
			common.HexToAddress("0x287B0e934ed0439E2a7b1d5F0FC25eA2c24b64f7"),
			common.HexToAddress("0x51aDC79e7760aC5317a0d05e7a64c4f9cB2d4369"),
			common.HexToAddress("0x224Cc4e5b50036108C1d862442365054600c260C"),
			common.HexToAddress("0xb8b672bdd9cFF3D0979e7344c7358CA12E78a1F0"),
		},
		PoolsUSDC: []common.Address{
			common.HexToAddress("0x3289680dD4d6C10bb19b899729cda5eEF58AEfF1"),
			common.HexToAddress("0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50"),
			common.HexToAddress("0xFeEd501c2B21D315F04946F85fC6416B640240b5"),
			common.HexToAddress("0x6418EEC70f50913ff0d756B48d32Ce7C02b47C47"),
		},
	}

	if v := os.Getenv("CHAIN_ID"); v != "" {
		chainID, ok := new(big.Int).SetString(v, 10)
		if !ok {
			return Config{}, errors.New("CHAIN_ID is not a valid integer")
		}
		cfg.ChainID = chainID
	}

	if cfg.RPCURL == "" {
		return Config{}, errors.New("missing RPC_URL")
	}
	if !cfg.EnableArbitrageBot && !cfg.EnableRebalanceBot {
		return Config{}, errors.New("ENABLE_ARBITRAGE_BOT and ENABLE_REBALANCE_BOT cannot both be false")
	}
	if cfg.PrivateKeyHex == "" {
		return Config{}, errors.New("missing PRIVATE_KEY_HEX")
	}

	if cfg.EnableArbitrageBot {
		if cfg.AtomicExecutor == (common.Address{}) {
			return Config{}, errors.New("arb mode requires ATOMIC_EXECUTOR_ADDRESS")
		}
		if cfg.Source == (common.Address{}) {
			return Config{}, errors.New("arb mode requires SOURCE_ADDRESS")
		}
		if cfg.MaxLossBps > bpsDenominator {
			return Config{}, errors.New("MAX_LOSS_BPS must be 0..10000")
		}
		if cfg.ArbitrageMaxTradeBps > bpsDenominator {
			return Config{}, errors.New("ARBITRAGE_MAX_TRADE_BPS must be 0..10000")
		}
		if cfg.AmountInWei.Sign() <= 0 {
			return Config{}, errors.New("AMOUNT_IN_WEI must be > 0")
		}
		if cfg.METH == (common.Address{}) {
			return Config{}, errors.New("METH_ADDRESS must not be zero address")
		}
	}

	if cfg.EnableRebalanceBot {
		if cfg.StrategyRouter == (common.Address{}) {
			return Config{}, errors.New("rebalance mode requires STRATEGY_ROUTER_ADDRESS")
		}
		if cfg.RebalanceSource == (common.Address{}) {
			return Config{}, errors.New("rebalance mode requires REBALANCE_SOURCE_ADDRESS")
		}
		if cfg.RebalancePool == (common.Address{}) {
			return Config{}, errors.New("rebalance mode requires REBALANCE_POOL_ADDRESS")
		}
		if cfg.PositionManager == (common.Address{}) {
			return Config{}, errors.New("rebalance mode requires POSITION_MANAGER_ADDRESS")
		}
		if cfg.RebalanceMaxLossBps > bpsDenominator {
			return Config{}, errors.New("REBALANCE_MAX_LOSS_BPS must be 0..10000")
		}
		if cfg.RebalanceATRLength < 2 {
			return Config{}, errors.New("REBALANCE_ATR_LENGTH must be >= 2")
		}
		if cfg.RebalanceATRPeriodSec == 0 {
			return Config{}, errors.New("REBALANCE_ATR_PERIOD_SEC must be > 0")
		}
		if cfg.RebalanceATRMultiplier <= 0 {
			return Config{}, errors.New("REBALANCE_ATR_MULTIPLIER must be > 0")
		}
		if cfg.RebalanceRangeBpsMin == 0 || cfg.RebalanceRangeBpsMax == 0 {
			return Config{}, errors.New("REBALANCE_RANGE_BPS_MIN/MAX must be > 0")
		}
		if cfg.RebalanceRangeBpsMin > cfg.RebalanceRangeBpsMax {
			return Config{}, errors.New("REBALANCE_RANGE_BPS_MIN must not exceed REBALANCE_RANGE_BPS_MAX")
		}
		if cfg.RebalanceRangeBpsMax > bpsDenominator {
			return Config{}, errors.New("REBALANCE_RANGE_BPS_MAX must not exceed 10000")
		}
		if cfg.RebalanceAdxLength < 2 {
			return Config{}, errors.New("REBALANCE_ADX_LENGTH must be >= 2")
		}
		if cfg.RebalanceAdxPeriodSec == 0 {
			return Config{}, errors.New("REBALANCE_ADX_PERIOD_SEC must be > 0")
		}
		if cfg.RebalanceAdxTrendThreshold <= 0 {
			return Config{}, errors.New("REBALANCE_ADX_TREND_THRESHOLD must be > 0")
		}
		if cfg.RebalanceExpectedFeeDays <= 0 {
			return Config{}, errors.New("REBALANCE_EXPECTED_FEE_DAYS must be > 0")
		}
		if cfg.RebalanceDailyVolumeAsset < 0 {
			return Config{}, errors.New("REBALANCE_DAILY_VOLUME_ASSET must not be < 0")
		}
		if cfg.RebalanceTriggerZoneBps > bpsDenominator {
			return Config{}, errors.New("REBALANCE_TRIGGER_ZONE_BPS must not exceed 10000")
		}
		if cfg.RebalanceCostMultiplierBps == 0 {
			return Config{}, errors.New("REBALANCE_COST_MULTIPLIER_BPS must be > 0")
		}
		if cfg.RebalancePaybackThresholdHours <= 0 {
			return Config{}, errors.New("REBALANCE_PAYBACK_THRESHOLD_HOURS must be > 0")
		}
	}

	return cfg, nil
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}

		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		if _, exists := os.LookupEnv(key); exists {
			continue
		}

		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func mustStringWithDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func mustBigIntWithDefault(key, fallback string) *big.Int {
	v := mustStringWithDefault(key, fallback)
	n, ok := new(big.Int).SetString(v, 10)
	if !ok {
		panic(fmt.Sprintf("%s is not a valid decimal integer", key))
	}
	return n
}

func mustUintWithDefault(key string, fallback uint64) uint64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		panic(fmt.Sprintf("%s is not a valid integer: %v", key, err))
	}
	return n
}

func mustIntWithDefault(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		panic(fmt.Sprintf("%s is not a valid integer: %v", key, err))
	}
	return n
}

func mustFloatWithDefault(key string, fallback float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		panic(fmt.Sprintf("%s is not a valid float: %v", key, err))
	}
	return f
}

func mustBoolWithDefault(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		panic(fmt.Sprintf("%s is not a valid bool: %v", key, err))
	}
	return b
}

func mustBytesWithDefault(key, fallback string) []byte {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		v = fallback
	}
	if v == "" {
		return nil
	}
	b, err := hexutil.Decode(v)
	if err != nil {
		panic(fmt.Sprintf("%s is not valid hex bytes: %v", key, err))
	}
	return b
}

func asUint32(v interface{}) (uint32, error) {
	switch t := v.(type) {
	case uint8:
		return uint32(t), nil
	case uint16:
		return uint32(t), nil
	case uint32:
		return t, nil
	case uint64:
		return uint32(t), nil
	case *big.Int:
		return uint32(t.Uint64()), nil
	default:
		return 0, fmt.Errorf("unknown uint32 type: %T", v)
	}
}

func asInt32(v interface{}) (int32, error) {
	switch t := v.(type) {
	case int8:
		return int32(t), nil
	case int16:
		return int32(t), nil
	case int32:
		return t, nil
	case int64:
		return int32(t), nil
	case *big.Int:
		return int32(t.Int64()), nil
	default:
		return 0, fmt.Errorf("unknown int32 type: %T", v)
	}
}
