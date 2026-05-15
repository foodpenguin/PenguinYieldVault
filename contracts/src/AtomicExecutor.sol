// SPDX-License-Identifier: MIT
pragma solidity ^0.8.13;

import {OnchainStateLens} from "./OnchainStateLens.sol";
import {StrategyRouter} from "./StrategyRouter.sol";

/// @title Atomic executor
/// @notice On-chain pre-check snapshot then execute strategy in a single tx
contract AtomicExecutor {
    uint16 internal constant BPS_DENOMINATOR = 10_000;

    address public owner;
    StrategyRouter public immutable router;
    OnchainStateLens public immutable lens;
    bool public paused;

    mapping(address => bool) public keepers;

    struct ExecuteWithSnapshotParams {
        address source;
        bytes strategyParams;
        address pool;
        address tokenIn;
        address tokenOut;
        address secondaryPool;
        address secondaryTokenIn;
        address secondaryTokenOut;
        uint256 amountIn;
        uint256 minAmountOut;
        uint256 minReturnedAssets;
        uint256 minProfit;
        uint16 maxLossBps;
        uint256 deadline;
    }

    event OwnerTransferred(address indexed oldOwner, address indexed newOwner);
    event KeeperUpdated(address indexed keeper, bool allowed);
    event PausedUpdated(bool paused);
    event AtomicExecuted(
        address indexed caller,
        address indexed source,
        address indexed buyPool,
        address sellPool,
        bytes32 executionContextHash,
        uint256 buyQuoteAmountOut,
        uint256 sellQuoteAmountOut,
        uint256 minAmountOut,
        uint256 minReturnedAssets,
        uint256 profit,
        uint256 minProfit,
        int256 realizedPnl
    );

    modifier onlyOwner() {
        require(msg.sender == owner, "NOT_OWNER");
        _;
    }

    modifier onlyKeeper() {
        require(keepers[msg.sender], "NOT_KEEPER");
        _;
    }

    constructor(address router_, address lens_) {
        require(router_ != address(0) && lens_ != address(0), "ZERO_ADDRESS");

        owner = msg.sender;
        router = StrategyRouter(router_);
        lens = OnchainStateLens(lens_);
    }

    function transferOwnership(address newOwner) external onlyOwner {
        require(newOwner != address(0), "ZERO_ADDRESS");
        emit OwnerTransferred(owner, newOwner);
        owner = newOwner;
    }

    function setKeeper(address keeper, bool allowed) external onlyOwner {
        require(keeper != address(0), "ZERO_KEEPER");
        keepers[keeper] = allowed;
        emit KeeperUpdated(keeper, allowed);
    }

    /// @notice Emergency pause toggle
    function setPaused(bool paused_) external onlyOwner {
        paused = paused_;
        emit PausedUpdated(paused_);
    }

    /// @notice Pre-check pool snapshots then execute strategy atomically
    /// @dev Reverts if quote insufficient or realized profit below minimum
    function executeWithSnapshot(ExecuteWithSnapshotParams calldata params)
        external
        onlyKeeper
        returns (
            int256 realizedPnl,
            uint256 profit,
            OnchainStateLens.V3PoolSnapshot memory buySnapshot,
            OnchainStateLens.V3PoolSnapshot memory sellSnapshot
        )
    {
        _validateParams(params);

        buySnapshot = lens.getV3PoolSnapshot(
            params.pool,
            params.tokenIn,
            params.tokenOut,
            params.amountIn
        );

        require(buySnapshot.quoteAmountOut >= params.minAmountOut, "PRECHECK_SLIPPAGE");

        sellSnapshot = lens.getV3PoolSnapshot(
            params.secondaryPool,
            params.secondaryTokenIn,
            params.secondaryTokenOut,
            buySnapshot.quoteAmountOut
        );
        require(sellSnapshot.quoteAmountOut > 0, "PRECHECK_SELL_QUOTE_ZERO");

        uint256 minReturnedAssets = _normalizedMinReturnedAssets(params.minReturnedAssets, params.amountIn);

        bytes32 executionContextHash = computeExecutionContextHash(params, buySnapshot, sellSnapshot);

        (realizedPnl, profit,,) =
            router.executeStrategy(
                params.source,
                params.strategyParams,
                executionContextHash,
                params.amountIn,
                params.maxLossBps,
                minReturnedAssets
            );

        require(profit >= params.minProfit, "MIN_PROFIT_NOT_MET");

        emit AtomicExecuted(
            msg.sender,
            params.source,
            params.pool,
            params.secondaryPool,
            executionContextHash,
            buySnapshot.quoteAmountOut,
            sellSnapshot.quoteAmountOut,
            params.minAmountOut,
            minReturnedAssets,
            profit,
            params.minProfit,
            realizedPnl
        );
    }

    function _validateParams(ExecuteWithSnapshotParams calldata params) internal view {
        require(!paused, "PAUSED");
        require(block.timestamp <= params.deadline, "EXPIRED");
        require(params.source != address(0), "ZERO_SOURCE");
        require(params.pool != address(0), "ZERO_BUY_POOL");
        require(params.secondaryPool != address(0), "ZERO_SECONDARY_POOL");
        require(params.tokenIn != address(0) && params.tokenOut != address(0), "ZERO_PRIMARY_TOKEN");
        require(params.secondaryTokenIn != address(0) && params.secondaryTokenOut != address(0), "ZERO_SECONDARY_TOKEN");
        require(params.tokenIn != params.tokenOut, "PRIMARY_SAME_TOKEN");
        require(params.secondaryTokenIn != params.secondaryTokenOut, "SECONDARY_SAME_TOKEN");
        require(params.amountIn > 0, "ZERO_AMOUNT_IN");
        require(params.secondaryTokenIn == params.tokenOut, "SECONDARY_TOKEN_IN_MISMATCH");
        require(params.secondaryTokenOut == params.tokenIn, "SECONDARY_TOKEN_OUT_MISMATCH");
        require(params.maxLossBps <= BPS_DENOMINATOR, "INVALID_MAX_LOSS_BPS");
    }

    /// @notice Deterministic context hash binding pre-check to execution
    function computeExecutionContextHash(
        ExecuteWithSnapshotParams calldata params,
        OnchainStateLens.V3PoolSnapshot memory buySnapshot,
        OnchainStateLens.V3PoolSnapshot memory sellSnapshot
    ) public pure returns (bytes32) {
        uint256 minReturnedAssets = _normalizedMinReturnedAssets(params.minReturnedAssets, params.amountIn);

        bytes32 primaryHash = _computePrimaryLegHash(params, buySnapshot, minReturnedAssets);

        bytes32 secondaryHash = _computeSecondaryLegHash(params, sellSnapshot);

        return keccak256(abi.encode(primaryHash, secondaryHash));
    }

    function _computePrimaryLegHash(
        ExecuteWithSnapshotParams calldata params,
        OnchainStateLens.V3PoolSnapshot memory buySnapshot,
        uint256 minReturnedAssets
    ) internal pure returns (bytes32) {
        return keccak256(
            abi.encode(
                params.source,
                params.pool,
                params.tokenIn,
                params.tokenOut,
                params.amountIn,
                params.minAmountOut,
                minReturnedAssets,
                params.minProfit,
                params.maxLossBps,
                params.deadline,
                buySnapshot.fee,
                buySnapshot.liquidity,
                buySnapshot.sqrtPriceX96,
                buySnapshot.tick
            )
        );
    }

    function _computeSecondaryLegHash(
        ExecuteWithSnapshotParams calldata params,
        OnchainStateLens.V3PoolSnapshot memory sellSnapshot
    ) internal pure returns (bytes32) {
        return keccak256(
            abi.encode(
                params.secondaryPool,
                params.secondaryTokenIn,
                params.secondaryTokenOut,
                sellSnapshot.fee,
                sellSnapshot.liquidity,
                sellSnapshot.sqrtPriceX96,
                sellSnapshot.tick
            )
        );
    }

    function _normalizedMinReturnedAssets(uint256 minReturnedAssets, uint256 amountIn)
        internal
        pure
        returns (uint256)
    {
        return minReturnedAssets == 0 ? amountIn : minReturnedAssets;
    }
}
