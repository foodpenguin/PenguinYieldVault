// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.20;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "openzeppelin-contracts/contracts/token/ERC20/IERC20.sol";

import {OnchainStateLens} from "../src/OnchainStateLens.sol";
import {UniswapV3ArbitrageSource} from "../src/UniswapV3ArbitrageSource.sol";

interface IWETHFork {
    function deposit() external payable;
}

interface IUniswapV3PoolFee {
    function fee() external view returns (uint24);
}

interface ISwapRouterSkew {
    struct ExactInputSingleParams {
        address tokenIn;
        address tokenOut;
        uint24 fee;
        address recipient;
        uint256 amountIn;
        uint256 amountOutMinimum;
        uint160 sqrtPriceLimitX96;
    }

    function exactInputSingle(ExactInputSingleParams calldata params)
        external
        payable
        returns (uint256 amountOut);
}

contract SepoliaForkUniswapV3ArbitrageSourceTest is Test {
    address internal constant SWAP_ROUTER = 0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E;
    address internal constant WETH = 0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14;
    address internal constant USDC = 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238;

    address internal constant USDC_WETH_POOL_A = 0x3289680dD4d6C10bb19b899729cda5eEF58AEfF1;
    address internal constant USDC_WETH_POOL_B = 0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50;
    address internal constant USDC_WETH_POOL_C = 0xFeEd501c2B21D315F04946F85fC6416B640240b5;
    address internal constant USDC_WETH_POOL_D = 0x6418EEC70f50913ff0d756B48d32Ce7C02b47C47;

    struct BestRoute {
        address buyPool;
        address sellPool;
        uint24 buyFee;
        uint24 sellFee;
        uint256 buyAmountOut;
        uint256 finalAmountOut;
    }

    OnchainStateLens public lens;
    UniswapV3ArbitrageSource public source;

    function setUp() public {
        uint256 forkId = vm.createFork("https://ethereum-sepolia-rpc.publicnode.com");
        vm.selectFork(forkId);

        require(SWAP_ROUTER.code.length > 0, "ROUTER_NO_CODE");
        require(WETH.code.length > 0, "WETH_NO_CODE");
        require(USDC.code.length > 0, "USDC_NO_CODE");
        require(USDC_WETH_POOL_A.code.length > 0, "POOL_A_NO_CODE");
        require(USDC_WETH_POOL_B.code.length > 0, "POOL_B_NO_CODE");
        require(USDC_WETH_POOL_C.code.length > 0, "POOL_C_NO_CODE");
        require(USDC_WETH_POOL_D.code.length > 0, "POOL_D_NO_CODE");

        lens = new OnchainStateLens();
        source = new UniswapV3ArbitrageSource(SWAP_ROUTER, WETH, "fork-amm-arb");
        source.setRouter(address(this));

        vm.deal(address(this), 5 ether);
        IWETHFork(WETH).deposit{value: 5 ether}();
        bool ok = IERC20(WETH).transfer(address(source), 5 ether);
        require(ok, "FUND_SOURCE_FAILED");
    }

    function test_Fork_RevertWhenContextHashMismatch() public {
        UniswapV3ArbitrageSource.ArbitrageParams memory p = _baseParams(0.01 ether);
        p.expectedContextHash = bytes32(uint256(111));

        vm.expectRevert(UniswapV3ArbitrageSource.ContextMismatch.selector);
        source.execute(abi.encode(p), bytes32(uint256(222)), p.amountIn);
    }

    function test_Fork_RevertWhenPoolMismatch() public {
        UniswapV3ArbitrageSource.ArbitrageParams memory p = _baseParams(0.01 ether);
        address expectedPool = p.buyPool;
        p.buyPool = address(0x1234);

        vm.expectRevert(
            abi.encodeWithSelector(UniswapV3ArbitrageSource.InvalidPool.selector, expectedPool, address(0x1234))
        );
        source.execute(abi.encode(p), p.expectedContextHash, p.amountIn);
    }

    function test_Fork_ExecuteProfitPath() public {
        _skewPoolForArbitrage();

        uint256 callerBalanceBefore = IERC20(WETH).balanceOf(address(this));

        uint256[5] memory amountCandidates = [
            uint256(0.005 ether),
            uint256(0.01 ether),
            uint256(0.02 ether),
            uint256(0.05 ether),
            uint256(0.1 ether)
        ];

        bool found;
        int256 bestPnl;
        uint256 bestReturned;
        uint256 bestAmountIn;

        for (uint256 i = 0; i < amountCandidates.length; i++) {
            uint256 amountIn = amountCandidates[i];
            BestRoute memory route = _bestRoute(amountIn);

            if (route.buyPool == address(0) || route.sellPool == address(0)) {
                continue;
            }

            uint256 snap = vm.snapshotState();

            UniswapV3ArbitrageSource.ArbitrageParams memory p = UniswapV3ArbitrageSource.ArbitrageParams({
                expectedContextHash: keccak256("fork-context"),
                buyPool: route.buyPool,
                sellPool: route.sellPool,
                tokenIn: WETH,
                tokenMid: USDC,
                buyFee: route.buyFee,
                sellFee: route.sellFee,
                amountIn: amountIn,
                minBuyAmountOut: 1,
                minFinalAmountOut: 1,
                deadline: block.timestamp + 1 hours
            });

            try source.execute(abi.encode(p), p.expectedContextHash, p.amountIn) returns (int256 pnl, uint256 returnedAssets) {
                if (pnl > 0) {
                    found = true;
                    bestPnl = pnl;
                    bestReturned = returnedAssets;
                    bestAmountIn = amountIn;
                    break;
                }
            } catch {
                // 若路徑在真實池上失敗，回滾後嘗試下一組
            }

            vm.revertToState(snap);
        }

        require(found, "NO_REAL_PROFIT_PATH");

        uint256 callerBalanceAfter = IERC20(WETH).balanceOf(address(this));
        assertEq(callerBalanceAfter - callerBalanceBefore, bestReturned);
        assertEq(bestReturned, uint256(bestPnl) + bestAmountIn, "returnedAssets should equal amountIn + pnl");
    }

    function _skewPoolForArbitrage() internal {
        uint24 feeA = IUniswapV3PoolFee(USDC_WETH_POOL_A).fee();

        vm.deal(address(this), 200 ether);
        IWETHFork(WETH).deposit{value: 100 ether}();

        IERC20(WETH).approve(SWAP_ROUTER, type(uint256).max);

        ISwapRouterSkew(SWAP_ROUTER).exactInputSingle(
            ISwapRouterSkew.ExactInputSingleParams({
                tokenIn: WETH,
                tokenOut: USDC,
                fee: feeA,
                recipient: address(this),
                amountIn: 50 ether,
                amountOutMinimum: 1,
                sqrtPriceLimitX96: 0
            })
        );
    }

    function _baseParams(uint256 amountIn)
        internal
        view
        returns (UniswapV3ArbitrageSource.ArbitrageParams memory p)
    {
        p.expectedContextHash = keccak256("fork-context");
        p.buyPool = USDC_WETH_POOL_A;
        p.sellPool = USDC_WETH_POOL_B;
        p.tokenIn = WETH;
        p.tokenMid = USDC;
        p.buyFee = IUniswapV3PoolFee(USDC_WETH_POOL_A).fee();
        p.sellFee = IUniswapV3PoolFee(USDC_WETH_POOL_B).fee();
        p.amountIn = amountIn;
        p.minBuyAmountOut = 1;
        p.minFinalAmountOut = amountIn;
        p.deadline = block.timestamp + 1 hours;
    }

    function _bestRoute(uint256 amountIn) internal view returns (BestRoute memory best) {
        address[4] memory pools = [
            USDC_WETH_POOL_A,
            USDC_WETH_POOL_B,
            USDC_WETH_POOL_C,
            USDC_WETH_POOL_D
        ];

        for (uint256 i = 0; i < pools.length; i++) {
            OnchainStateLens.V3PoolSnapshot memory buySnapshot =
                lens.getV3PoolSnapshot(pools[i], WETH, USDC, amountIn);

            if (buySnapshot.quoteAmountOut == 0) {
                continue;
            }

            for (uint256 j = 0; j < pools.length; j++) {
                if (i == j) {
                    continue;
                }

                OnchainStateLens.V3PoolSnapshot memory sellSnapshot =
                    lens.getV3PoolSnapshot(pools[j], USDC, WETH, buySnapshot.quoteAmountOut);

                if (sellSnapshot.quoteAmountOut > best.finalAmountOut) {
                    best.buyPool = pools[i];
                    best.sellPool = pools[j];
                    best.buyFee = IUniswapV3PoolFee(pools[i]).fee();
                    best.sellFee = IUniswapV3PoolFee(pools[j]).fee();
                    best.buyAmountOut = buySnapshot.quoteAmountOut;
                    best.finalAmountOut = sellSnapshot.quoteAmountOut;
                }
            }
        }
    }
}
