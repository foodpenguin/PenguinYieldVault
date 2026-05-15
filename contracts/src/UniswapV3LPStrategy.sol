// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Ownable} from "openzeppelin-contracts/contracts/access/Ownable.sol";
import {IERC20} from "openzeppelin-contracts/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "openzeppelin-contracts/contracts/token/ERC20/utils/SafeERC20.sol";
import {IERC721Receiver} from "openzeppelin-contracts/contracts/token/ERC721/IERC721Receiver.sol";
import {ReentrancyGuard} from "openzeppelin-contracts/contracts/utils/ReentrancyGuard.sol";
import {Math} from "openzeppelin-contracts/contracts/utils/math/Math.sol";

import {IProfitSource} from "./interfaces/IProfitSource.sol";
import {IUniswapV3Factory} from "./interfaces/uniswap/IUniswapV3Factory.sol";
import {IUniswapV3Pool} from "./interfaces/uniswap/IUniswapV3Pool.sol";
import {IV3SwapRouter} from "./interfaces/uniswap/IV3SwapRouter.sol";
import {INonfungiblePositionManager} from "./interfaces/uniswap/INonfungiblePositionManager.sol";

/// @title Uniswap V3 single-pool LP strategy
/// @notice Provides initial mint, rebalance, and full withdrawal operations
contract UniswapV3LPStrategy is IProfitSource, Ownable, ReentrancyGuard, IERC721Receiver {
    using SafeERC20 for IERC20;

    int24 internal constant MIN_TICK = -887_272;
    int24 internal constant MAX_TICK = 887_272;
    uint256 internal constant Q96 = 2 ** 96;

    uint8 internal constant ACTION_REBALANCE_ONLY = 0;
    uint8 internal constant ACTION_WITHDRAW_ONLY = 1;
    uint8 internal constant ACTION_REBALANCE_THEN_WITHDRAW = 2;
    uint8 internal constant ACTION_BOOTSTRAP = 3;

    struct InitialLiquidityParams {
        uint256 amountAssetIn;
        uint24 swapFee;
        uint256 minSwapOut;
        int24 tickLower;
        int24 tickUpper;
        uint256 amount0Min;
        uint256 amount1Min;
        uint256 deadline;
    }

    struct RebalanceParams {
        int24 newTickLower;
        int24 newTickUpper;
        uint24 swapFee;
        bool swapToken0ToToken1;
        uint256 rebalanceSwapAmountIn;
        uint256 minSwapOut;
        uint256 amount0Min;
        uint256 amount1Min;
        uint256 deadline;
    }

    struct RouterExecuteParams {
        bytes32 expectedContextHash;
        uint8 action;
        int24 newTickLower;
        int24 newTickUpper;
        uint24 swapFee;
        uint256 rebalanceSwapAmountIn;
        uint256 minSwapOut;
        uint256 amount0Min;
        uint256 amount1Min;
        uint256 deadline;
        bool burnPosition;
    }

    INonfungiblePositionManager public immutable positionManager;
    IV3SwapRouter public immutable swapRouter;
    IUniswapV3Pool public immutable pool;
    IERC20 public immutable token0;
    IERC20 public immutable token1;
    IERC20 public immutable assetToken;
    uint24 public immutable poolFee;

    address public router;
    uint256 public activeTokenId;
    uint256 public accountedPrincipal;
    uint256 public totalManagedReturnedAssets;
    int256 public cumulativeRealizedPnl;
    int256 public latestFloatingPnl;
    bool private _active;
    string private _id;

    event PositionOpened(
        uint256 indexed tokenId,
        uint128 liquidity,
        uint256 amount0,
        uint256 amount1,
        int24 tickLower,
        int24 tickUpper
    );
    event PositionRebalanced(
        uint256 indexed oldTokenId,
        uint256 indexed newTokenId,
        uint128 oldLiquidity,
        uint128 newLiquidity,
        int24 newTickLower,
        int24 newTickUpper
    );
    event PositionWithdrawn(uint256 indexed tokenId, address indexed recipient, uint256 assetOut, bool burned);
    event AssetSplitSwap(address indexed tokenIn, address indexed tokenOut, uint256 amountIn, uint256 amountOut, uint24 fee);
    event AssetConvertedToAsset(address indexed tokenIn, uint256 amountIn, uint256 amountOut, uint24 fee);
    event AutoSwapPlanned(bool swapToken0ToToken1, uint256 amountIn, uint16 amountInBps, uint256 balance0, uint256 balance1);
    event AccountedPrincipalUpdated(uint256 oldPrincipal, uint256 newPrincipal);
    event ManagedExecutionReported(
        uint8 indexed action,
        uint256 navBefore,
        uint256 navAfter,
        uint256 returnedAssets,
        int256 realizedPnl,
        int256 floatingPnl,
        uint256 principalAfter
    );
    event ActiveUpdated(bool active);
    event RouterUpdated(address indexed oldRouter, address indexed newRouter);

    error ZeroAddress();
    error InvalidAsset();
    error ActivePositionExists();
    error NoActivePosition();
    error InvalidTickRange();
    error InvalidTickSpacing();
    error ZeroAmount();
    error InvalidPoolFactory();
    error PositionPoolMismatch();
    error InsufficientBalance();
    error SourceInactive();
    error InvalidAction();
    error ContextMismatch();
    error AmountInMustBeZero();
    error NotRouterCaller();
    error Expired();
    error InvalidSqrtPrice();
    error IntOverflow();

    constructor(
        address pool_,
        address positionManager_,
        address swapRouter_,
        address assetToken_
    ) Ownable(msg.sender) {
        if (pool_ == address(0) || positionManager_ == address(0) || swapRouter_ == address(0) || assetToken_ == address(0)) {
            revert ZeroAddress();
        }

        pool = IUniswapV3Pool(pool_);
        positionManager = INonfungiblePositionManager(positionManager_);
        swapRouter = IV3SwapRouter(swapRouter_);

        address token0_ = pool.token0();
        address token1_ = pool.token1();
        uint24 fee_ = pool.fee();

        token0 = IERC20(token0_);
        token1 = IERC20(token1_);
        poolFee = fee_;

        if (assetToken_ != token0_ && assetToken_ != token1_) {
            revert InvalidAsset();
        }
        assetToken = IERC20(assetToken_);

        address factory = positionManager.factory();
        address expectedPool = IUniswapV3Factory(factory).getPool(token0_, token1_, fee_);
        if (expectedPool != pool_) {
            revert InvalidPoolFactory();
        }

        _active = true;
        _id = "uniswap-v3-lp";
    }

    function sourceId() external view returns (string memory) {
        return _id;
    }

    function asset() external view returns (address) {
        return address(assetToken);
    }

    function isActive() external view returns (bool) {
        return _active;
    }

    function setSourceId(string calldata newId) external onlyOwner {
        _id = newId;
    }

    function setActive(bool active_) external onlyOwner {
        _active = active_;
        emit ActiveUpdated(active_);
    }

    function setRouter(address newRouter) external onlyOwner {
        if (newRouter == address(0)) revert ZeroAddress();
        emit RouterUpdated(router, newRouter);
        router = newRouter;
    }

    function estimatedNavInAsset() external view returns (uint256) {
        return _estimatedNavInAsset();
    }

    /// @notice Sync accounted principal to current NAV after external fund moves
    function syncAccountedPrincipalToNav() external onlyOwner {
        uint256 oldPrincipal = accountedPrincipal;
        uint256 navNow = _estimatedNavInAsset();
        accountedPrincipal = navNow;
        latestFloatingPnl = 0;
        emit AccountedPrincipalUpdated(oldPrincipal, navNow);
    }

    function setAccountedPrincipal(uint256 newPrincipal) external onlyOwner {
        uint256 oldPrincipal = accountedPrincipal;
        accountedPrincipal = newPrincipal;
        latestFloatingPnl = _toInt256(_estimatedNavInAsset()) - _toInt256(accountedPrincipal);
        emit AccountedPrincipalUpdated(oldPrincipal, newPrincipal);
    }

    /// @notice Router-triggered managed execution entry
    /// @dev action: 0=rebalance, 1=withdraw, 2=rebalance+withdraw, 3=bootstrap
    function execute(bytes calldata params, bytes32 executionContextHash, uint256 amountIn)
        external
        nonReentrant
        returns (int256 realizedPnl, uint256 returnedAssets)
    {
        if (!_active) revert SourceInactive();
        if (msg.sender != router) revert NotRouterCaller();

        RouterExecuteParams memory p = abi.decode(params, (RouterExecuteParams));
        if (p.expectedContextHash != executionContextHash) revert ContextMismatch();
        if (block.timestamp > p.deadline) revert Expired();
        if (p.action > ACTION_BOOTSTRAP) revert InvalidAction();

        if (p.action != ACTION_BOOTSTRAP && amountIn != 0) revert AmountInMustBeZero();
        if (p.action == ACTION_BOOTSTRAP && amountIn == 0) revert ZeroAmount();

        uint256 navBefore = _estimatedNavInAsset();

        if (p.action == ACTION_REBALANCE_ONLY || p.action == ACTION_REBALANCE_THEN_WITHDRAW) {
            RebalanceParams memory rp = RebalanceParams({
                newTickLower: p.newTickLower,
                newTickUpper: p.newTickUpper,
                swapFee: p.swapFee,
                swapToken0ToToken1: false,
                rebalanceSwapAmountIn: p.rebalanceSwapAmountIn,
                minSwapOut: p.minSwapOut,
                amount0Min: p.amount0Min,
                amount1Min: p.amount1Min,
                deadline: p.deadline
            });

            _rebalanceInternal(rp);
        }

        uint256 navAfter;
        if (p.action == ACTION_BOOTSTRAP) {
            if (activeTokenId != 0) revert ActivePositionExists();
            
            _validateTicks(p.newTickLower, p.newTickUpper);

            uint256 swapIn = amountIn / 2;
            if (swapIn > 0) {
                _splitAsset(swapIn, p.swapFee, p.minSwapOut);
            }

            uint256 bal0 = token0.balanceOf(address(this));
            uint256 bal1 = token1.balanceOf(address(this));

            (activeTokenId, , , ) =
                _mintFromCurrentBalances(p.newTickLower, p.newTickUpper, p.amount0Min, p.amount1Min, p.deadline);

            navAfter = _estimatedNavInAsset();
            accountedPrincipal = navAfter;

            emit PositionOpened(activeTokenId, 0, bal0, bal1, p.newTickLower, p.newTickUpper);
        } else {
            if (p.action == ACTION_WITHDRAW_ONLY || p.action == ACTION_REBALANCE_THEN_WITHDRAW) {
                returnedAssets = _withdrawAllToAssetInternal(
                    msg.sender,
                    p.amount0Min,
                    p.amount1Min,
                    _effectiveSwapFee(p.swapFee),
                    p.minSwapOut,
                    p.deadline,
                    p.burnPosition
                );
            }

            navAfter = _estimatedNavInAsset();
            int256 navDelta = _toInt256(navAfter) - _toInt256(navBefore);
            realizedPnl = navDelta + _toInt256(returnedAssets);
        }

        cumulativeRealizedPnl += realizedPnl;
        latestFloatingPnl = _toInt256(navAfter) - _toInt256(accountedPrincipal);

        emit ManagedExecutionReported(
            p.action,
            navBefore,
            navAfter,
            returnedAssets,
            realizedPnl,
            latestFloatingPnl,
            accountedPrincipal
        );
    }

    /// @notice Bootstrap LP position from single asset (auto-splits 50/50 then mints)
    function provideInitialLiquidity(InitialLiquidityParams calldata params)
        external
        onlyOwner
        nonReentrant
        returns (uint256 tokenId, uint128 liquidity, uint256 amount0, uint256 amount1)
    {
        if (activeTokenId != 0) revert ActivePositionExists();
        if (params.amountAssetIn == 0) revert ZeroAmount();

        _validateTicks(params.tickLower, params.tickUpper);

        assetToken.safeTransferFrom(msg.sender, address(this), params.amountAssetIn);

        uint256 swapIn = params.amountAssetIn / 2;
        if (swapIn > 0) {
            _splitAsset(swapIn, params.swapFee, params.minSwapOut);
        }

        (tokenId, liquidity, amount0, amount1) = _mintFromCurrentBalances(
            params.tickLower,
            params.tickUpper,
            params.amount0Min,
            params.amount1Min,
            params.deadline
        );

        activeTokenId = tokenId;

        uint256 oldPrincipal = accountedPrincipal;
        accountedPrincipal = _estimatedNavInAsset();
        latestFloatingPnl = 0;
        emit AccountedPrincipalUpdated(oldPrincipal, accountedPrincipal);

        emit PositionOpened(tokenId, liquidity, amount0, amount1, params.tickLower, params.tickUpper);
    }

    /// @notice Fully collect old position, re-mint at new tick range
    function rebalancePosition(RebalanceParams calldata params)
        external
        onlyOwner
        nonReentrant
        returns (uint256 newTokenId, uint128 newLiquidity, uint256 amount0, uint256 amount1)
    {
        (newTokenId, newLiquidity, amount0, amount1) = _rebalanceInternal(params);
    }

    /// @notice Withdraw all liquidity, convert to asset token, send to recipient
    function withdrawAllLiquidity(
        address recipient,
        uint256 amount0Min,
        uint256 amount1Min,
        uint256 deadline,
        bool burnPosition
    ) external onlyOwner nonReentrant returns (uint256 assetOut) {
        assetOut = _withdrawAllToAssetInternal(recipient, amount0Min, amount1Min, poolFee, 1, deadline, burnPosition);
    }

    function poolMeta() external view returns (address token0_, address token1_, uint24 fee_, int24 tickSpacing_, int24 currentTick) {
        token0_ = address(token0);
        token1_ = address(token1);
        fee_ = poolFee;
        tickSpacing_ = pool.tickSpacing();
        (, currentTick,,,,,) = pool.slot0();
    }

    function rescueToken(address token, address to, uint256 amount) external onlyOwner {
        if (token == address(0) || to == address(0)) revert ZeroAddress();
        IERC20(token).safeTransfer(to, amount);
    }

    function onERC721Received(address, address, uint256, bytes calldata) external pure override returns (bytes4) {
        return IERC721Receiver.onERC721Received.selector;
    }

    /// @notice Estimate optimal rebalance swap direction and amount from current balances
    function estimateRebalanceSwap()
        external
        view
        returns (bool swapToken0ToToken1, uint256 amountIn, uint16 amountInBps, uint256 balance0, uint256 balance1)
    {
        balance0 = token0.balanceOf(address(this));
        balance1 = token1.balanceOf(address(this));
        (swapToken0ToToken1, amountIn, amountInBps) = _estimateSwapForRebalance(balance0, balance1);
    }

    function _rebalanceInternal(RebalanceParams memory params)
        internal
        returns (uint256 newTokenId, uint128 newLiquidity, uint256 amount0, uint256 amount1)
    {
        uint256 oldTokenId = activeTokenId;
        if (oldTokenId == 0) revert NoActivePosition();

        _validateTicks(params.newTickLower, params.newTickUpper);

        uint128 oldLiquidity = _positionLiquidity(oldTokenId);
        _decreaseAndCollectAll(oldTokenId, params.amount0Min, params.amount1Min, params.deadline);
        positionManager.burn(oldTokenId);

        bool swapToken0ToToken1 = params.swapToken0ToToken1;
        uint256 swapAmountIn = params.rebalanceSwapAmountIn;
        uint16 amountInBps;

        if (swapAmountIn == 0) {
            uint256 balance0 = token0.balanceOf(address(this));
            uint256 balance1 = token1.balanceOf(address(this));
            (swapToken0ToToken1, swapAmountIn, amountInBps) = _estimateSwapForRebalance(balance0, balance1);
            emit AutoSwapPlanned(swapToken0ToToken1, swapAmountIn, amountInBps, balance0, balance1);
        }

        if (swapAmountIn > 0) {
            _rebalanceSwap(
                swapToken0ToToken1,
                swapAmountIn,
                _effectiveSwapFee(params.swapFee),
                params.minSwapOut
            );
        }

        (newTokenId, newLiquidity, amount0, amount1) = _mintFromCurrentBalances(
            params.newTickLower,
            params.newTickUpper,
            params.amount0Min,
            params.amount1Min,
            params.deadline
        );

        activeTokenId = newTokenId;
        emit PositionRebalanced(oldTokenId, newTokenId, oldLiquidity, newLiquidity, params.newTickLower, params.newTickUpper);
    }

    function _withdrawAllToAssetInternal(
        address recipient,
        uint256 amount0Min,
        uint256 amount1Min,
        uint24 swapFee,
        uint256 minSwapOut,
        uint256 deadline,
        bool burnPosition
    ) internal returns (uint256 assetOut) {
        if (recipient == address(0)) revert ZeroAddress();

        uint256 tokenId = activeTokenId;
        if (tokenId == 0) revert NoActivePosition();

        _decreaseAndCollectAll(tokenId, amount0Min, amount1Min, deadline);
        _convertAllToAsset(_effectiveSwapFee(swapFee), minSwapOut);

        assetOut = assetToken.balanceOf(address(this));
        if (assetOut > 0) {
            assetToken.safeTransfer(recipient, assetOut);
            _applyPrincipalOutflow(assetOut);
            totalManagedReturnedAssets += assetOut;
        }

        if (burnPosition) {
            positionManager.burn(tokenId);
            activeTokenId = 0;
        }

        emit PositionWithdrawn(tokenId, recipient, assetOut, burnPosition);
    }

    function _estimateSwapForRebalance(uint256 balance0, uint256 balance1)
        internal
        view
        returns (bool swapToken0ToToken1, uint256 amountIn, uint16 amountInBps)
    {
        if (balance0 == 0 && balance1 == 0) {
            return (true, 0, 0);
        }

        if (balance0 == 0) {
            amountIn = balance1 / 2;
            amountInBps = _toBps(amountIn, balance1);
            return (false, amountIn, amountInBps);
        }

        if (balance1 == 0) {
            amountIn = balance0 / 2;
            amountInBps = _toBps(amountIn, balance0);
            return (true, amountIn, amountInBps);
        }

        (uint160 sqrtPriceX96,,,,,,) = pool.slot0();
        if (sqrtPriceX96 == 0) revert InvalidSqrtPrice();

        uint256 value0InToken1 = _token0ToToken1(balance0, sqrtPriceX96);

        if (value0InToken1 > balance1) {
            uint256 diffValue = value0InToken1 - balance1;
            uint256 swapValueInToken1 = diffValue / 2;
            amountIn = _token1ToToken0(swapValueInToken1, sqrtPriceX96);
            if (amountIn > balance0) {
                amountIn = balance0;
            }
            amountInBps = _toBps(amountIn, balance0);
            return (true, amountIn, amountInBps);
        }

        uint256 diffToken1 = balance1 - value0InToken1;
        amountIn = diffToken1 / 2;
        if (amountIn > balance1) {
            amountIn = balance1;
        }
        amountInBps = _toBps(amountIn, balance1);
        return (false, amountIn, amountInBps);
    }

    function _splitAsset(uint256 amountIn, uint24 fee, uint256 minSwapOut) internal {
        IERC20 tokenIn = assetToken;
        IERC20 tokenOut = address(assetToken) == address(token0) ? token1 : token0;

        _approveIfNeeded(tokenIn, address(swapRouter), amountIn);

        uint256 amountOut = swapRouter.exactInputSingle(
            IV3SwapRouter.ExactInputSingleParams({
                tokenIn: address(tokenIn),
                tokenOut: address(tokenOut),
                fee: fee,
                recipient: address(this),
                amountIn: amountIn,
                amountOutMinimum: minSwapOut,
                sqrtPriceLimitX96: 0
            })
        );

        emit AssetSplitSwap(address(tokenIn), address(tokenOut), amountIn, amountOut, fee);
    }

    function _convertAllToAsset(uint24 fee, uint256 minSwapOut) internal {
        if (address(assetToken) == address(token0)) {
            uint256 amountIn = token1.balanceOf(address(this));
            if (amountIn > 0) {
                _approveIfNeeded(token1, address(swapRouter), amountIn);
                uint256 amountOut = swapRouter.exactInputSingle(
                    IV3SwapRouter.ExactInputSingleParams({
                        tokenIn: address(token1),
                        tokenOut: address(token0),
                        fee: fee,
                        recipient: address(this),
                        amountIn: amountIn,
                        amountOutMinimum: minSwapOut,
                        sqrtPriceLimitX96: 0
                    })
                );

                emit AssetConvertedToAsset(address(token1), amountIn, amountOut, fee);
            }
            return;
        }

        uint256 amountInToken0 = token0.balanceOf(address(this));
        if (amountInToken0 > 0) {
            _approveIfNeeded(token0, address(swapRouter), amountInToken0);
            uint256 amountOutToken1 = swapRouter.exactInputSingle(
                IV3SwapRouter.ExactInputSingleParams({
                    tokenIn: address(token0),
                    tokenOut: address(token1),
                    fee: fee,
                    recipient: address(this),
                    amountIn: amountInToken0,
                    amountOutMinimum: minSwapOut,
                    sqrtPriceLimitX96: 0
                })
            );

            emit AssetConvertedToAsset(address(token0), amountInToken0, amountOutToken1, fee);
        }
    }

    function _rebalanceSwap(bool token0ToToken1, uint256 amountIn, uint24 fee, uint256 minSwapOut) internal {
        IERC20 tokenIn = token0ToToken1 ? token0 : token1;
        IERC20 tokenOut = token0ToToken1 ? token1 : token0;

        if (tokenIn.balanceOf(address(this)) < amountIn) revert InsufficientBalance();

        _approveIfNeeded(tokenIn, address(swapRouter), amountIn);

        uint256 amountOut = swapRouter.exactInputSingle(
            IV3SwapRouter.ExactInputSingleParams({
                tokenIn: address(tokenIn),
                tokenOut: address(tokenOut),
                fee: fee,
                recipient: address(this),
                amountIn: amountIn,
                amountOutMinimum: minSwapOut,
                sqrtPriceLimitX96: 0
            })
        );

        emit AssetSplitSwap(address(tokenIn), address(tokenOut), amountIn, amountOut, fee);
    }

    function _mintFromCurrentBalances(
        int24 tickLower,
        int24 tickUpper,
        uint256 amount0Min,
        uint256 amount1Min,
        uint256 deadline
    ) internal returns (uint256 tokenId, uint128 liquidity, uint256 amount0, uint256 amount1) {
        uint256 amount0Desired = token0.balanceOf(address(this));
        uint256 amount1Desired = token1.balanceOf(address(this));
        if (amount0Desired == 0 && amount1Desired == 0) revert ZeroAmount();

        _approveIfNeeded(token0, address(positionManager), amount0Desired);
        _approveIfNeeded(token1, address(positionManager), amount1Desired);

        (tokenId, liquidity, amount0, amount1) = positionManager.mint(
            INonfungiblePositionManager.MintParams({
                token0: address(token0),
                token1: address(token1),
                fee: poolFee,
                tickLower: tickLower,
                tickUpper: tickUpper,
                amount0Desired: amount0Desired,
                amount1Desired: amount1Desired,
                amount0Min: amount0Min,
                amount1Min: amount1Min,
                recipient: address(this),
                deadline: deadline
            })
        );
    }

    function _decreaseAndCollectAll(uint256 tokenId, uint256 amount0Min, uint256 amount1Min, uint256 deadline) internal {
        uint128 liquidity = _positionLiquidity(tokenId);
        if (liquidity > 0) {
            positionManager.decreaseLiquidity(
                INonfungiblePositionManager.DecreaseLiquidityParams({
                    tokenId: tokenId,
                    liquidity: liquidity,
                    amount0Min: amount0Min,
                    amount1Min: amount1Min,
                    deadline: deadline
                })
            );
        }

        positionManager.collect(
            INonfungiblePositionManager.CollectParams({
                tokenId: tokenId,
                recipient: address(this),
                amount0Max: type(uint128).max,
                amount1Max: type(uint128).max
            })
        );
    }

    function _positionLiquidity(uint256 tokenId) internal view returns (uint128 liquidity) {
        (
            uint96 nonce_,
            address operator_,
            address posToken0,
            address posToken1,
            uint24 posFee,
            int24 tickLower_,
            int24 tickUpper_,
            uint128 posLiquidity,
            uint256 feeGrowthInside0LastX128_,
            uint256 feeGrowthInside1LastX128_,
            uint128 tokensOwed0_,
            uint128 tokensOwed1_
        ) = positionManager.positions(tokenId);

        nonce_;
        operator_;
        tickLower_;
        tickUpper_;
        feeGrowthInside0LastX128_;
        feeGrowthInside1LastX128_;
        tokensOwed0_;
        tokensOwed1_;

        liquidity = posLiquidity;
        if (posToken0 != address(token0) || posToken1 != address(token1) || posFee != poolFee) {
            revert PositionPoolMismatch();
        }
    }

    function _token0ToToken1(uint256 amount0, uint160 sqrtPriceX96) internal pure returns (uint256) {
        uint256 step = Math.mulDiv(amount0, uint256(sqrtPriceX96), Q96);
        return Math.mulDiv(step, uint256(sqrtPriceX96), Q96);
    }

    function _token1ToToken0(uint256 amount1, uint160 sqrtPriceX96) internal pure returns (uint256) {
        uint256 step = Math.mulDiv(amount1, Q96, uint256(sqrtPriceX96));
        return Math.mulDiv(step, Q96, uint256(sqrtPriceX96));
    }

    function _toBps(uint256 numerator, uint256 denominator) internal pure returns (uint16) {
        if (denominator == 0 || numerator == 0) {
            return 0;
        }
        uint256 bps = (numerator * 10_000) / denominator;
        if (bps > type(uint16).max) {
            return type(uint16).max;
        }
        return uint16(bps);
    }

    function _effectiveSwapFee(uint24 providedFee) internal view returns (uint24) {
        return providedFee == 0 ? poolFee : providedFee;
    }

    function _estimatedNavInAsset() internal view returns (uint256 navAsset) {
        uint256 balance0 = token0.balanceOf(address(this));
        uint256 balance1 = token1.balanceOf(address(this));

        (uint160 sqrtPriceX96,,,,,,) = pool.slot0();
        if (sqrtPriceX96 == 0) revert InvalidSqrtPrice();

        uint256 tokenId = activeTokenId;
        if (tokenId != 0) {
            (
                uint96 nonce_,
                address operator_,
                address posToken0,
                address posToken1,
                uint24 posFee,
                int24 tickLower,
                int24 tickUpper,
                uint128 liquidity,
                uint256 feeGrowthInside0LastX128_,
                uint256 feeGrowthInside1LastX128_,
                uint128 tokensOwed0,
                uint128 tokensOwed1
            ) = positionManager.positions(tokenId);

            nonce_;
            operator_;
            feeGrowthInside0LastX128_;
            feeGrowthInside1LastX128_;

            if (posToken0 != address(token0) || posToken1 != address(token1) || posFee != poolFee) {
                revert PositionPoolMismatch();
            }

            (uint256 liqAmount0, uint256 liqAmount1) = _amountsForLiquidity(liquidity, tickLower, tickUpper, sqrtPriceX96);
            balance0 += liqAmount0 + uint256(tokensOwed0);
            balance1 += liqAmount1 + uint256(tokensOwed1);
        }

        if (address(assetToken) == address(token0)) {
            navAsset = balance0 + _token1ToToken0(balance1, sqrtPriceX96);
        } else {
            navAsset = balance1 + _token0ToToken1(balance0, sqrtPriceX96);
        }
    }

    function _applyPrincipalOutflow(uint256 outflow) internal {
        if (outflow == 0) {
            return;
        }

        uint256 oldPrincipal = accountedPrincipal;
        if (outflow >= oldPrincipal) {
            accountedPrincipal = 0;
        } else {
            accountedPrincipal = oldPrincipal - outflow;
        }

        emit AccountedPrincipalUpdated(oldPrincipal, accountedPrincipal);
    }

    function _amountsForLiquidity(uint128 liquidity, int24 tickLower, int24 tickUpper, uint160 sqrtPriceX96)
        internal
        pure
        returns (uint256 amount0, uint256 amount1)
    {
        if (liquidity == 0) {
            return (0, 0);
        }

        uint160 sqrtRatioAX96 = _sqrtRatioAtTick(tickLower);
        uint160 sqrtRatioBX96 = _sqrtRatioAtTick(tickUpper);
        if (sqrtRatioAX96 > sqrtRatioBX96) {
            (sqrtRatioAX96, sqrtRatioBX96) = (sqrtRatioBX96, sqrtRatioAX96);
        }

        if (sqrtPriceX96 <= sqrtRatioAX96) {
            amount0 = _amount0ForLiquidity(sqrtRatioAX96, sqrtRatioBX96, liquidity);
        } else if (sqrtPriceX96 < sqrtRatioBX96) {
            amount0 = _amount0ForLiquidity(sqrtPriceX96, sqrtRatioBX96, liquidity);
            amount1 = _amount1ForLiquidity(sqrtRatioAX96, sqrtPriceX96, liquidity);
        } else {
            amount1 = _amount1ForLiquidity(sqrtRatioAX96, sqrtRatioBX96, liquidity);
        }
    }

    function _amount0ForLiquidity(uint160 sqrtRatioAX96, uint160 sqrtRatioBX96, uint128 liquidity)
        internal
        pure
        returns (uint256 amount0)
    {
        if (sqrtRatioAX96 > sqrtRatioBX96) {
            (sqrtRatioAX96, sqrtRatioBX96) = (sqrtRatioBX96, sqrtRatioAX96);
        }

        uint256 numerator1 = uint256(liquidity) << 96;
        uint256 numerator2 = uint256(sqrtRatioBX96) - uint256(sqrtRatioAX96);
        uint256 intermediate = Math.mulDiv(numerator1, numerator2, uint256(sqrtRatioBX96));
        amount0 = intermediate / uint256(sqrtRatioAX96);
    }

    function _amount1ForLiquidity(uint160 sqrtRatioAX96, uint160 sqrtRatioBX96, uint128 liquidity)
        internal
        pure
        returns (uint256 amount1)
    {
        if (sqrtRatioAX96 > sqrtRatioBX96) {
            (sqrtRatioAX96, sqrtRatioBX96) = (sqrtRatioBX96, sqrtRatioAX96);
        }
        amount1 = Math.mulDiv(uint256(liquidity), uint256(sqrtRatioBX96) - uint256(sqrtRatioAX96), Q96);
    }

    function _sqrtRatioAtTick(int24 tick) internal pure returns (uint160 sqrtPriceX96) {
        int256 tickInt = int256(tick);
        uint256 absTick = uint256(tickInt < 0 ? -tickInt : tickInt);
        if (absTick > uint256(uint24(MAX_TICK))) revert InvalidTickRange();

        uint256 ratio = absTick & 0x1 != 0
            ? 0xfffcb933bd6fad37aa2d162d1a5940c1
            : 0x100000000000000000000000000000000;
        if (absTick & 0x2 != 0) ratio = (ratio * 0xfff97272373d413259a46990580e213a) >> 128;
        if (absTick & 0x4 != 0) ratio = (ratio * 0xfff2e50f5f656932ef12357cf3c7fdcc) >> 128;
        if (absTick & 0x8 != 0) ratio = (ratio * 0xffe5caca7e10e4e61c3624eaa0941cd0) >> 128;
        if (absTick & 0x10 != 0) ratio = (ratio * 0xffcb9843d60f6159c9db58835c926644) >> 128;
        if (absTick & 0x20 != 0) ratio = (ratio * 0xff973b41fa98c081472e6896dfb254c0) >> 128;
        if (absTick & 0x40 != 0) ratio = (ratio * 0xff2ea16466c96a3843ec78b326b52861) >> 128;
        if (absTick & 0x80 != 0) ratio = (ratio * 0xfe5dee046a99a2a811c461f1969c3053) >> 128;
        if (absTick & 0x100 != 0) ratio = (ratio * 0xfcbe86c7900a88aedcffc83b479aa3a4) >> 128;
        if (absTick & 0x200 != 0) ratio = (ratio * 0xf987a7253ac413176f2b074cf7815e54) >> 128;
        if (absTick & 0x400 != 0) ratio = (ratio * 0xf3392b0822b70005940c7a398e4b70f3) >> 128;
        if (absTick & 0x800 != 0) ratio = (ratio * 0xe7159475a2c29b7443b29c7fa6e889d9) >> 128;
        if (absTick & 0x1000 != 0) ratio = (ratio * 0xd097f3bdfd2022b8845ad8f792aa5825) >> 128;
        if (absTick & 0x2000 != 0) ratio = (ratio * 0xa9f746462d870fdf8a65dc1f90e061e5) >> 128;
        if (absTick & 0x4000 != 0) ratio = (ratio * 0x70d869a156d2a1b890bb3df62baf32f7) >> 128;
        if (absTick & 0x8000 != 0) ratio = (ratio * 0x31be135f97d08fd981231505542fcfa6) >> 128;
        if (absTick & 0x10000 != 0) ratio = (ratio * 0x9aa508b5b7a84e1c677de54f3e99bc9) >> 128;
        if (absTick & 0x20000 != 0) ratio = (ratio * 0x5d6af8fbc1ada9856dfd0f01de96b24) >> 128;
        if (absTick & 0x40000 != 0) ratio = (ratio * 0x2439a066b1d40134bc217d84f09d842) >> 128;
        if (absTick & 0x80000 != 0) ratio = (ratio * 0x11746f3286377366367f1b7ca1e3db9) >> 128;

        if (tick > 0) {
            ratio = type(uint256).max / ratio;
        }

        sqrtPriceX96 = uint160((ratio >> 32) + (ratio % (1 << 32) == 0 ? 0 : 1));
    }

    function _toInt256(uint256 value) internal pure returns (int256) {
        if (value > uint256(type(int256).max)) revert IntOverflow();
        return int256(value);
    }

    function _validateTicks(int24 tickLower, int24 tickUpper) internal view {
        if (tickLower >= tickUpper) revert InvalidTickRange();
        if (tickLower < MIN_TICK || tickUpper > MAX_TICK) revert InvalidTickRange();

        int24 spacing = pool.tickSpacing();
        if (tickLower % spacing != 0 || tickUpper % spacing != 0) {
            revert InvalidTickSpacing();
        }
    }

    function _approveIfNeeded(IERC20 token, address spender, uint256 required) internal {
        if (required == 0) {
            return;
        }
        uint256 allowance = token.allowance(address(this), spender);
        if (allowance < required) {
            token.forceApprove(spender, type(uint256).max);
        }
    }
}
