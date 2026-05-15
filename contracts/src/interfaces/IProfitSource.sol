// SPDX-License-Identifier: MIT
pragma solidity ^0.8.13;

/// @title Pluggable profit source interface
interface IProfitSource {
    /// @notice Unique identifier for this source (e.g. "arbitrage", "lp")
    function sourceId() external view returns (string memory);

    /// @notice Asset token address (must match Vault asset)
    function asset() external view returns (address);

    /// @notice Whether this source is currently active
    function isActive() external view returns (bool);

    /// @notice Execute strategy and transfer returned assets to caller (Router)
    /// @param params ABI-encoded strategy-specific parameters
    /// @param executionContextHash Binds on-chain pre-check to execution
    /// @param amountIn Principal allocated by Vault (0 for managed ops)
    function execute(bytes calldata params, bytes32 executionContextHash, uint256 amountIn)
        external
        returns (int256 realizedPnl, uint256 returnedAssets);
}
