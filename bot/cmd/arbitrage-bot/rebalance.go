package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	lpMinTick = -887272
	lpMaxTick = 887272
)

var (
	strategyRouterABIJSON = `[
		{"inputs":[{"internalType":"address","name":"source","type":"address"},{"internalType":"bytes","name":"params","type":"bytes"},{"internalType":"bytes32","name":"executionContextHash","type":"bytes32"},{"internalType":"uint256","name":"principalToSettle","type":"uint256"},{"internalType":"uint16","name":"maxLossBps","type":"uint16"},{"internalType":"uint256","name":"minReturnedAssets","type":"uint256"}],"name":"executeManagedStrategy","outputs":[{"internalType":"int256","name":"realizedPnl","type":"int256"},{"internalType":"uint256","name":"returnedAssets","type":"uint256"},{"internalType":"uint256","name":"profit","type":"uint256"},{"internalType":"uint256","name":"loss","type":"uint256"}],"stateMutability":"nonpayable","type":"function"},
		{"inputs":[{"internalType":"address","name":"source","type":"address"},{"internalType":"bytes","name":"params","type":"bytes"},{"internalType":"bytes32","name":"executionContextHash","type":"bytes32"},{"internalType":"uint256","name":"amountToAllocate","type":"uint256"},{"internalType":"uint256","name":"principalToSettle","type":"uint256"},{"internalType":"uint16","name":"maxLossBps","type":"uint16"},{"internalType":"uint256","name":"minReturnedAssets","type":"uint256"}],"name":"allocateAndExecuteManagedStrategy","outputs":[{"internalType":"int256","name":"realizedPnl","type":"int256"},{"internalType":"uint256","name":"returnedAssets","type":"uint256"},{"internalType":"uint256","name":"profit","type":"uint256"},{"internalType":"uint256","name":"loss","type":"uint256"}],"stateMutability":"nonpayable","type":"function"}
	]`

	lpStrategyABIJSON = `[
		{"inputs":[],"name":"activeTokenId","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
		{"inputs":[],"name":"asset","outputs":[{"internalType":"address","name":"","type":"address"}],"stateMutability":"view","type":"function"}
	]`

	positionManagerABIJSON = `[
		{"inputs":[{"internalType":"uint256","name":"tokenId","type":"uint256"}],"name":"positions","outputs":[{"internalType":"uint96","name":"nonce","type":"uint96"},{"internalType":"address","name":"operator","type":"address"},{"internalType":"address","name":"token0","type":"address"},{"internalType":"address","name":"token1","type":"address"},{"internalType":"uint24","name":"fee","type":"uint24"},{"internalType":"int24","name":"tickLower","type":"int24"},{"internalType":"int24","name":"tickUpper","type":"int24"},{"internalType":"uint128","name":"liquidity","type":"uint128"},{"internalType":"uint256","name":"feeGrowthInside0LastX128","type":"uint256"},{"internalType":"uint256","name":"feeGrowthInside1LastX128","type":"uint256"},{"internalType":"uint128","name":"tokensOwed0","type":"uint128"},{"internalType":"uint128","name":"tokensOwed1","type":"uint128"}],"stateMutability":"view","type":"function"}
	]`
)

// RebalanceRunner maintains rebalance state and runs ADX/ATR + golden rule decisions.
type RebalanceRunner struct {
	routerABI      abi.ABI
	strategyABI    abi.ABI
	positionMgrABI abi.ABI
	vaultABI       abi.ABI
	state          *RebalanceState
}

type RebalanceState struct {
	BasePrice         float64       `json:"basePrice"`
	LastRebalanceUnix int64         `json:"lastRebalanceUnix"`
	AtrCandles        []PriceCandle `json:"atrCandles"`
	AdxCandles        []PriceCandle `json:"adxCandles"`
	Candles           []PriceCandle `json:"candles,omitempty"`
}

type PriceCandle struct {
	StartUnix int64   `json:"startUnix"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
}

type LPPositionInfo struct {
	Token0      common.Address
	Token1      common.Address
	Fee         uint32
	TickLower   int32
	TickUpper   int32
	Liquidity   *big.Int
	TokensOwed0 *big.Int
	TokensOwed1 *big.Int
}

func NewRebalanceRunner(cfg Config) (*RebalanceRunner, error) {
	routerABI, err := abi.JSON(strings.NewReader(strategyRouterABIJSON))
	if err != nil {
		return nil, fmt.Errorf("parse StrategyRouter ABI failed: %w", err)
	}
	strategyABI, err := abi.JSON(strings.NewReader(lpStrategyABIJSON))
	if err != nil {
		return nil, fmt.Errorf("parse LP Strategy ABI failed: %w", err)
	}
	positionMgrABI, err := abi.JSON(strings.NewReader(positionManagerABIJSON))
	if err != nil {
		return nil, fmt.Errorf("parse PositionManager ABI failed: %w", err)
	}

	state, err := loadRebalanceState(cfg.RebalanceStateFile)
	if err != nil {
		return nil, err
	}
	if len(state.AtrCandles) == 0 && len(state.Candles) > 0 {
		state.AtrCandles = state.Candles
	}
	if state.AtrCandles == nil {
		state.AtrCandles = []PriceCandle{}
	}
	if state.AdxCandles == nil {
		state.AdxCandles = []PriceCandle{}
	}

	parsedVault, err := abi.JSON(strings.NewReader(vaultABIJSON))
	if err != nil {
		return nil, err
	}

	return &RebalanceRunner{
		routerABI:      routerABI,
		strategyABI:    strategyABI,
		positionMgrABI: positionMgrABI,
		vaultABI:       parsedVault,
		state:          state,
	}, nil
}

func (r *RebalanceRunner) RunCycle(
	ctx context.Context,
	client *ethclient.Client,
	poolABI abi.ABI,
	cfg Config,
	privateKey *ecdsa.PrivateKey,
	from common.Address,
) (retErr error) {
	defer func() {
		if err := saveRebalanceState(cfg.RebalanceStateFile, r.state); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("save rebalance state failed: %w", err)
			} else {
				log.Printf("rebalance state write failed: %v", err)
			}
		}
	}()

	snap, err := fetchSnapshot(ctx, client, poolABI, cfg.RebalancePool)
	if err != nil {
		return fmt.Errorf("read rebalance pool snapshot failed: %w", err)
	}

	price, err := priceFromSqrtPriceX96(snap.SqrtPriceX96)
	if err != nil {
		return fmt.Errorf("pool price conversion failed: %w", err)
	}

	nowUnix := time.Now().Unix()
	if r.state.BasePrice <= 0 {
		r.state.BasePrice = price
	}
	if r.state.LastRebalanceUnix == 0 {
		r.state.LastRebalanceUnix = nowUnix
	}

	maxAtrCandles := cfg.RebalanceATRLength * 4
	if maxAtrCandles < cfg.RebalanceATRLength+2 {
		maxAtrCandles = cfg.RebalanceATRLength + 2
	}
	updateCandleSeries(&r.state.AtrCandles, nowUnix, price, int64(cfg.RebalanceATRPeriodSec), maxAtrCandles)

	maxAdxCandles := cfg.RebalanceAdxLength * 4
	if maxAdxCandles < cfg.RebalanceAdxLength+2 {
		maxAdxCandles = cfg.RebalanceAdxLength + 2
	}
	updateCandleSeries(&r.state.AdxCandles, nowUnix, price, int64(cfg.RebalanceAdxPeriodSec), maxAdxCandles)

	atr, ok := computeATR(r.state.AtrCandles, cfg.RebalanceATRLength)
	if !ok {
		log.Printf("rebalance waiting for ATR data: candles=%d, need=%d", len(r.state.AtrCandles), cfg.RebalanceATRLength)
		return nil
	}
	adx, ok := computeADX(r.state.AdxCandles, cfg.RebalanceAdxLength)
	if !ok {
		log.Printf("rebalance waiting for ADX data: candles=%d, need=%d", len(r.state.AdxCandles), cfg.RebalanceAdxLength)
		return nil
	}

	rangeHalf := cfg.RebalanceATRMultiplier * atr
	minRange := price * float64(cfg.RebalanceRangeBpsMin) / float64(bpsDenominator)
	maxRange := price * float64(cfg.RebalanceRangeBpsMax) / float64(bpsDenominator)
	if rangeHalf < minRange {
		rangeHalf = minRange
	}
	if maxRange > 0 && rangeHalf > maxRange {
		rangeHalf = maxRange
	}
	if rangeHalf <= 0 {
		log.Printf("rebalance skip: invalid ATR range (atr=%.10f)", atr)
		return nil
	}

	lower := r.state.BasePrice - rangeHalf
	upper := r.state.BasePrice + rangeHalf
	if lower <= 0 || upper <= lower {
		log.Printf("rebalance skip: invalid dynamic range (base=%.10f, lower=%.10f, upper=%.10f)", r.state.BasePrice, lower, upper)
		return nil
	}

	outOfRange := price < lower || price > upper
	if !outOfRange {
		log.Printf(
			"rebalance not triggered: price=%.10f, base=%.10f, atr=%.10f, adx=%.2f, range=[%.10f, %.10f]",
			price,
			r.state.BasePrice,
			atr,
			adx,
			lower,
			upper,
		)
		return nil
	}

	if adx < cfg.RebalanceAdxTrendThreshold {
		log.Printf(
			"rebalance skip: price out of range but in sideways market (adx=%.2f < %.2f)",
			adx,
			cfg.RebalanceAdxTrendThreshold,
		)
		return nil
	}

	activeTokenID, err := r.fetchActiveTokenID(ctx, client, cfg.RebalanceSource)
	if err != nil {
		return fmt.Errorf("read activeTokenId failed: %w", err)
	}
	isBootstrap := false
	if activeTokenID.Sign() == 0 {
		isBootstrap = true
		log.Printf("rebalance triggering bootstrap: source=%s has no active LP position", cfg.RebalanceSource.Hex())
	}

	assetAddr, err := r.fetchStrategyAsset(ctx, client, cfg.RebalanceSource)
	if err != nil {
		return fmt.Errorf("read strategy asset failed: %w", err)
	}
	if assetAddr != cfg.WETH {
		log.Printf("rebalance skip: strategy asset=%s is not WETH, cannot unify with gas cost", assetAddr.Hex())
		return nil
	}

	var expectedFees, divergenceLoss, gasCostAsset, swapCost float64
	var goldenRuleOk bool

	spacing, err := fetchTickSpacing(ctx, client, poolABI, cfg.RebalancePool)
	if err != nil {
		return fmt.Errorf("read tickSpacing failed: %w", err)
	}

	gasCostWei, tipCap, feeCap, gasLimit, err := estimateGasCost(ctx, client, from, cfg.RebalanceGasLimit)
	if err != nil {
		return fmt.Errorf("estimate rebalance gas cost failed: %w", err)
	}
	gasCostAsset = bigIntToFloat(gasCostWei)

	if !isBootstrap {
		position, err := r.fetchPositionInfo(ctx, client, cfg.PositionManager, activeTokenID)
		if err != nil {
			return fmt.Errorf("read position info failed: %w", err)
		}

		liquidityF := bigIntToFloat(position.Liquidity)
		amount0, amount1 := estimatePositionAmountsFromLiquidity(liquidityF, position.TickLower, position.TickUpper, price)
		positionValueAsset, comp0Asset, comp1Asset, err := valueInAsset(amount0, amount1, assetAddr, position.Token0, position.Token1, price)
		if err != nil {
			return fmt.Errorf("position value conversion failed: %w", err)
		}
		imbalanceShare := estimateImbalanceShare(comp0Asset, comp1Asset)
		swapAmountAsset := positionValueAsset * imbalanceShare
		slippageCost := swapAmountAsset * float64(cfg.SlippageBps) / float64(bpsDenominator)
		swapFeeCost := swapAmountAsset * float64(snap.Fee) / float64(feeDenominator)
		swapCost = slippageCost + swapFeeCost

		poolLiquidity := bigIntToFloat(snap.Liquidity)
		liquidityShare := 0.0
		if poolLiquidity > 0 && liquidityF > 0 {
			liquidityShare = liquidityF / poolLiquidity
		}

		expectedFees = estimateExpectedFees(cfg.RebalanceDailyVolumeAsset, snap.Fee, liquidityShare, cfg.RebalanceExpectedFeeDays) * 1e18
		divergenceLoss = positionValueAsset * estimateImpermanentLoss(r.state.BasePrice, price)
		goldenRuleCost := divergenceLoss + gasCostAsset + swapCost
		goldenRuleOk = expectedFees > goldenRuleCost
	}

	targetLower := price - rangeHalf
	targetUpper := price + rangeHalf
	newLowerTick, newUpperTick := buildTargetTicksFromRange(targetLower, targetUpper, spacing, snap.Tick)
	deadlineUnix := nowUnix + int64(cfg.DeadlineSec)
	contextHash := buildRebalanceContextHash(nowUnix, cfg.RebalancePool, activeTokenID, newLowerTick, newUpperTick, r.state.BasePrice, price, atr)
	
	action := uint8(0)
	actionLabel := "rebalance"
	if isBootstrap {
		action = 3
		actionLabel = "bootstrap"
	} else if !goldenRuleOk {
		action = 1
		actionLabel = "withdraw"
	}

	strategyParams, err := encodeRebalanceRouterParams(
		contextHash,
		action,
		newLowerTick,
		newUpperTick,
		snap.Fee,
		cfg.RebalanceMinSwapOut,
		cfg.RebalanceAmount0Min,
		cfg.RebalanceAmount1Min,
		deadlineUnix,
		cfg.RebalanceBurnPosition,
	)
	if err != nil {
		return fmt.Errorf("build rebalance params failed: %w", err)
	}

	log.Printf(
		"rebalance triggered: action=%s, tokenId=%s, price=%.10f, atr=%.10f, adx=%.2f, range=[%.10f, %.10f], ticks=[%d,%d], E[fees]=%.10f, L_div=%.10f, C_gas=%.10f, C_swap=%.10f",
		actionLabel,
		activeTokenID.String(),
		price,
		atr,
		adx,
		lower,
		upper,
		newLowerTick,
		newUpperTick,
		expectedFees,
		divergenceLoss,
		gasCostAsset,
		swapCost,
	)

	if cfg.DryRun {
		log.Printf("rebalance dry-run: tx not sent")
		return nil
	}

	var txHash common.Hash

	if isBootstrap {
		availToAlloc, err := r.fetchAvailableForStrategy(ctx, client, cfg.VaultAddress, cfg.RebalanceSource)
		if err != nil {
			return fmt.Errorf("fetch availableForStrategy failed: %w", err)
		}
		if availToAlloc.Sign() <= 0 {
			log.Printf("bootstrap skip: vault has 0 available capital for strategy")
			return nil
		}
		txHash, err = r.sendAllocateAndExecuteManagedStrategyTx(
			ctx, client, cfg, privateKey, from, strategyParams, contextHash, availToAlloc, gasLimit, tipCap, feeCap,
		)
		if err != nil {
			return fmt.Errorf("send bootstrap tx failed: %w", err)
		}
		log.Printf("bootstrap tx sent: %s (amountAllocated: %s)", txHash.Hex(), availToAlloc.String())
	} else {
		debtToSettle, err := r.fetchStrategyDebt(ctx, client, cfg.VaultAddress, cfg.RebalanceSource)
		if err != nil {
			log.Printf("fetch strategyDebt failed, fallback to config principalToSettle: %v", err)
			debtToSettle = cfg.RebalancePrincipalToSettle
		}
		txHash, err = r.sendManagedStrategyTx(
			ctx, client, cfg, privateKey, from, strategyParams, contextHash, debtToSettle, gasLimit, tipCap, feeCap,
		)
		if err != nil {
			return fmt.Errorf("send rebalance tx failed: %w", err)
		}
		log.Printf("rebalance tx sent: %s (principalToSettle: %s)", txHash.Hex(), debtToSettle.String())
	}

	if action == 0 {
		r.state.BasePrice = price
		r.state.LastRebalanceUnix = nowUnix
		resetCandlesToCurrentBucket(&r.state.AtrCandles, nowUnix, int64(cfg.RebalanceATRPeriodSec), price)
	}
	return nil
}

func (r *RebalanceRunner) sendManagedStrategyTx(
	ctx context.Context,
	client *ethclient.Client,
	cfg Config,
	privateKey *ecdsa.PrivateKey,
	from common.Address,
	strategyParams []byte,
	contextHash common.Hash,
	principalToSettle *big.Int,
	gasLimit uint64,
	tipCap *big.Int,
	feeCap *big.Int,
) (common.Hash, error) {
	data, err := r.routerABI.Pack(
		"executeManagedStrategy",
		cfg.RebalanceSource,
		strategyParams,
		contextHash,
		principalToSettle,
		uint16(cfg.RebalanceMaxLossBps),
		cfg.RebalanceMinReturnedAssets,
	)
	if err != nil {
		return common.Hash{}, err
	}

	estimated, err := client.EstimateGas(ctx, ethereum.CallMsg{
		From:      from,
		To:        &cfg.StrategyRouter,
		GasFeeCap: feeCap,
		GasTipCap: tipCap,
		Data:      data,
	})
	if err == nil && estimated > 0 {
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
		To:        &cfg.StrategyRouter,
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

func (r *RebalanceRunner) sendAllocateAndExecuteManagedStrategyTx(
	ctx context.Context,
	client *ethclient.Client,
	cfg Config,
	privateKey *ecdsa.PrivateKey,
	from common.Address,
	strategyParams []byte,
	contextHash common.Hash,
	amountToAllocate *big.Int,
	gasLimit uint64,
	tipCap *big.Int,
	feeCap *big.Int,
) (common.Hash, error) {
	data, err := r.routerABI.Pack(
		"allocateAndExecuteManagedStrategy",
		cfg.RebalanceSource,
		strategyParams,
		contextHash,
		amountToAllocate,
		big.NewInt(0), // principalToSettle is 0 for bootstrap
		uint16(cfg.RebalanceMaxLossBps),
		big.NewInt(0), // minReturnedAssets is 0 for bootstrap
	)
	if err != nil {
		return common.Hash{}, err
	}

	estimated, err := client.EstimateGas(ctx, ethereum.CallMsg{
		From:      from,
		To:        &cfg.StrategyRouter,
		GasFeeCap: feeCap,
		GasTipCap: tipCap,
		Data:      data,
	})
	if err == nil && estimated > 0 {
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
		To:        &cfg.StrategyRouter,
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

func (r *RebalanceRunner) fetchActiveTokenID(ctx context.Context, client *ethclient.Client, strategy common.Address) (*big.Int, error) {
	raw, err := callMethod(ctx, client, r.strategyABI, strategy, "activeTokenId")
	if err != nil {
		return nil, err
	}
	if len(raw) != 1 {
		return nil, errors.New("activeTokenId unexpected return length")
	}
	id, err := asBigInt(raw[0])
	if err != nil {
		return nil, err
	}
	return id, nil
}

func (r *RebalanceRunner) fetchAvailableForStrategy(ctx context.Context, client *ethclient.Client, vault, strategy common.Address) (*big.Int, error) {
	raw, err := callMethod(ctx, client, r.vaultABI, vault, "availableForStrategy", strategy)
	if err != nil {
		return nil, err
	}
	if len(raw) != 1 {
		return nil, errors.New("availableForStrategy unexpected return length")
	}
	return asBigInt(raw[0])
}

func (r *RebalanceRunner) fetchStrategyDebt(ctx context.Context, client *ethclient.Client, vault, strategy common.Address) (*big.Int, error) {
	raw, err := callMethod(ctx, client, r.vaultABI, vault, "strategyDebt", strategy)
	if err != nil {
		return nil, err
	}
	if len(raw) != 1 {
		return nil, errors.New("strategyDebt unexpected return length")
	}
	return asBigInt(raw[0])
}

func (r *RebalanceRunner) fetchStrategyAsset(ctx context.Context, client *ethclient.Client, strategy common.Address) (common.Address, error) {
	raw, err := callMethod(ctx, client, r.strategyABI, strategy, "asset")
	if err != nil {
		return common.Address{}, err
	}
	if len(raw) != 1 {
		return common.Address{}, errors.New("asset unexpected return length")
	}
	asset, ok := raw[0].(common.Address)
	if !ok {
		return common.Address{}, fmt.Errorf("asset type assertion failed: %T", raw[0])
	}
	return asset, nil
}

func (r *RebalanceRunner) fetchPositionInfo(
	ctx context.Context,
	client *ethclient.Client,
	positionManager common.Address,
	tokenID *big.Int,
) (LPPositionInfo, error) {
	raw, err := callMethod(ctx, client, r.positionMgrABI, positionManager, "positions", tokenID)
	if err != nil {
		return LPPositionInfo{}, err
	}
	if len(raw) != 12 {
		return LPPositionInfo{}, fmt.Errorf("positions unexpected field count: %d", len(raw))
	}

	token0, ok := raw[2].(common.Address)
	if !ok {
		return LPPositionInfo{}, fmt.Errorf("positions.token0 type assertion failed: %T", raw[2])
	}
	token1, ok := raw[3].(common.Address)
	if !ok {
		return LPPositionInfo{}, fmt.Errorf("positions.token1 type assertion failed: %T", raw[3])
	}
	fee, err := asUint32(raw[4])
	if err != nil {
		return LPPositionInfo{}, fmt.Errorf("positions.fee type assertion failed: %w", err)
	}
	tickLower, err := asInt32(raw[5])
	if err != nil {
		return LPPositionInfo{}, fmt.Errorf("positions.tickLower type assertion failed: %w", err)
	}
	tickUpper, err := asInt32(raw[6])
	if err != nil {
		return LPPositionInfo{}, fmt.Errorf("positions.tickUpper type assertion failed: %w", err)
	}
	liquidity, err := asBigInt(raw[7])
	if err != nil {
		return LPPositionInfo{}, fmt.Errorf("positions.liquidity type assertion failed: %w", err)
	}
	tokensOwed0, err := asBigInt(raw[10])
	if err != nil {
		return LPPositionInfo{}, fmt.Errorf("positions.tokensOwed0 type assertion failed: %w", err)
	}
	tokensOwed1, err := asBigInt(raw[11])
	if err != nil {
		return LPPositionInfo{}, fmt.Errorf("positions.tokensOwed1 type assertion failed: %w", err)
	}

	return LPPositionInfo{
		Token0:      token0,
		Token1:      token1,
		Fee:         fee,
		TickLower:   tickLower,
		TickUpper:   tickUpper,
		Liquidity:   liquidity,
		TokensOwed0: tokensOwed0,
		TokensOwed1: tokensOwed1,
	}, nil
}

func encodeRebalanceRouterParams(
	expectedContextHash common.Hash,
	action uint8,
	newTickLower int32,
	newTickUpper int32,
	swapFee uint32,
	minSwapOut *big.Int,
	amount0Min *big.Int,
	amount1Min *big.Int,
	deadlineUnix int64,
	burnPosition bool,
) ([]byte, error) {
	args, err := rebalanceStrategyArguments()
	if err != nil {
		return nil, err
	}

	return args.Pack(
		expectedContextHash,
		action,
		big.NewInt(int64(newTickLower)),
		big.NewInt(int64(newTickUpper)),
		new(big.Int).SetUint64(uint64(swapFee)),
		big.NewInt(0),
		safeBigInt(minSwapOut),
		safeBigInt(amount0Min),
		safeBigInt(amount1Min),
		big.NewInt(deadlineUnix),
		burnPosition,
	)
}

func rebalanceStrategyArguments() (abi.Arguments, error) {
	bytes32T, err := abi.NewType("bytes32", "", nil)
	if err != nil {
		return nil, err
	}
	uint8T, err := abi.NewType("uint8", "", nil)
	if err != nil {
		return nil, err
	}
	int24T, err := abi.NewType("int24", "", nil)
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
	boolT, err := abi.NewType("bool", "", nil)
	if err != nil {
		return nil, err
	}

	return abi.Arguments{
		{Type: bytes32T}, // expectedContextHash
		{Type: uint8T},   // action
		{Type: int24T},   // newTickLower
		{Type: int24T},   // newTickUpper
		{Type: uint24T},  // swapFee
		{Type: uint256T}, // rebalanceSwapAmountIn
		{Type: uint256T}, // minSwapOut
		{Type: uint256T}, // amount0Min
		{Type: uint256T}, // amount1Min
		{Type: uint256T}, // deadline
		{Type: boolT},    // burnPosition
	}, nil
}

func fetchTickSpacing(ctx context.Context, client *ethclient.Client, poolABI abi.ABI, pool common.Address) (int32, error) {
	raw, err := callMethod(ctx, client, poolABI, pool, "tickSpacing")
	if err != nil {
		return 0, err
	}
	if len(raw) != 1 {
		return 0, errors.New("tickSpacing unexpected return length")
	}
	spacing, err := asInt32(raw[0])
	if err != nil {
		return 0, err
	}
	if spacing <= 0 {
		return 0, fmt.Errorf("tickSpacing non-positive: %d", spacing)
	}
	return spacing, nil
}

func loadRebalanceState(path string) (*RebalanceState, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RebalanceState{Candles: []PriceCandle{}}, nil
		}
		return nil, fmt.Errorf("read rebalance state file failed: %w", err)
	}

	var state RebalanceState
	if err := json.Unmarshal(content, &state); err != nil {
		return nil, fmt.Errorf("parse rebalance state file failed: %w", err)
	}
	if state.Candles == nil {
		state.Candles = []PriceCandle{}
	}
	return &state, nil
}

func saveRebalanceState(path string, state *RebalanceState) error {
	if state == nil {
		return nil
	}

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func updateCandleSeries(candles *[]PriceCandle, nowUnix int64, price float64, periodSec int64, maxCandles int) {
	if periodSec <= 0 {
		periodSec = 1
	}
	bucket := nowUnix - (nowUnix % periodSec)
	if candles == nil {
		return
	}
	if len(*candles) == 0 || (*candles)[len(*candles)-1].StartUnix != bucket {
		*candles = append(*candles, PriceCandle{
			StartUnix: bucket,
			Open:      price,
			High:      price,
			Low:       price,
			Close:     price,
		})
	} else {
		last := &(*candles)[len(*candles)-1]
		if price > last.High {
			last.High = price
		}
		if price < last.Low {
			last.Low = price
		}
		last.Close = price
	}

	if maxCandles > 0 && len(*candles) > maxCandles {
		*candles = (*candles)[len(*candles)-maxCandles:]
	}
}

func resetCandlesToCurrentBucket(candles *[]PriceCandle, nowUnix int64, periodSec int64, price float64) {
	if periodSec <= 0 {
		periodSec = 1
	}
	bucket := nowUnix - (nowUnix % periodSec)
	if candles == nil {
		return
	}
	*candles = []PriceCandle{{
		StartUnix: bucket,
		Open:      price,
		High:      price,
		Low:       price,
		Close:     price,
	}}
}

func computeATR(candles []PriceCandle, length int) (float64, bool) {
	if length <= 0 || len(candles) < length {
		return 0, false
	}

	trs := make([]float64, len(candles))
	for i := 0; i < len(candles); i++ {
		c := candles[i]
		trHL := c.High - c.Low
		tr := trHL
		if i > 0 {
			prevClose := candles[i-1].Close
			trHC := math.Abs(c.High - prevClose)
			trLC := math.Abs(c.Low - prevClose)
			tr = maxFloat(trHL, trHC, trLC)
		}
		if tr < 0 {
			tr = 0
		}
		trs[i] = tr
	}

	start := len(trs) - length
	sum := 0.0
	for _, tr := range trs[start:] {
		sum += tr
	}
	return sum / float64(length), true
}

func computeADX(candles []PriceCandle, length int) (float64, bool) {
	if length <= 0 || len(candles) < (2*length) {
		return 0, false
	}

	tr := make([]float64, 0, len(candles)-1)
	plusDM := make([]float64, 0, len(candles)-1)
	minusDM := make([]float64, 0, len(candles)-1)
	for i := 1; i < len(candles); i++ {
		curr := candles[i]
		prev := candles[i-1]
		upMove := curr.High - prev.High
		downMove := prev.Low - curr.Low
		pdm := 0.0
		mdm := 0.0
		if upMove > downMove && upMove > 0 {
			pdm = upMove
		}
		if downMove > upMove && downMove > 0 {
			mdm = downMove
		}
		trueRange := maxFloat(curr.High-curr.Low, math.Abs(curr.High-prev.Close), math.Abs(curr.Low-prev.Close))
		tr = append(tr, trueRange)
		plusDM = append(plusDM, pdm)
		minusDM = append(minusDM, mdm)
	}

	sumTR := sumSlice(tr[:length])
	sumPDM := sumSlice(plusDM[:length])
	sumMDM := sumSlice(minusDM[:length])

	adx := 0.0
	dxCount := 0
	dxSum := 0.0
	for i := length - 1; i < len(tr); i++ {
		if i > length-1 {
			sumTR = sumTR - (sumTR / float64(length)) + tr[i]
			sumPDM = sumPDM - (sumPDM / float64(length)) + plusDM[i]
			sumMDM = sumMDM - (sumMDM / float64(length)) + minusDM[i]
		}
		if sumTR <= 0 {
			continue
		}
		plusDI := 100.0 * sumPDM / sumTR
		minusDI := 100.0 * sumMDM / sumTR
		diSum := plusDI + minusDI
		dx := 0.0
		if diSum > 0 {
			dx = 100.0 * math.Abs(plusDI-minusDI) / diSum
		}
		if dxCount < length {
			dxSum += dx
			dxCount++
			if dxCount == length {
				adx = dxSum / float64(length)
			}
		} else {
			adx = ((adx * float64(length-1)) + dx) / float64(length)
		}
	}

	if dxCount < length {
		return 0, false
	}
	return adx, true
}

func estimateExpectedFees(dailyVolumeAsset float64, fee uint32, liquidityShare float64, expectedDays float64) float64 {
	if dailyVolumeAsset <= 0 || liquidityShare <= 0 || expectedDays <= 0 {
		return 0
	}
	feeRate := float64(fee) / float64(feeDenominator)
	return dailyVolumeAsset * feeRate * liquidityShare * expectedDays
}

func estimateImpermanentLoss(basePrice float64, currentPrice float64) float64 {
	if basePrice <= 0 || currentPrice <= 0 {
		return 0
	}
	ratio := currentPrice / basePrice
	if ratio < 1 {
		ratio = 1 / ratio
	}
	loss := 1 - (2*math.Sqrt(ratio))/(1+ratio)
	if loss < 0 {
		return 0
	}
	return loss
}

func sumSlice(values []float64) float64 {
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum
}

func priceFromSqrtPriceX96(sqrtPriceX96 *big.Int) (float64, error) {
	if sqrtPriceX96 == nil || sqrtPriceX96.Sign() <= 0 {
		return 0, errors.New("sqrtPriceX96 invalid")
	}

	sqrtPrice := new(big.Float).Quo(new(big.Float).SetInt(sqrtPriceX96), new(big.Float).SetInt(q96))
	sqrtPriceF, _ := sqrtPrice.Float64()
	if sqrtPriceF <= 0 {
		return 0, errors.New("sqrtPriceX96 converted to non-positive")
	}
	return sqrtPriceF * sqrtPriceF, nil
}

func estimatePositionAmountsFromLiquidity(liquidity float64, tickLower int32, tickUpper int32, price float64) (float64, float64) {
	if liquidity <= 0 || price <= 0 || tickLower >= tickUpper {
		return 0, 0
	}

	sqrtP := math.Sqrt(price)
	sqrtA := math.Pow(1.0001, float64(tickLower)/2.0)
	sqrtB := math.Pow(1.0001, float64(tickUpper)/2.0)
	if sqrtA > sqrtB {
		sqrtA, sqrtB = sqrtB, sqrtA
	}

	var amount0 float64
	var amount1 float64

	switch {
	case sqrtP <= sqrtA:
		amount0 = liquidity * (sqrtB - sqrtA) / (sqrtA * sqrtB)
		amount1 = 0
	case sqrtP >= sqrtB:
		amount0 = 0
		amount1 = liquidity * (sqrtB - sqrtA)
	default:
		amount0 = liquidity * (sqrtB - sqrtP) / (sqrtP * sqrtB)
		amount1 = liquidity * (sqrtP - sqrtA)
	}

	if amount0 < 0 {
		amount0 = 0
	}
	if amount1 < 0 {
		amount1 = 0
	}
	return amount0, amount1
}

func valueInAsset(
	amount0 float64,
	amount1 float64,
	asset common.Address,
	token0 common.Address,
	token1 common.Address,
	priceToken1PerToken0 float64,
) (float64, float64, float64, error) {
	if priceToken1PerToken0 <= 0 {
		return 0, 0, 0, errors.New("priceToken1PerToken0 must be > 0")
	}

	if asset == token0 {
		v0 := amount0
		v1 := amount1 / priceToken1PerToken0
		return v0 + v1, v0, v1, nil
	}
	if asset == token1 {
		v0 := amount0 * priceToken1PerToken0
		v1 := amount1
		return v0 + v1, v0, v1, nil
	}

	return 0, 0, 0, errors.New("asset is neither token0 nor token1")
}

func estimateImbalanceShare(componentA float64, componentB float64) float64 {
	total := componentA + componentB
	if total <= 0 {
		return 0
	}
	diff := math.Abs(componentA - componentB)
	share := diff / (2 * total)
	if share < 0 {
		return 0
	}
	if share > 1 {
		return 1
	}
	return share
}

func buildTargetTicksFromRange(lowerPrice float64, upperPrice float64, spacing int32, currentTick int32) (int32, int32) {
	if spacing <= 0 {
		spacing = 1
	}

	fallbackCenter := floorToSpacing(currentTick, spacing)
	fallbackLower := clampTick(fallbackCenter - spacing*10)
	fallbackUpper := clampTick(fallbackCenter + spacing*10)
	if fallbackUpper <= fallbackLower {
		fallbackUpper = clampTick(fallbackLower + spacing)
	}

	if lowerPrice <= 0 || upperPrice <= lowerPrice {
		return fallbackLower, fallbackUpper
	}

	lowerTick := int32(math.Floor(math.Log(lowerPrice) / math.Log(1.0001)))
	upperTick := int32(math.Ceil(math.Log(upperPrice) / math.Log(1.0001)))

	lowerTick = floorToSpacing(clampTick(lowerTick), spacing)
	upperTick = ceilToSpacing(clampTick(upperTick), spacing)

	lowerTick = clampTick(lowerTick)
	upperTick = clampTick(upperTick)

	if upperTick <= lowerTick {
		return fallbackLower, fallbackUpper
	}
	return lowerTick, upperTick
}

func floorToSpacing(tick int32, spacing int32) int32 {
	if spacing <= 0 {
		return tick
	}
	compressed := tick / spacing
	if tick < 0 && tick%spacing != 0 {
		compressed--
	}
	return compressed * spacing
}

func ceilToSpacing(tick int32, spacing int32) int32 {
	floored := floorToSpacing(tick, spacing)
	if floored == tick {
		return tick
	}
	return floored + spacing
}

func clampTick(tick int32) int32 {
	if tick < lpMinTick {
		return lpMinTick
	}
	if tick > lpMaxTick {
		return lpMaxTick
	}
	return tick
}

func buildRebalanceContextHash(
	nowUnix int64,
	pool common.Address,
	tokenID *big.Int,
	lowerTick int32,
	upperTick int32,
	basePrice float64,
	currentPrice float64,
	atr float64,
) common.Hash {
	payload := []byte(fmt.Sprintf(
		"rebalance|%s|%s|%d|%d|%d|%.12f|%.12f|%.12f",
		pool.Hex(),
		tokenID.String(),
		nowUnix,
		lowerTick,
		upperTick,
		basePrice,
		currentPrice,
		atr,
	))
	return crypto.Keccak256Hash(payload)
}

func asBigInt(v interface{}) (*big.Int, error) {
	switch t := v.(type) {
	case *big.Int:
		return new(big.Int).Set(t), nil
	case uint8:
		return big.NewInt(int64(t)), nil
	case uint16:
		return big.NewInt(int64(t)), nil
	case uint32:
		return big.NewInt(int64(t)), nil
	case uint64:
		return new(big.Int).SetUint64(t), nil
	case int8:
		return big.NewInt(int64(t)), nil
	case int16:
		return big.NewInt(int64(t)), nil
	case int32:
		return big.NewInt(int64(t)), nil
	case int64:
		return big.NewInt(t), nil
	default:
		return nil, fmt.Errorf("unknown big.Int type: %T", v)
	}
}

func safeBigInt(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(v)
}

func bigIntToFloat(v *big.Int) float64 {
	if v == nil {
		return 0
	}
	f, _ := new(big.Float).SetInt(v).Float64()
	return f
}

func maxFloat(values ...float64) float64 {
	if len(values) == 0 {
		return 0
	}
	maxV := values[0]
	for i := 1; i < len(values); i++ {
		if values[i] > maxV {
			maxV = values[i]
		}
	}
	return maxV
}
