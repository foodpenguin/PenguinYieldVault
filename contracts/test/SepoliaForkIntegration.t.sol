// SPDX-License-Identifier: UNLICENSED
pragma solidity ^0.8.20;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "openzeppelin-contracts/contracts/token/ERC20/IERC20.sol";

import {Vault} from "../src/Vault.sol";
import {SepoliaV3PoolLens} from "../src/SepoliaV3PoolLens.sol";

interface IWETHLike {
    function deposit() external payable;
}

contract SepoliaForkIntegrationTest is Test {
    // 使用者指定：Sepolia 可監控的 Uniswap 路由地址
    address internal constant SEPOLIA_UNISWAP_ROUTER = 0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E;

    // 使用者指定：真實 token 地址
    address internal constant WETH = 0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14;
    address internal constant USDC = 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238;
    address internal constant UNI = 0x1f9840a85d5aF5bf1D1762F925BDADdC4201F984;

    SepoliaV3PoolLens public lens;

    function setUp() public {
        uint256 forkId = vm.createFork("https://ethereum-sepolia-rpc.publicnode.com");
        vm.selectFork(forkId);

        lens = new SepoliaV3PoolLens(SEPOLIA_UNISWAP_ROUTER);
    }

    function test_Fork_GivenAddressesHaveCode() public view {
        _assertHasCode(SEPOLIA_UNISWAP_ROUTER, "ROUTER_NO_CODE");
        _assertHasCode(WETH, "WETH_NO_CODE");
        _assertHasCode(USDC, "USDC_NO_CODE");
        _assertHasCode(UNI, "UNI_NO_CODE");

        address[] memory uniWethPools = _uniWethPools();
        address[] memory usdcWethPools = _usdcWethPools();

        for (uint256 i = 0; i < uniWethPools.length; i++) {
            _assertHasCode(uniWethPools[i], "UNI_WETH_POOL_NO_CODE");
        }

        for (uint256 i = 0; i < usdcWethPools.length; i++) {
            _assertHasCode(usdcWethPools[i], "USDC_WETH_POOL_NO_CODE");
        }

        assertTrue(lens.routerHasCode());
    }

    function test_Fork_Vault4626_DepositRedeemWithRealWETH() public {
        Vault vault = new Vault(WETH, "MEV Vault Share", "mvSHARE");

        address alice = makeAddr("alice");
        vm.deal(alice, 3 ether);

        vm.startPrank(alice);
        IWETHLike(WETH).deposit{value: 2 ether}();
        IERC20(WETH).approve(address(vault), 2 ether);

        uint256 expectedShares = vault.previewDeposit(1 ether);
        uint256 mintedShares = vault.deposit(1 ether, alice);
        assertEq(mintedShares, expectedShares);
        assertEq(vault.balanceOf(alice), mintedShares);

        uint256 assetsOut = vault.redeem(mintedShares / 2, alice, alice);
        assertGt(assetsOut, 0);
        vm.stopPrank();

        assertEq(vault.totalAssets(), IERC20(WETH).balanceOf(address(vault)));
    }

    function test_Fork_Vault_DepositETHWrapsToWETH() public {
        Vault vault = new Vault(WETH, "MEV Vault Share", "mvSHARE");

        address bob = makeAddr("bob");
        vm.deal(bob, 2 ether);

        vm.prank(bob);
        uint256 shares = vault.depositETH{value: 1 ether}(bob);

        assertGt(shares, 0);
        assertEq(IERC20(WETH).balanceOf(address(vault)), 1 ether);
    }

    function test_Fork_CanScanUNIWETHPoolsForSpread() public view {
        address[] memory pools = _uniWethPools();
        (SepoliaV3PoolLens.PoolQuote[] memory quotes, uint256 minQuote, uint256 maxQuote, uint256 spreadBps) =
            lens.scanPools(pools, WETH, 1 ether);

        assertEq(quotes.length, 4);
        assertGt(minQuote, 0);
        assertGe(maxQuote, minQuote);

        for (uint256 i = 0; i < quotes.length; i++) {
            assertEq(quotes[i].tokenOut, UNI);
        }

        if (maxQuote > minQuote) {
            assertEq(spreadBps, ((maxQuote - minQuote) * 10_000) / minQuote);
        } else {
            assertEq(spreadBps, 0);
        }
    }

    function test_Fork_CanScanUSDCWETHPoolsForSpread() public view {
        address[] memory pools = _usdcWethPools();
        (SepoliaV3PoolLens.PoolQuote[] memory quotes, uint256 minQuote, uint256 maxQuote, uint256 spreadBps) =
            lens.scanPools(pools, WETH, 1 ether);

        assertEq(quotes.length, 4);
        assertGt(minQuote, 0);
        assertGe(maxQuote, minQuote);

        for (uint256 i = 0; i < quotes.length; i++) {
            assertEq(quotes[i].tokenOut, USDC);
        }

        if (maxQuote > minQuote) {
            assertEq(spreadBps, ((maxQuote - minQuote) * 10_000) / minQuote);
        } else {
            assertEq(spreadBps, 0);
        }
    }

    function _assertHasCode(address target, string memory err) internal view {
        assertGt(target.code.length, 0, err);
    }

    function _uniWethPools() internal pure returns (address[] memory pools) {
        pools = new address[](4);
        pools[0] = 0x287B0e934ed0439E2a7b1d5F0FC25eA2c24b64f7;
        pools[1] = 0x51aDC79e7760aC5317a0d05e7a64c4f9cB2d4369;
        pools[2] = 0x224Cc4e5b50036108C1d862442365054600c260C;
        pools[3] = 0xb8b672bdd9cFF3D0979e7344c7358CA12E78a1F0;
    }

    function _usdcWethPools() internal pure returns (address[] memory pools) {
        pools = new address[](4);
        pools[0] = 0x3289680dD4d6C10bb19b899729cda5eEF58AEfF1;
        pools[1] = 0x6Ce0896eAE6D4BD668fDe41BB784548fb8F59b50;
        pools[2] = 0xFeEd501c2B21D315F04946F85fC6416B640240b5;
        pools[3] = 0x6418EEC70f50913ff0d756B48d32Ce7C02b47C47;
    }
}
