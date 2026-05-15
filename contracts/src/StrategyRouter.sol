// SPDX-License-Identifier: MIT
pragma solidity ^0.8.13;

import {BotRegistry} from "./BotRegistry.sol";
import {IProfitSource} from "./interfaces/IProfitSource.sol";
import {Vault} from "./Vault.sol";
import {IERC20} from "openzeppelin-contracts/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "openzeppelin-contracts/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "openzeppelin-contracts/contracts/utils/ReentrancyGuard.sol";

/// @title Strategy router
/// @notice Receives bot requests, allocates Vault capital, executes source, and settles P&L
contract StrategyRouter is ReentrancyGuard {
    using SafeERC20 for IERC20;

    uint16 internal constant BPS_DENOMINATOR = 10_000;

    address public owner;
    BotRegistry public immutable registry;
    Vault public immutable vault;
    bool public paused;
    bool public haltOnLossLimitBreach;

    mapping(address => bool) public executors;
    mapping(address => mapping(address => bool)) public executorSourceAllowed;
    mapping(address => bool) public managedOnlySource;

    event OwnerTransferred(address indexed oldOwner, address indexed newOwner);
    event ExecutorUpdated(address indexed executor, bool allowed);
    event ExecutorSourceUpdated(address indexed executor, address indexed source, bool allowed);
    event ManagedOnlySourceUpdated(address indexed source, bool managedOnly);
    event HaltOnLossLimitBreachUpdated(bool enabled);
    event PausedUpdated(bool paused);
    event LossLimitBreached(address indexed source, uint256 principal, uint256 loss, uint256 maxAllowedLoss, bytes32 executionContextHash);
    event StrategyExecuted(
        address indexed source,
        int256 realizedPnl,
        uint256 amountIn,
        uint256 returnedAssets,
        uint256 profit,
        uint256 loss,
        bytes32 executionContextHash
    );
    event StrategyManaged(
        address indexed source,
        int256 realizedPnl,
        uint256 principalSettled,
        uint256 returnedAssets,
        uint256 profit,
        uint256 loss,
        bytes32 executionContextHash
    );

    modifier onlyOwner() {
        require(msg.sender == owner, "NOT_OWNER");
        _;
    }

    modifier onlyExecutor() {
        require(executors[msg.sender], "NOT_EXECUTOR");
        _;
    }

    constructor(address registry_, address vault_) {
        require(registry_ != address(0) && vault_ != address(0), "ZERO_ADDRESS");
        owner = msg.sender;
        registry = BotRegistry(registry_);
        vault = Vault(payable(vault_));
    }

    function transferOwnership(address newOwner) external onlyOwner {
        require(newOwner != address(0), "ZERO_ADDRESS");
        emit OwnerTransferred(owner, newOwner);
        owner = newOwner;
    }

    /// @notice Set executor allowlist
    function setExecutor(address executor, bool allowed) external onlyOwner {
        require(executor != address(0), "ZERO_EXECUTOR");
        executors[executor] = allowed;
        emit ExecutorUpdated(executor, allowed);
    }

    /// @notice Scope executor to specific sources
    function setExecutorSource(address executor, address source, bool allowed) external onlyOwner {
        require(executor != address(0), "ZERO_EXECUTOR");
        require(source != address(0), "ZERO_SOURCE");
        executorSourceAllowed[executor][source] = allowed;
        emit ExecutorSourceUpdated(executor, source, allowed);
    }

    /// @notice Mark source as managed-only (no capital allocation path)
    function setManagedOnlySource(address source, bool managedOnly_) external onlyOwner {
        require(source != address(0), "ZERO_SOURCE");
        managedOnlySource[source] = managedOnly_;
        emit ManagedOnlySourceUpdated(source, managedOnly_);
    }

    /// @notice Auto-halt on loss limit breach toggle
    function setHaltOnLossLimitBreach(bool enabled) external onlyOwner {
        haltOnLossLimitBreach = enabled;
        emit HaltOnLossLimitBreachUpdated(enabled);
    }

    /// @notice Emergency pause toggle
    function setPaused(bool paused_) external onlyOwner {
        paused = paused_;
        emit PausedUpdated(paused_);
    }

    /// @notice Execute source: allocate capital → execute → settle
    /// @dev Actual return computed from Router balance delta to prevent source spoofing
    function executeStrategy(
        address source,
        bytes calldata params,
        bytes32 executionContextHash,
        uint256 amountIn,
        uint16 maxLossBps,
        uint256 minReturnedAssets
    )
        external
        onlyExecutor
        nonReentrant
        returns (int256 realizedPnl, uint256 profit, uint256 loss, uint256 returnedAssets)
    {
        _validateExecutorScope(source);
        IERC20 assetToken = _validateAndAllocate(source, amountIn, maxLossBps);

        uint256 received;
        (realizedPnl, returnedAssets, received) = _executeAndCollect(
            source,
            params,
            executionContextHash,
            amountIn,
            assetToken
        );

        require(received >= minReturnedAssets, "SLIPPAGE_RETURN");

        (profit, loss) = vault.settleStrategy(source, amountIn, received);
        _handleLossLimit(source, executionContextHash, amountIn, maxLossBps, loss);

        emit StrategyExecuted(source, realizedPnl, amountIn, returnedAssets, profit, loss, executionContextHash);
    }

    /// @notice Execute managed strategy (no capital allocation), e.g. LP rebalance/withdraw
    function executeManagedStrategy(
        address source,
        bytes calldata params,
        bytes32 executionContextHash,
        uint256 principalToSettle,
        uint16 maxLossBps,
        uint256 minReturnedAssets
    )
        external
        onlyExecutor
        nonReentrant
        returns (int256 realizedPnl, uint256 returnedAssets, uint256 profit, uint256 loss)
    {
        _validateExecutorScope(source);
        IERC20 assetToken = _validateSource(source);
        require(maxLossBps <= BPS_DENOMINATOR, "INVALID_MAX_LOSS_BPS");

        uint256 received;
        (realizedPnl, returnedAssets, received) = _executeAndCollect(
            source,
            params,
            executionContextHash,
            0,
            assetToken
        );

        require(received >= minReturnedAssets, "SLIPPAGE_RETURN");
        (profit, loss) = vault.settleStrategy(source, principalToSettle, received);
        _handleLossLimit(source, executionContextHash, principalToSettle, maxLossBps, loss);

        emit StrategyManaged(source, realizedPnl, principalToSettle, returnedAssets, profit, loss, executionContextHash);
    }

    /// @notice Allocate capital and execute managed strategy without forcing full settlement
    function allocateAndExecuteManagedStrategy(
        address source,
        bytes calldata params,
        bytes32 executionContextHash,
        uint256 amountToAllocate,
        uint256 principalToSettle,
        uint16 maxLossBps,
        uint256 minReturnedAssets
    )
        external
        onlyExecutor
        nonReentrant
        returns (int256 realizedPnl, uint256 returnedAssets, uint256 profit, uint256 loss)
    {
        _validateExecutorScope(source);
        IERC20 assetToken = _validateAndAllocate(source, amountToAllocate, maxLossBps);

        uint256 received;
        (realizedPnl, returnedAssets, received) = _executeAndCollect(
            source,
            params,
            executionContextHash,
            amountToAllocate,
            assetToken
        );

        require(received >= minReturnedAssets, "SLIPPAGE_RETURN");
        (profit, loss) = vault.settleStrategy(source, principalToSettle, received);
        _handleLossLimit(source, executionContextHash, principalToSettle, maxLossBps, loss);

        emit StrategyManaged(source, realizedPnl, principalToSettle, returnedAssets, profit, loss, executionContextHash);
    }

    function _validateAndAllocate(address source, uint256 amountIn, uint16 maxLossBps)
        internal
        returns (IERC20 assetToken)
    {
        assetToken = _validateSource(source);
        require(!managedOnlySource[source], "MANAGED_ONLY_SOURCE");
        require(amountIn > 0, "ZERO_AMOUNT_IN");
        require(maxLossBps <= BPS_DENOMINATOR, "INVALID_MAX_LOSS_BPS");

        vault.allocateToStrategy(source, amountIn);
    }

    function _validateExecutorScope(address source) internal view {
        require(executorSourceAllowed[msg.sender][source], "EXECUTOR_SOURCE_FORBIDDEN");
    }

    function _validateSource(address source) internal view returns (IERC20 assetToken) {
        require(!paused, "PAUSED");
        require(registry.exists(source), "SOURCE_NOT_EXISTS");
        require(registry.active(source), "SOURCE_DISABLED");
        require(IProfitSource(source).isActive(), "SOURCE_INACTIVE");

        address vaultAsset = vault.asset();
        require(IProfitSource(source).asset() == vaultAsset, "ASSET_MISMATCH");

        assetToken = IERC20(vaultAsset);
    }

    function _executeAndCollect(
        address source,
        bytes calldata params,
        bytes32 executionContextHash,
        uint256 amountIn,
        IERC20 assetToken
    )
        internal
        returns (int256 realizedPnl, uint256 returnedAssets, uint256 received)
    {
        uint256 routerBefore = assetToken.balanceOf(address(this));
        uint256 vaultBefore = assetToken.balanceOf(address(vault));

        (realizedPnl, returnedAssets) = IProfitSource(source).execute(params, executionContextHash, amountIn);

        uint256 routerAfter = assetToken.balanceOf(address(this));
        uint256 vaultAfter = assetToken.balanceOf(address(vault));

        require(routerAfter >= routerBefore, "NEGATIVE_ROUTER_DELTA");
        require(vaultAfter >= vaultBefore, "NEGATIVE_VAULT_DELTA");

        uint256 receivedInRouter;
        uint256 receivedDirectToVault;
        unchecked {
            receivedInRouter = routerAfter - routerBefore;
            receivedDirectToVault = vaultAfter - vaultBefore;
            received = receivedInRouter + receivedDirectToVault;
        }

        require(received == returnedAssets, "RETURN_MISMATCH");

        if (receivedInRouter > 0) {
            assetToken.safeTransfer(address(vault), receivedInRouter);
        }

        uint256 vaultFinal = assetToken.balanceOf(address(vault));
        require(vaultFinal >= vaultBefore, "VAULT_BALANCE_UNDERFLOW");

        uint256 vaultDelta;
        unchecked {
            vaultDelta = vaultFinal - vaultBefore;
        }
        require(vaultDelta == received, "VAULT_CREDIT_MISMATCH");
    }

    function _handleLossLimit(
        address source,
        bytes32 executionContextHash,
        uint256 principal,
        uint16 maxLossBps,
        uint256 loss
    ) internal {
        if (loss == 0 || principal == 0) {
            return;
        }

        uint256 maxLoss = (principal * uint256(maxLossBps)) / BPS_DENOMINATOR;
        if (loss > maxLoss) {
            emit LossLimitBreached(source, principal, loss, maxLoss, executionContextHash);

            if (haltOnLossLimitBreach && !paused) {
                paused = true;
                emit PausedUpdated(true);
            }
        }
    }
}
