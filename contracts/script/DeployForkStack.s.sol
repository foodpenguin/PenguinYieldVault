// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Script, console2} from "forge-std/Script.sol";
import {IERC20} from "openzeppelin-contracts/contracts/token/ERC20/IERC20.sol";

import {AtomicExecutor} from "../src/AtomicExecutor.sol";
import {BotRegistry} from "../src/BotRegistry.sol";
import {OnchainStateLens} from "../src/OnchainStateLens.sol";
import {StrategyRouter} from "../src/StrategyRouter.sol";
import {UniswapV3ArbitrageSource} from "../src/UniswapV3ArbitrageSource.sol";
import {UniswapV3LPStrategy} from "../src/UniswapV3LPStrategy.sol";
import {Vault} from "../src/Vault.sol";

interface IWETHDeploy {
    function deposit() external payable;
}

contract DeployForkStack is Script {
    address internal constant WETH = 0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14;
    address internal constant SWAP_ROUTER = 0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E;

    function run() external {
        address keeper = vm.envAddress("KEEPER_ADDRESS");
        uint256 seedWei = vm.envOr("VAULT_SEED_WEI", uint256(5 ether));
        uint256 minIdleBpsRaw = vm.envOr("MIN_IDLE_BPS", uint256(500));
        address lpPool = vm.envAddress("LP_POOL_ADDRESS");
        address positionManager = vm.envAddress("POSITION_MANAGER_ADDRESS");
        uint256 sourceCapBpsRaw = vm.envOr("SOURCE_CAP_BPS", uint256(1000));
        uint256 lpCapBpsRaw = vm.envOr("LP_CAP_BPS", uint256(9000));

        require(minIdleBpsRaw <= 10_000, "INVALID_MIN_IDLE_BPS");
        require(sourceCapBpsRaw <= 10_000, "INVALID_SOURCE_CAP_BPS");
        require(lpCapBpsRaw <= 10_000, "INVALID_LP_CAP_BPS");
        require(sourceCapBpsRaw + lpCapBpsRaw <= 10_000, "CAP_BPS_SUM_EXCEEDED");

        vm.startBroadcast();

        Vault vault = new Vault(WETH, "Penguin Yield Eth Vault Share", "pyETH");
        BotRegistry registry = new BotRegistry();
        StrategyRouter router = new StrategyRouter(address(registry), address(vault));
        OnchainStateLens lens = new OnchainStateLens();
        AtomicExecutor executor = new AtomicExecutor(address(router), address(lens));
        UniswapV3ArbitrageSource source = new UniswapV3ArbitrageSource(SWAP_ROUTER, WETH, "amm-arbitrage");
        UniswapV3LPStrategy lpStrategy = new UniswapV3LPStrategy(lpPool, positionManager, SWAP_ROUTER, WETH);

        vault.setRouter(address(router));
        vault.setMinIdleBps(uint16(minIdleBpsRaw));
        vault.setStrategyCapBps(address(source), uint16(sourceCapBpsRaw));
        vault.setStrategyCapBps(address(lpStrategy), uint16(lpCapBpsRaw));
        source.setRouter(address(router));
        lpStrategy.setRouter(address(router));
        router.setExecutor(address(executor), true);
        router.setExecutorSource(address(executor), address(source), true);
        router.setExecutorSource(address(executor), address(lpStrategy), true);
        router.setManagedOnlySource(address(lpStrategy), true);
        executor.setKeeper(keeper, true);
        registry.addSource(address(source));
        registry.addSource(address(lpStrategy));

        IWETHDeploy(WETH).deposit{value: seedWei}();
        bool approved = IERC20(WETH).approve(address(vault), seedWei);
        require(approved, "SEED_APPROVE_FAILED");
        vault.deposit(seedWei, msg.sender);

        vm.stopBroadcast();

        console2.log("vault:", address(vault));
        console2.log("registry:", address(registry));
        console2.log("router:", address(router));
        console2.log("lens:", address(lens));
        console2.log("executor:", address(executor));
        console2.log("source:", address(source));
        console2.log("lpStrategy:", address(lpStrategy));
        console2.log("keeper:", keeper);
        console2.log("vaultSeedWei:", seedWei);
        console2.log("minIdleBps:", minIdleBpsRaw);
        console2.log("sourceCapBps:", sourceCapBpsRaw);
        console2.log("lpCapBps:", lpCapBpsRaw);
    }
}
