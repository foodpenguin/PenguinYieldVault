// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Math} from "openzeppelin-contracts/contracts/utils/math/Math.sol";
import {IUniswapV3Pool} from "./interfaces/uniswap/IUniswapV3Pool.sol";

/// @title Sepolia V3 multi-pool quote lens
/// @notice Batch quote and spread scan for bot-side opportunity detection
contract SepoliaV3PoolLens {
    uint256 internal constant Q96 = 2 ** 96;
    uint256 internal constant FEE_DENOMINATOR = 1_000_000;

    address public immutable observedRouter;

    struct PoolQuote {
        address pool;
        address tokenIn;
        address tokenOut;
        uint24 fee;
        uint128 liquidity;
        uint160 sqrtPriceX96;
        uint256 reserveInVirtual;
        uint256 reserveOutVirtual;
        uint256 amountIn;
        uint256 amountOut;
    }

    constructor(address observedRouter_) {
        require(observedRouter_ != address(0), "ZERO_ROUTER");
        observedRouter = observedRouter_;
    }

    /// @notice Check if observed router has deployed code
    function routerHasCode() external view returns (bool) {
        return observedRouter.code.length > 0;
    }

    /// @notice Quote a single pool using slot0 price (floor rounding)
    function quotePool(address pool, address tokenIn, uint256 amountIn) public view returns (PoolQuote memory quote) {
        require(pool != address(0), "ZERO_POOL");
        require(tokenIn != address(0), "ZERO_TOKEN_IN");
        require(amountIn > 0, "ZERO_AMOUNT_IN");

        IUniswapV3Pool v3pool = IUniswapV3Pool(pool);
        address token0 = v3pool.token0();
        address token1 = v3pool.token1();
        require(tokenIn == token0 || tokenIn == token1, "TOKEN_NOT_IN_POOL");

        quote.pool = pool;
        quote.tokenIn = tokenIn;
        quote.fee = v3pool.fee();
        quote.liquidity = v3pool.liquidity();
        (quote.sqrtPriceX96,,,,,,) = v3pool.slot0();
        quote.amountIn = amountIn;

        (uint256 reserve0Virtual, uint256 reserve1Virtual) = _virtualReserves(quote.sqrtPriceX96, quote.liquidity);

        if (tokenIn == token0) {
            quote.tokenOut = token1;
            quote.reserveInVirtual = reserve0Virtual;
            quote.reserveOutVirtual = reserve1Virtual;
        } else {
            quote.tokenOut = token0;
            quote.reserveInVirtual = reserve1Virtual;
            quote.reserveOutVirtual = reserve0Virtual;
        }

        quote.amountOut = _quoteAmountOut(quote.amountIn, quote.fee, quote.reserveInVirtual, quote.reserveOutVirtual);
    }

    /// @notice Scan multiple pools; returns quotes, min/max and spread in bps
    function scanPools(address[] calldata pools, address tokenIn, uint256 amountIn)
        external
        view
        returns (PoolQuote[] memory quotes, uint256 minQuote, uint256 maxQuote, uint256 spreadBps)
    {
        require(pools.length > 0, "EMPTY_POOLS");

        quotes = new PoolQuote[](pools.length);
        minQuote = type(uint256).max;

        uint256 length = pools.length;
        for (uint256 i = 0; i < length;) {
            PoolQuote memory q = quotePool(pools[i], tokenIn, amountIn);
            quotes[i] = q;

            if (q.amountOut < minQuote) {
                minQuote = q.amountOut;
            }
            if (q.amountOut > maxQuote) {
                maxQuote = q.amountOut;
            }

            unchecked {
                ++i;
            }
        }

        if (minQuote > 0 && maxQuote > minQuote) {
            spreadBps = ((maxQuote - minQuote) * 10_000) / minQuote;
        }
    }

    function _virtualReserves(uint160 sqrtPriceX96, uint128 liquidity)
        internal
        pure
        returns (uint256 reserve0Virtual, uint256 reserve1Virtual)
    {
        require(sqrtPriceX96 > 0, "INVALID_SQRT_PRICE");
        require(liquidity > 0, "ZERO_LIQUIDITY");

        reserve0Virtual = Math.mulDiv(uint256(liquidity), Q96, uint256(sqrtPriceX96));
        reserve1Virtual = Math.mulDiv(uint256(liquidity), uint256(sqrtPriceX96), Q96);
        require(reserve0Virtual > 0 && reserve1Virtual > 0, "INVALID_VIRTUAL_RESERVE");
    }

    function _quoteAmountOut(uint256 amountIn, uint24 poolFee, uint256 reserveInVirtual, uint256 reserveOutVirtual)
        internal
        pure
        returns (uint256 amountOut)
    {
        require(poolFee < FEE_DENOMINATOR, "BAD_FEE");
        uint256 amountInAfterFee = Math.mulDiv(amountIn, FEE_DENOMINATOR - poolFee, FEE_DENOMINATOR);
        amountOut = Math.mulDiv(reserveOutVirtual, amountInAfterFee, reserveInVirtual + amountInAfterFee);
    }
}
