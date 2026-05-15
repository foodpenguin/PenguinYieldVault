// SPDX-License-Identifier: MIT
pragma solidity ^0.8.13;

import {IProfitSource} from "./interfaces/IProfitSource.sol";

/// @title Bot source registry
/// @notice Manages pluggable profit sources (add, disable, remove)
contract BotRegistry {
    address public owner;

    address[] private _sources;
    mapping(address => bool) public exists;
    mapping(address => bool) public active;

    event SourceAdded(address indexed source, string sourceId);
    event SourceRemoved(address indexed source);
    event SourceActiveUpdated(address indexed source, bool active);
    event OwnerTransferred(address indexed oldOwner, address indexed newOwner);

    modifier onlyOwner() {
        require(msg.sender == owner, "NOT_OWNER");
        _;
    }

    constructor() {
        owner = msg.sender;
    }

    function transferOwnership(address newOwner) external onlyOwner {
        require(newOwner != address(0), "ZERO_ADDRESS");
        emit OwnerTransferred(owner, newOwner);
        owner = newOwner;
    }

    /// @notice Register a new profit source
    function addSource(address source) external onlyOwner {
        require(source != address(0), "ZERO_ADDRESS");
        require(source.code.length > 0, "SOURCE_NOT_CONTRACT");
        require(!exists[source], "ALREADY_EXISTS");

        exists[source] = true;
        active[source] = true;
        _sources.push(source);

        emit SourceAdded(source, IProfitSource(source).sourceId());
        emit SourceActiveUpdated(source, true);
    }

    /// @notice Toggle source active state
    function setSourceActive(address source, bool isActive_) external onlyOwner {
        require(exists[source], "NOT_EXISTS");
        active[source] = isActive_;
        emit SourceActiveUpdated(source, isActive_);
    }

    /// @notice Remove source using swap-and-pop
    function removeSource(address source) external onlyOwner {
        require(exists[source], "NOT_EXISTS");

        uint256 length = _sources.length;
        for (uint256 i = 0; i < length;) {
            if (_sources[i] == source) {
                if (i != length - 1) {
                    _sources[i] = _sources[length - 1];
                }
                _sources.pop();
                break;
            }

            unchecked {
                ++i;
            }
        }

        delete exists[source];
        delete active[source];

        emit SourceRemoved(source);
    }

    function listSources() external view returns (address[] memory) {
        return _sources;
    }
}
