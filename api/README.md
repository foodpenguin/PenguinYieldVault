# PenguinYieldVault GraphQL API Server

A lightweight event indexer and high-performance GraphQL API server built in Go. It continuously indexes blockchain events from the `Vault` and `StrategyRouter` contracts, stores them in a local SQLite database, and exposes a real-time GraphQL API for frontend applications.

## Features

- **Blockchain Event Indexing**: Monitors and indexes:
  - `Deposit` / `Withdraw` (ERC-4626 standard events)
  - `StrategySettled` / `StrategySettledDetails`
  - `CapitalAllocated`
  - `PerformanceFeeMinted`
  - `StrategyExecuted` / `StrategyManaged`
- **Telemetry Snapshots**: Takes periodic total-value-locked (TVL) snapshots.
- **SQLite Storage**: Uses WAL-enabled SQLite for high-performance concurrent writes and reads.
- **GraphQL Endpoint**: Exposes structured telemetry, user transaction logs, bot operation history, and aggregated statistics.
- **Embedded GraphiQL Playground**: Visually test and run queries directly in the browser.

## Getting Started

### Prerequisites

- Go (v1.22+)
- SQLite3 compiler dependencies (if building from source)

### Configuration

The server parses configuration variables from `/home/ark009770/PenguinYieldVault/.env`. Make sure the following keys are set:

```env
RPC_URL=https://sepolia.infura.io/v3/YOUR_INFURA_KEY
VAULT_ADDRESS=0x...
STRATEGY_ROUTER_ADDRESS=0x...
API_PORT=8080
INDEXER_START_BLOCK=0
```

### Running the Server

Run the server locally with:

```bash
cd api
go run ./cmd/api-server/
```

Access the interactive GraphQL interface by navigating to:
* **Public Cloud Server**: **[http://34.81.58.100:8080/graphql](http://34.81.58.100:8080/graphql)**
* **Local Development**: **[http://localhost:8080/graphql](http://localhost:8080/graphql)**

---

## GraphQL Schema Reference

### 1. Query Vault Statistics

Returns global health metrics, lifetime profits, performance fees, and outstanding debts.

```graphql
query {
  vaultStats {
    totalReportedProfit
    totalReportedLoss
    totalGrossProfit
    totalFeeAssetsAccrued
    totalStrategyDebt
    smoothedPnl
  }
}
```

### 2. Query Historical TVL

Fetches TVL history over a specified timestamp range.

```graphql
query {
  tvlHistory(from: 1716300000, to: 1716400000, interval: "day") {
    timestamp
    totalAssets
    idleAssets
    strategyDebt
    blockNumber
  }
}
```

### 3. Query User Transactions

Returns the complete transaction history for a specific user.

```graphql
query {
  userTransactions(user: "0x111cF245355BDe9633C530f701B6B64D71a22BCA", first: 10, skip: 0) {
    txHash
    blockNumber
    timestamp
    type
    assets
    shares
  }
}
```

### 4. Query Bot Operations

Exposes all automated bot activities (capital allocations, trade executions, rebalances, and settlements).

```graphql
query {
  botOperations(first: 10, skip: 0) {
    txHash
    blockNumber
    timestamp
    type
    source
    profit
    loss
    details
  }
}
```
