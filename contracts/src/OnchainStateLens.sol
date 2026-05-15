// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Math} from "openzeppelin-contracts/contracts/utils/math/Math.sol";
import {IUniswapV3Pool} from "./interfaces/uniswap/IUniswapV3Pool.sol";

/// @title On-chain state lens
/// @notice Same-tx pool snapshot and quote for atomic pre-check
contract OnchainStateLens {
    uint256 internal constant Q96 = 2 ** 96;
    uint256 internal constant FEE_DENOMINATOR = 1_000_000;

    struct V3PoolSnapshot {
        address pool;
        address tokenIn;
        address tokenOut;
        uint24 fee;
        uint128 liquidity;
        uint160 sqrtPriceX96;
        int24 tick;
        uint256 reserveInVirtual;
        uint256 reserveOutVirtual;
        uint256 quoteAmountOut;
    }

    /// @notice Snapshot a V3 pool's current state and compute estimated output
    function getV3PoolSnapshot(
        address pool,
        address tokenIn,
        address tokenOut,
        uint256 amountIn
    ) external view returns (V3PoolSnapshot memory snapshot) {
        require(pool != address(0), "ZERO_POOL");
        require(tokenIn != address(0) && tokenOut != address(0), "ZERO_TOKEN");
        require(tokenIn != tokenOut, "SAME_TOKEN");
        require(amountIn > 0, "ZERO_AMOUNT_IN");

        IUniswapV3Pool v3pool = IUniswapV3Pool(pool);
        address token0 = v3pool.token0();
        address token1 = v3pool.token1();
        require(
            (tokenIn == token0 && tokenOut == token1) || (tokenIn == token1 && tokenOut == token0),
            "POOL_TOKEN_MISMATCH"
        );

        uint24 poolFee = v3pool.fee();
        uint128 currentLiquidity = v3pool.liquidity();
        (uint160 sqrtPriceX96, int24 tick,,,,,) = v3pool.slot0();

        (uint256 reserve0Virtual, uint256 reserve1Virtual) = _virtualReserves(sqrtPriceX96, currentLiquidity);
        bool tokenInIsToken0 = tokenIn == token0;

        snapshot.pool = pool;
        snapshot.tokenIn = tokenIn;
        snapshot.tokenOut = tokenOut;
        snapshot.fee = poolFee;
        snapshot.liquidity = currentLiquidity;
        snapshot.sqrtPriceX96 = sqrtPriceX96;
        snapshot.tick = tick;
        snapshot.reserveInVirtual = tokenInIsToken0 ? reserve0Virtual : reserve1Virtual;
        snapshot.reserveOutVirtual = tokenInIsToken0 ? reserve1Virtual : reserve0Virtual;
        snapshot.quoteAmountOut = quoteV3AmountOut(amountIn, sqrtPriceX96, currentLiquidity, poolFee, tokenInIsToken0);
    }

    /// @notice V3 quote using slot0 sqrtPriceX96, liquidity and fee (floor rounding)
    function quoteV3AmountOut(
        uint256 amountIn,
        uint160 sqrtPriceX96,
        uint128 liquidity,
        uint24 poolFee,
        bool tokenInIsToken0
    )
        public
        pure
        returns (uint256 amountOut)
    {
        require(amountIn > 0, "ZERO_AMOUNT_IN");
        require(sqrtPriceX96 > 0, "INVALID_SQRT_PRICE");
        require(liquidity > 0, "ZERO_LIQUIDITY");
        require(poolFee < FEE_DENOMINATOR, "BAD_FEE");

        uint256 amountInAfterFee = Math.mulDiv(amountIn, FEE_DENOMINATOR - poolFee, FEE_DENOMINATOR);

        (uint256 reserve0Virtual, uint256 reserve1Virtual) = _virtualReserves(sqrtPriceX96, liquidity);

        if (tokenInIsToken0) {
            amountOut = Math.mulDiv(
                reserve1Virtual,
                amountInAfterFee,
                reserve0Virtual + amountInAfterFee
            );
        } else {
            amountOut = Math.mulDiv(
                reserve0Virtual,
                amountInAfterFee,
                reserve1Virtual + amountInAfterFee
            );
        }
    }

    /// @dev Estimate in-range virtual reserves from current sqrtPrice and liquidity
    function _virtualReserves(uint160 sqrtPriceX96, uint128 liquidity)
        internal
        pure
        returns (uint256 reserve0Virtual, uint256 reserve1Virtual)
    {
        reserve0Virtual = Math.mulDiv(uint256(liquidity), Q96, uint256(sqrtPriceX96));
        reserve1Virtual = Math.mulDiv(uint256(liquidity), uint256(sqrtPriceX96), Q96);
        require(reserve0Virtual > 0 && reserve1Virtual > 0, "INVALID_VIRTUAL_RESERVE");
    }
}
