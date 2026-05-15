// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.20;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "openzeppelin-contracts/contracts/token/ERC20/IERC20.sol";

import {BotRegistry} from "../src/BotRegistry.sol";
import {Vault} from "../src/Vault.sol";
import {StrategyRouter} from "../src/StrategyRouter.sol";
import {UniswapV3LPStrategy} from "../src/UniswapV3LPStrategy.sol";

interface IWETHForkLike {
    function deposit() external payable;
}

interface IUniswapV3PoolRange {
    function fee() external view returns (uint24);
    function tickSpacing() external view returns (int24);
    function slot0()
        external
        view
        returns (
            uint160 sqrtPriceX96,
            int24 tick,
            uint16 observationIndex,
            uint16 observationCardinality,
            uint16 observationCardinalityNext,
            uint8 feeProtocol,
            bool unlocked
        );
}

interface IPositionManagerView {
    function positions(uint256 tokenId)
        external
        view
        returns (
            uint96 nonce,
            address operator,
            address token0,
            address token1,
            uint24 fee,
            int24 tickLower,
            int24 tickUpper,
            uint128 liquidity,
            uint256 feeGrowthInside0LastX128,
            uint256 feeGrowthInside1LastX128,
            uint128 tokensOwed0,
            uint128 tokensOwed1
        );
}

contract SepoliaForkUniswapV3LPStrategyTest is Test {
    // Uniswap 官方 Sepolia 部署地址（來源：Uniswap v3 Ethereum Deployments）
    address internal constant SEPOLIA_SWAP_ROUTER02 = 0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E;
    address internal constant SEPOLIA_POSITION_MANAGER = 0x1238536071E1c677A632429e3655c799b22cDA52;

    address internal constant WETH = 0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14;
    address internal constant USDC = 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238;

    // 使用者指定池
    address internal constant TARGET_POOL = 0x6418EEC70f50913ff0d756B48d32Ce7C02b47C47;

    UniswapV3LPStrategy public strategy;

    function setUp() public {
        uint256 forkId = vm.createFork("https://ethereum-sepolia-rpc.publicnode.com");
        vm.selectFork(forkId);

        require(SEPOLIA_SWAP_ROUTER02.code.length > 0, "ROUTER_NO_CODE");
        require(SEPOLIA_POSITION_MANAGER.code.length > 0, "POS_MANAGER_NO_CODE");
        require(WETH.code.length > 0, "WETH_NO_CODE");
        require(USDC.code.length > 0, "USDC_NO_CODE");
        require(TARGET_POOL.code.length > 0, "POOL_NO_CODE");

        strategy = new UniswapV3LPStrategy(TARGET_POOL, SEPOLIA_POSITION_MANAGER, SEPOLIA_SWAP_ROUTER02, WETH);

        vm.deal(address(this), 5 ether);
        IWETHForkLike(WETH).deposit{value: 2 ether}();
        IERC20(WETH).approve(address(strategy), type(uint256).max);
    }

    function test_Fork_ProvideInitialLiquidity_UsingTargetPool() public {
        (int24 tickLower, int24 tickUpper) = _defaultTicks();
        uint24 poolFee = IUniswapV3PoolRange(TARGET_POOL).fee();

        (uint256 tokenId, uint128 liquidity,,) = strategy.provideInitialLiquidity(
            UniswapV3LPStrategy.InitialLiquidityParams({
                amountAssetIn: 0.2 ether,
                swapFee: poolFee,
                minSwapOut: 1,
                tickLower: tickLower,
                tickUpper: tickUpper,
                amount0Min: 0,
                amount1Min: 0,
                deadline: block.timestamp + 15 minutes
            })
        );

        assertGt(tokenId, 0);
        assertEq(strategy.activeTokenId(), tokenId);
        assertGt(uint256(liquidity), 0);
    }

    function test_Fork_RebalanceAndWithdraw_UsingTargetPool() public {
        (int24 tickLower, int24 tickUpper) = _defaultTicks();
        uint24 poolFee = IUniswapV3PoolRange(TARGET_POOL).fee();

        (uint256 oldTokenId,,,) = strategy.provideInitialLiquidity(
            UniswapV3LPStrategy.InitialLiquidityParams({
                amountAssetIn: 0.2 ether,
                swapFee: poolFee,
                minSwapOut: 1,
                tickLower: tickLower,
                tickUpper: tickUpper,
                amount0Min: 0,
                amount1Min: 0,
                deadline: block.timestamp + 15 minutes
            })
        );

        int24 spacing = IUniswapV3PoolRange(TARGET_POOL).tickSpacing();
        int24 newLower = tickLower + spacing;
        int24 newUpper = tickUpper + spacing;

        (uint256 newTokenId, uint128 newLiquidity,,) = strategy.rebalancePosition(
            UniswapV3LPStrategy.RebalanceParams({
                newTickLower: newLower,
                newTickUpper: newUpper,
                swapFee: poolFee,
                swapToken0ToToken1: false,
                rebalanceSwapAmountIn: 0,
                minSwapOut: 0,
                amount0Min: 0,
                amount1Min: 0,
                deadline: block.timestamp + 15 minutes
            })
        );

        assertTrue(newTokenId != oldTokenId);
        assertEq(strategy.activeTokenId(), newTokenId);
        assertGt(uint256(newLiquidity), 0);

        uint256 wethBefore = IERC20(WETH).balanceOf(address(this));

        uint256 assetOut = strategy.withdrawAllLiquidity(address(this), 0, 0, block.timestamp + 15 minutes, true);

        assertEq(strategy.activeTokenId(), 0);
        assertGt(assetOut, 0);
        assertEq(IERC20(WETH).balanceOf(address(strategy)), 0);
        assertEq(IERC20(USDC).balanceOf(address(strategy)), 0);
        assertGe(IERC20(WETH).balanceOf(address(this)), wethBefore);
    }

    function test_Fork_RouterManagedFlow_WithdrawsAsWETH() public {
        (int24 tickLower, int24 tickUpper) = _defaultTicks();
        uint24 poolFee = IUniswapV3PoolRange(TARGET_POOL).fee();

        strategy.provideInitialLiquidity(
            UniswapV3LPStrategy.InitialLiquidityParams({
                amountAssetIn: 0.2 ether,
                swapFee: poolFee,
                minSwapOut: 1,
                tickLower: tickLower,
                tickUpper: tickUpper,
                amount0Min: 0,
                amount1Min: 0,
                deadline: block.timestamp + 15 minutes
            })
        );

        Vault vault = new Vault(WETH, "MEV Vault Share", "mvSHARE");
        BotRegistry registry = new BotRegistry();
        StrategyRouter router = new StrategyRouter(address(registry), address(vault));

        vault.setRouter(address(router));
        router.setExecutor(address(this), true);
        router.setExecutorSource(address(this), address(strategy), true);

        registry.addSource(address(strategy));
        vault.setStrategyCapBps(address(strategy), 10_000);
        strategy.setRouter(address(router));

        bytes32 contextHash = keccak256("lp-router-manage");
        UniswapV3LPStrategy.RouterExecuteParams memory p = UniswapV3LPStrategy.RouterExecuteParams({
            expectedContextHash: contextHash,
            action: 1,
            newTickLower: 0,
            newTickUpper: 0,
            swapFee: poolFee,
            rebalanceSwapAmountIn: 0,
            minSwapOut: 1,
            amount0Min: 0,
            amount1Min: 0,
            deadline: block.timestamp + 15 minutes,
            burnPosition: true
        });

        uint256 vaultWethBefore = IERC20(WETH).balanceOf(address(vault));
        (int256 realizedPnl, uint256 returnedAssets,,) = router.executeManagedStrategy(
            address(strategy),
            abi.encode(p),
            contextHash,
            0,
            10_000,
            1
        );

        assertTrue(realizedPnl > -int256(0.05 ether));
        assertGt(returnedAssets, 0);
        assertEq(strategy.activeTokenId(), 0);
        assertEq(IERC20(USDC).balanceOf(address(strategy)), 0);
        assertEq(IERC20(WETH).balanceOf(address(strategy)), 0);
        assertEq(IERC20(WETH).balanceOf(address(vault)) - vaultWethBefore, returnedAssets);
    }

    function test_Fork_PositionBoundToTargetPool() public {
        (int24 tickLower, int24 tickUpper) = _defaultTicks();
        uint24 poolFee = IUniswapV3PoolRange(TARGET_POOL).fee();

        (uint256 tokenId,,,) = strategy.provideInitialLiquidity(
            UniswapV3LPStrategy.InitialLiquidityParams({
                amountAssetIn: 0.2 ether,
                swapFee: poolFee,
                minSwapOut: 1,
                tickLower: tickLower,
                tickUpper: tickUpper,
                amount0Min: 0,
                amount1Min: 0,
                deadline: block.timestamp + 15 minutes
            })
        );

        (, , address token0, address token1, uint24 fee, int24 storedLower, int24 storedUpper, uint128 liquidity, , , ,) =
            IPositionManagerView(SEPOLIA_POSITION_MANAGER).positions(tokenId);

        assertEq(token0, USDC);
        assertEq(token1, WETH);
        assertEq(fee, poolFee);
        assertEq(storedLower, tickLower);
        assertEq(storedUpper, tickUpper);
        assertGt(uint256(liquidity), 0);
    }

    function _defaultTicks() internal view returns (int24 tickLower, int24 tickUpper) {
        IUniswapV3PoolRange pool = IUniswapV3PoolRange(TARGET_POOL);
        int24 spacing = pool.tickSpacing();
        (, int24 currentTick,,,,,) = pool.slot0();

        int24 center = _floorToSpacing(currentTick, spacing);
        int24 width = spacing * 10;

        tickLower = center - width;
        tickUpper = center + width;
    }

    function _floorToSpacing(int24 tick, int24 spacing) internal pure returns (int24) {
        int24 compressed = tick / spacing;
        if (tick < 0 && tick % spacing != 0) {
            compressed -= 1;
        }
        return compressed * spacing;
    }
}
