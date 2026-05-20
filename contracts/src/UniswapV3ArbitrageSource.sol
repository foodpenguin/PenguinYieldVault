// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Ownable} from "openzeppelin-contracts/contracts/access/Ownable.sol";
import {IERC20} from "openzeppelin-contracts/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "openzeppelin-contracts/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "openzeppelin-contracts/contracts/utils/ReentrancyGuard.sol";

import {IProfitSource} from "./interfaces/IProfitSource.sol";
import {IUniswapV3Factory} from "./interfaces/uniswap/IUniswapV3Factory.sol";
import {IV3SwapRouter} from "./interfaces/uniswap/IV3SwapRouter.sol";

/// @title Uniswap V3 two-leg arbitrage source
/// @notice Executes tokenIn → tokenMid → tokenIn via two V3 swaps, returns all to Router
contract UniswapV3ArbitrageSource is IProfitSource, Ownable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    struct ArbitrageParams {
        bytes32 expectedContextHash;
        address buyPool;
        address sellPool;
        address tokenIn;
        address tokenMid;
        uint24 buyFee;
        uint24 sellFee;
        uint256 amountIn;
        uint256 minBuyAmountOut;
        uint256 minFinalAmountOut;
        uint256 deadline;
    }

    IV3SwapRouter public immutable swapRouter;
    IUniswapV3Factory public immutable poolFactory;
    IERC20 public immutable assetToken;
    address public router;

    string private _id;
    bool private _active;

    event ActiveUpdated(bool active);
    event RouterUpdated(address indexed oldRouter, address indexed newRouter);
    event ArbitrageExecuted(
        address indexed caller,
        address indexed buyPool,
        address indexed sellPool,
        uint256 amountIn,
        uint256 amountMid,
        uint256 finalAmount,
        int256 realizedPnl,
        bytes32 executionContextHash
    );

    error SourceInactive();
    error ContextMismatch();
    error Expired();
    error ZeroAmountIn();
    error SameToken();
    error InvalidTokenIn();
    error InvalidPool(address expected, address actual);
    error InsufficientBalance();
    error AmountInMismatch();
    error ZeroAddress();
    error NotRouterCaller();

    constructor(address swapRouter_, address asset_, string memory sourceId_) Ownable(msg.sender) {
        if (swapRouter_ == address(0) || asset_ == address(0)) revert ZeroAddress();

        swapRouter = IV3SwapRouter(swapRouter_);
        poolFactory = IUniswapV3Factory(IV3SwapRouter(swapRouter_).factory());
        assetToken = IERC20(asset_);
        _id = sourceId_;
        _active = true;
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

    function setActive(bool active_) external onlyOwner {
        _active = active_;
        emit ActiveUpdated(active_);
    }

    function setRouter(address newRouter) external onlyOwner {
        if (newRouter == address(0)) revert ZeroAddress();
        emit RouterUpdated(router, newRouter);
        router = newRouter;
    }

    function execute(bytes calldata params, bytes32 executionContextHash, uint256 amountIn)
        external
        nonReentrant
        returns (int256 realizedPnl, uint256 returnedAssets)
    {
        if (!_active) revert SourceInactive();
        if (msg.sender != router) revert NotRouterCaller();

        ArbitrageParams memory p = abi.decode(params, (ArbitrageParams));

        if (p.expectedContextHash != bytes32(0) && p.expectedContextHash != executionContextHash) revert ContextMismatch();
        if (block.timestamp > p.deadline) revert Expired();
        if (p.amountIn == 0) revert ZeroAmountIn();
        if (p.amountIn != amountIn) revert AmountInMismatch();
        if (p.tokenIn == p.tokenMid) revert SameToken();
        if (p.tokenIn != address(assetToken)) revert InvalidTokenIn();

        address expectedBuyPool = poolFactory.getPool(p.tokenIn, p.tokenMid, p.buyFee);
        if (expectedBuyPool != p.buyPool) revert InvalidPool(expectedBuyPool, p.buyPool);

        address expectedSellPool = poolFactory.getPool(p.tokenMid, p.tokenIn, p.sellFee);
        if (expectedSellPool != p.sellPool) revert InvalidPool(expectedSellPool, p.sellPool);

        uint256 startingBalance = assetToken.balanceOf(address(this));
        if (startingBalance < p.amountIn) revert InsufficientBalance();

        _approveIfNeeded(IERC20(p.tokenIn), p.amountIn);
        uint256 amountMid = swapRouter.exactInputSingle(
            IV3SwapRouter.ExactInputSingleParams({
                tokenIn: p.tokenIn,
                tokenOut: p.tokenMid,
                fee: p.buyFee,
                recipient: address(this),
                amountIn: p.amountIn,
                amountOutMinimum: p.minBuyAmountOut,
                sqrtPriceLimitX96: 0
            })
        );

        _approveIfNeeded(IERC20(p.tokenMid), amountMid);
        uint256 finalAmount = swapRouter.exactInputSingle(
            IV3SwapRouter.ExactInputSingleParams({
                tokenIn: p.tokenMid,
                tokenOut: p.tokenIn,
                fee: p.sellFee,
                recipient: address(this),
                amountIn: amountMid,
                amountOutMinimum: p.minFinalAmountOut,
                sqrtPriceLimitX96: 0
            })
        );

        returnedAssets = finalAmount;
        if (returnedAssets > 0) {
            assetToken.safeTransfer(msg.sender, returnedAssets);
        }

        if (finalAmount >= p.amountIn) {
            unchecked {
                realizedPnl = int256(finalAmount - p.amountIn);
            }
        } else {
            unchecked {
                realizedPnl = -int256(p.amountIn - finalAmount);
            }
        }

        emit ArbitrageExecuted(
            msg.sender,
            p.buyPool,
            p.sellPool,
            p.amountIn,
            amountMid,
            finalAmount,
            realizedPnl,
            executionContextHash
        );
    }

    function rescueToken(address token, address to, uint256 amount) external onlyOwner {
        if (token == address(0) || to == address(0)) revert ZeroAddress();
        IERC20(token).safeTransfer(to, amount);
    }

    function _approveIfNeeded(IERC20 token, uint256 required) internal {
        uint256 allowance = token.allowance(address(this), address(swapRouter));
        if (allowance < required) {
            token.forceApprove(address(swapRouter), type(uint256).max);
        }
    }
}
