// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Ownable} from "openzeppelin-contracts/contracts/access/Ownable.sol";
import {ERC20} from "openzeppelin-contracts/contracts/token/ERC20/ERC20.sol";
import {IERC20} from "openzeppelin-contracts/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "openzeppelin-contracts/contracts/token/ERC20/utils/SafeERC20.sol";
import {ERC4626} from "openzeppelin-contracts/contracts/token/ERC20/extensions/ERC4626.sol";
import {ReentrancyGuard} from "openzeppelin-contracts/contracts/utils/ReentrancyGuard.sol";
import {Math} from "openzeppelin-contracts/contracts/utils/math/Math.sol";



interface IWETHMinimal {
    function deposit() external payable;
    function withdraw(uint256 amount) external;
}

/// @title ERC-4626 Vault with WETH ETH wrappers
contract Vault is ERC4626, Ownable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    uint16 public constant BPS_DENOMINATOR = 10_000;

    address public router;
    address public feeRecipient;

    uint16 public minIdleBps;
    uint16 public performanceFeeBps;
    uint16 public pnlSmoothingBps;

    uint256 public totalReportedProfit;
    uint256 public totalReportedLoss;
    uint256 public totalStrategyDebt;
    uint256 public totalGrossProfit;
    uint256 public totalFeeAssetsAccrued;
    mapping(address => uint256) public strategyPendingLoss;

    int256 public smoothedPnl;

    mapping(address => uint256) public strategyDebt;
    mapping(address => uint16) public strategyCapBps;
    mapping(address => bool) public strategyCapConfigured;
    mapping(address => bool) public strategyExists;
    mapping(address => bool) public strategyActive;
    mapping(address => uint256) public strategyRecallRequest;

    address[] private _strategies;

    event RouterUpdated(address indexed oldRouter, address indexed newRouter);
    event FeeRecipientUpdated(address indexed oldRecipient, address indexed newRecipient);
    event MinIdleBpsUpdated(uint16 oldBps, uint16 newBps);
    event PerformanceFeeBpsUpdated(uint16 oldBps, uint16 newBps);
    event PnlSmoothingBpsUpdated(uint16 oldBps, uint16 newBps);

    event StrategyRegistered(address indexed strategy);
    event StrategyActiveUpdated(address indexed strategy, bool active);
    event StrategyCapBpsUpdated(address indexed strategy, uint16 oldBps, uint16 newBps);
    event StrategyRecallRequested(address indexed strategy, uint256 requestedAssets);
    event StrategyRecallReduced(address indexed strategy, uint256 fulfilledAssets, uint256 remainingAssets);

    event CapitalAllocated(address indexed strategy, uint256 amount, uint256 strategyDebtAfter, uint256 totalStrategyDebtAfter);
    event StrategySettled(
        address indexed strategy,
        uint256 principal,
        uint256 returnedAssets,
        uint256 profit,
        uint256 loss,
        uint256 strategyDebtAfter,
        uint256 totalStrategyDebtAfter
    );
    event StrategySettledDetails(
        address indexed strategy,
        uint256 grossProfit,
        uint256 netProfit,
        uint256 feeAssets,
        uint256 lossOffset,
        uint256 pendingLossAfter
    );
    event PerformanceFeeMinted(address indexed strategy, address indexed recipient, uint256 feeAssets, uint256 mintedShares);
    event SmoothedPnlUpdated(int256 oldValue, int256 newValue, int256 samplePnl);

    modifier onlyRouter() {
        require(msg.sender == router, "NOT_ROUTER");
        _;
    }

    constructor(address asset_, string memory name_, string memory symbol_)
        ERC20(name_, symbol_)
        ERC4626(IERC20(asset_))
        Ownable(msg.sender)
    {
        require(asset_ != address(0), "ZERO_ASSET");
        minIdleBps = 500;
        performanceFeeBps = 250;
        pnlSmoothingBps = 2_000;
        feeRecipient = msg.sender;
    }



    /// @notice Deposit native ETH (auto-wraps to WETH)
    function depositETH(address receiver) external payable nonReentrant returns (uint256 shares) {
        require(receiver != address(0), "ZERO_RECEIVER");

        uint256 assets = msg.value;
        require(assets > 0, "ZERO_ASSETS");
        require(assets <= maxDeposit(receiver), "MAX_DEPOSIT_EXCEEDED");

        shares = previewDeposit(assets);
        require(shares > 0, "ZERO_SHARES");

        IWETHMinimal(asset()).deposit{value: assets}();
        _mint(receiver, shares);

        emit Deposit(msg.sender, receiver, assets, shares);
    }

    /// @notice Withdraw specified assets as native ETH
    function withdrawETH(uint256 assets, address receiver, address owner_)
        external
        nonReentrant
        returns (uint256 shares)
    {
        require(receiver != address(0), "ZERO_RECEIVER");
        require(assets > 0, "ZERO_ASSETS");
        require(assets <= idleAssets(), "INSUFFICIENT_IDLE");
        require(assets <= super.maxWithdraw(owner_), "MAX_WITHDRAW_EXCEEDED");

        shares = previewWithdraw(assets);
        if (msg.sender != owner_) {
            _spendAllowance(owner_, msg.sender, shares);
        }

        _burn(owner_, shares);
        IWETHMinimal(asset()).withdraw(assets);
        _safeTransferETH(receiver, assets);

        emit Withdraw(msg.sender, receiver, owner_, assets, shares);
    }

    /// @notice Burn shares and receive native ETH
    function redeemETH(uint256 shares, address receiver, address owner_)
        external
        nonReentrant
        returns (uint256 assets)
    {
        require(receiver != address(0), "ZERO_RECEIVER");
        require(shares > 0, "ZERO_SHARES");
        uint256 idleShares = _convertToShares(idleAssets(), Math.Rounding.Floor);
        require(shares <= idleShares, "INSUFFICIENT_IDLE");
        require(shares <= super.maxRedeem(owner_), "MAX_REDEEM_EXCEEDED");

        assets = previewRedeem(shares);
        if (msg.sender != owner_) {
            _spendAllowance(owner_, msg.sender, shares);
        }

        _burn(owner_, shares);
        IWETHMinimal(asset()).withdraw(assets);
        _safeTransferETH(receiver, assets);

        emit Withdraw(msg.sender, receiver, owner_, assets, shares);
    }

    /// @notice Idle asset balance available for redemption or allocation
    function idleAssets() public view returns (uint256) {
        return IERC20(asset()).balanceOf(address(this));
    }

    /// @notice Max allocatable to a strategy (bounded by cap and idle reserve)
    function availableForStrategy(address strategy) public view returns (uint256) {
        if (strategy == address(0) || !strategyExists[strategy] || !strategyActive[strategy]) {
            return 0;
        }

        if (strategyRecallRequest[strategy] > 0) {
            return 0;
        }

        uint256 tvl = totalAssets();
        uint256 idle = idleAssets();
        uint256 minIdleAssets = (tvl * minIdleBps) / 10_000;
        if (idle <= minIdleAssets) {
            return 0;
        }

        uint256 byIdle = idle - minIdleAssets;
        uint256 capAssets = (tvl * _strategyCapBps(strategy)) / 10_000;
        uint256 currentDebt = strategyDebt[strategy];
        if (capAssets <= currentDebt) {
            return 0;
        }

        uint256 byCap = capAssets - currentDebt;
        return byCap < byIdle ? byCap : byIdle;
    }

    /// @notice Router allocates principal to strategy
    function allocateToStrategy(address strategy, uint256 assets) external onlyRouter nonReentrant {
        require(strategy != address(0), "ZERO_STRATEGY");
        require(strategyExists[strategy], "STRATEGY_NOT_REGISTERED");
        require(strategyActive[strategy], "STRATEGY_INACTIVE");
        require(strategyRecallRequest[strategy] == 0, "STRATEGY_RECALL_PENDING");
        require(assets > 0, "ZERO_ASSETS");
        require(assets <= availableForStrategy(strategy), "ALLOCATION_EXCEEDS_LIMIT");

        uint256 newDebt = strategyDebt[strategy] + assets;
        strategyDebt[strategy] = newDebt;
        totalStrategyDebt += assets;

        IERC20(asset()).safeTransfer(strategy, assets);

        emit CapitalAllocated(strategy, assets, newDebt, totalStrategyDebt);
    }

    /// @notice Settle strategy: reconcile principal and P&L after execution
    function settleStrategy(address strategy, uint256 principal, uint256 returnedAssets)
        external
        onlyRouter
        nonReentrant
        returns (uint256 profit, uint256 loss)
    {
        require(strategy != address(0), "ZERO_STRATEGY");
        require(strategyExists[strategy], "STRATEGY_NOT_REGISTERED");

        uint256 currentDebt = strategyDebt[strategy];
        require(currentDebt >= principal, "SETTLE_GT_DEBT");

        if (principal > 0) {
            strategyDebt[strategy] = currentDebt - principal;
            totalStrategyDebt -= principal;
            _consumeRecallRequest(strategy, principal);
        }

        uint256 grossProfit;
        uint256 lossOffset;
        uint256 feeAssets;
        uint256 netProfitAfterOffset;

        if (returnedAssets >= principal) {
            grossProfit = returnedAssets - principal;
            totalGrossProfit += grossProfit;

            netProfitAfterOffset = grossProfit;
            uint256 sPendingLoss = strategyPendingLoss[strategy];
            if (sPendingLoss > 0 && netProfitAfterOffset > 0) {
                lossOffset = sPendingLoss < netProfitAfterOffset ? sPendingLoss : netProfitAfterOffset;
                strategyPendingLoss[strategy] = sPendingLoss - lossOffset;
                netProfitAfterOffset -= lossOffset;
            }

            if (netProfitAfterOffset > 0) {
                (feeAssets,) = _mintPerformanceFee(strategy, netProfitAfterOffset);
                profit = netProfitAfterOffset - feeAssets;
                totalReportedProfit += profit;
            }
        } else {
            loss = principal - returnedAssets;
            totalReportedLoss += loss;
            strategyPendingLoss[strategy] += loss;
        }

        _updateSmoothedPnl(principal, returnedAssets);

        emit StrategySettled(strategy, principal, returnedAssets, profit, loss, strategyDebt[strategy], totalStrategyDebt);
        emit StrategySettledDetails(strategy, grossProfit, profit, feeAssets, lossOffset, strategyPendingLoss[strategy]);
    }

    function setRouter(address newRouter) external onlyOwner {
        require(newRouter != address(0), "ZERO_ROUTER");
        emit RouterUpdated(router, newRouter);
        router = newRouter;
    }

    function setFeeRecipient(address newRecipient) external onlyOwner {
        require(newRecipient != address(0), "ZERO_FEE_RECIPIENT");
        emit FeeRecipientUpdated(feeRecipient, newRecipient);
        feeRecipient = newRecipient;
    }

    function setMinIdleBps(uint16 newBps) external onlyOwner {
        require(newBps <= BPS_DENOMINATOR, "INVALID_BPS");
        emit MinIdleBpsUpdated(minIdleBps, newBps);
        minIdleBps = newBps;
    }

    function setPerformanceFeeBps(uint16 newBps) external onlyOwner {
        require(newBps <= BPS_DENOMINATOR, "INVALID_BPS");
        emit PerformanceFeeBpsUpdated(performanceFeeBps, newBps);
        performanceFeeBps = newBps;
    }

    function setPnlSmoothingBps(uint16 newBps) external onlyOwner {
        require(newBps <= BPS_DENOMINATOR, "INVALID_BPS");
        emit PnlSmoothingBpsUpdated(pnlSmoothingBps, newBps);
        pnlSmoothingBps = newBps;
    }

    function setStrategy(address strategy, bool active_, uint16 capBps_) external onlyOwner {
        require(strategy != address(0), "ZERO_STRATEGY");
        require(capBps_ <= BPS_DENOMINATOR, "INVALID_BPS");

        _registerStrategyIfNeeded(strategy);

        if (strategyActive[strategy] != active_) {
            strategyActive[strategy] = active_;
            emit StrategyActiveUpdated(strategy, active_);
        }

        uint16 oldBps = _strategyCapBps(strategy);
        if (strategyCapBps[strategy] != capBps_) {
            strategyCapBps[strategy] = capBps_;
        }
        if (!strategyCapConfigured[strategy]) {
            strategyCapConfigured[strategy] = true;
        }
        emit StrategyCapBpsUpdated(strategy, oldBps, capBps_);
    }

    function setStrategyActive(address strategy, bool active_) external onlyOwner {
        require(strategyExists[strategy], "STRATEGY_NOT_REGISTERED");

        if (strategyActive[strategy] != active_) {
            strategyActive[strategy] = active_;
            emit StrategyActiveUpdated(strategy, active_);
        }
    }

    function setStrategyCapBps(address strategy, uint16 newBps) external onlyOwner {
        require(strategy != address(0), "ZERO_STRATEGY");
        require(newBps <= BPS_DENOMINATOR, "INVALID_BPS");

        bool isNew = _registerStrategyIfNeeded(strategy);
        if (isNew) {
            strategyActive[strategy] = true;
            emit StrategyActiveUpdated(strategy, true);
        }

        uint16 oldBps = _strategyCapBps(strategy);
        if (strategyCapBps[strategy] != newBps) {
            strategyCapBps[strategy] = newBps;
        }
        if (!strategyCapConfigured[strategy]) {
            strategyCapConfigured[strategy] = true;
        }
        emit StrategyCapBpsUpdated(strategy, oldBps, newBps);
    }

    /// @notice Request strategy recall; deactivates the strategy
    function requestStrategyRecall(address strategy, uint256 requestedAssets) external onlyOwner {
        require(strategyExists[strategy], "STRATEGY_NOT_REGISTERED");
        require(requestedAssets <= strategyDebt[strategy], "RECALL_EXCEEDS_DEBT");

        if (strategyActive[strategy]) {
            strategyActive[strategy] = false;
            emit StrategyActiveUpdated(strategy, false);
        }

        strategyRecallRequest[strategy] = requestedAssets;
        emit StrategyRecallRequested(strategy, requestedAssets);
    }

    function listStrategies() external view returns (address[] memory) {
        return _strategies;
    }

    function _safeTransferETH(address to, uint256 amount) internal {
        (bool ok,) = to.call{value: amount}("");
        require(ok, "ETH_TRANSFER_FAILED");
    }

    /// @notice totalAssets = idle + strategy debt (debt-based accounting prevents flash loan manipulation)
    function totalAssets() public view override returns (uint256) {
        return idleAssets() + _totalStrategyValue();
    }

    /// @dev Sum strategy values: uses debt-based accounting to prevent flash loan NAV manipulation
    function _totalStrategyValue() internal view returns (uint256 value) {
        uint256 len = _strategies.length;
        for (uint256 i = 0; i < len; i++) {
            address s = _strategies[i];
            value += strategyDebt[s];
        }
    }

    /// @notice Only accept ETH from WETH unwrap
    receive() external payable {
        require(msg.sender == asset(), "ONLY_ASSET");
    }

    function _strategyCapBps(address strategy) internal view returns (uint16) {
        if (!strategyCapConfigured[strategy]) {
            return BPS_DENOMINATOR;
        }
        return strategyCapBps[strategy];
    }

    function _registerStrategyIfNeeded(address strategy) internal returns (bool isNew) {
        if (strategyExists[strategy]) {
            return false;
        }

        strategyExists[strategy] = true;
        _strategies.push(strategy);
        emit StrategyRegistered(strategy);
        return true;
    }

    function _consumeRecallRequest(address strategy, uint256 settledPrincipal) internal {
        uint256 pending = strategyRecallRequest[strategy];
        if (pending == 0 || settledPrincipal == 0) {
            return;
        }

        uint256 fulfilled = settledPrincipal < pending ? settledPrincipal : pending;
        uint256 remaining = pending - fulfilled;
        strategyRecallRequest[strategy] = remaining;

        emit StrategyRecallReduced(strategy, fulfilled, remaining);
    }

    function _mintPerformanceFee(address strategy, uint256 netProfitAfterOffset)
        internal
        returns (uint256 feeAssets, uint256 mintedShares)
    {
        if (performanceFeeBps == 0 || feeRecipient == address(0)) {
            return (0, 0);
        }

        feeAssets = (netProfitAfterOffset * performanceFeeBps) / BPS_DENOMINATOR;
        if (feeAssets == 0) {
            return (0, 0);
        }

        mintedShares = _sharesForFeeAssets(feeAssets);
        if (mintedShares == 0) {
            return (0, 0);
        }

        _mint(feeRecipient, mintedShares);
        totalFeeAssetsAccrued += feeAssets;

        emit PerformanceFeeMinted(strategy, feeRecipient, feeAssets, mintedShares);
    }

    function _sharesForFeeAssets(uint256 feeAssets) internal view returns (uint256) {
        uint256 supply = totalSupply();
        uint256 assetsBeforeMint = totalAssets();

        if (supply == 0) {
            return feeAssets;
        }
        if (assetsBeforeMint <= feeAssets) {
            return 0;
        }

        return Math.mulDiv(feeAssets, supply, assetsBeforeMint - feeAssets, Math.Rounding.Floor);
    }

    /// @dev EMA-like PnL smoothing for telemetry
    function _updateSmoothedPnl(uint256 principal, uint256 returnedAssets) internal {
        int256 oldValue = smoothedPnl;
        int256 samplePnl = _toInt256(returnedAssets) - _toInt256(principal);

        uint256 alpha = uint256(pnlSmoothingBps);
        int256 weightedOld =
            (oldValue * int256(uint256(BPS_DENOMINATOR) - alpha)) / int256(uint256(BPS_DENOMINATOR));
        int256 weightedSample = (samplePnl * int256(alpha)) / int256(uint256(BPS_DENOMINATOR));
        smoothedPnl = weightedOld + weightedSample;

        emit SmoothedPnlUpdated(oldValue, smoothedPnl, samplePnl);
    }

    function _toInt256(uint256 value) internal pure returns (int256) {
        require(value <= uint256(type(int256).max), "INT256_OVERFLOW");
        return int256(value);
    }
}
