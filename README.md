# PenguinYieldVault

**鏈上流動性管理與做市策略金庫**

PenguinYieldVault 是一套自動化的鏈上做市與流動性再平衡系統。本專案以 ERC-4626 標準金庫為資產管理核心，結合 Go 語言編寫的鏈下後台程序，透過運算技術指標（ATR / ADX），自動化執行 Uniswap V3 流動性區間的動態再平衡與跨池套利操作。

---

## 目錄

- [系統架構](#系統架構)
- [專案目錄結構](#專案目錄結構)
- [智能合約模組](#智能合約模組)
- [鏈下機器人模組](#鏈下機器人模組)
- [合約部署地址 (Sepolia)](#合約部署地址-sepolia)
- [環境建置與部署](#環境建置與部署)
- [參數設定指南](#參數設定指南)
- [系統服務管理](#系統服務管理)
- [交易監控與日誌](#交易監控與日誌)
- [金庫狀態查詢 (CLI)](#金庫狀態查詢-cli)
- [DApp 整合介面](#dapp-整合介面)
- [權限控制與安全性](#權限控制與安全性)

---

## 系統架構

本系統採用「鏈上智能合約（On-Chain Contracts）」與「鏈下後台執行程序（Off-Chain Go Bot）」分離協作的技術架構，達到合約邏輯安全防護與鏈下高頻計算效率的平衡。

```
 使用者 (Deposit/Withdraw ETH)
         │
         ▼
 ┌──────────────────┐
 │   Vault (ERC4626) │  ◄── pyETH Shares
 │   totalAssets()   │  ◄── Debt-based accounting (prevents oracle manipulation)
 └───────┬──────────┘
         │ allocateToStrategy / settleStrategy
         ▼
 ┌──────────────────┐
 │  StrategyRouter  │  ◄── 策略調度、損失控制、權限驗證
 └───────┬──────────┘
         │
    ┌────┴────┐
    ▼         ▼
┌────────┐ ┌─────────────┐
│ Arb    │ │ LP Strategy │  ◄── Uniswap V3 流動性管理
│ Source │ │ (Managed)   │
└────────┘ └─────────────┘
    ▲              ▲
    │              │
 ┌──┴──────────────┴──┐
 │   AtomicExecutor   │  ◄── 原子化交易中樞（Pre-check + Execute）
 └────────────────────┘
         ▲
         │
 ┌───────┴────────┐
 │ Go Bot (Off-chain) │
 │ • 套利掃描       │
 │ • ATR/ADX 指標   │
 │ • 再平衡決策     │
 │ • Gas 估算       │
 └────────────────┘
```

---

## 專案目錄結構

```
PenguinYieldVault/
├── api/                           # Go GraphQL API 數據分析後端
│   ├── cmd/api-server/            # 主程式入口 (GraphQL + Indexer)
│   ├── internal/                  # 內部核心邏輯
│   │   ├── store/                 # SQLite 資料存取庫
│   │   ├── indexer/               # 鏈上日誌解析與 TVL 快照
│   │   └── graphql/               # GraphQL Schema 與解析器
│   ├── schema.graphql             # GraphQL 模式定義文件
│   └── README.md                  # API 文件
├── bot/                           # Go 機器人程序模組
│   ├── cmd/arbitrage-bot/         # 主程式入口
│   │   ├── main.go                # 套利掃描、池快照、交易構建
│   │   └── rebalance.go           # ATR/ADX 指標計算、再平衡決策
│   ├── rebalance_state.json       # 再平衡狀態持久化（蠟燭數據、基準價格）
│   └── go.mod / go.sum            # Go 依賴管理
├── contracts/                     # Solidity 智能合約模組（Foundry）
│   ├── src/                       # 合約原始碼
│   │   ├── Vault.sol              # ERC-4626 金庫，債務基礎記帳
│   │   ├── StrategyRouter.sol     # 策略路由與損失控制
│   │   ├── AtomicExecutor.sol     # 原子化交易執行器
│   │   ├── UniswapV3ArbitrageSource.sol  # 跨池套利合約
│   │   ├── UniswapV3LPStrategy.sol       # V3 LP 流動性管理
│   │   ├── OnchainStateLens.sol          # 鏈上池狀態快照
│   │   ├── BotRegistry.sol               # 策略源註冊表
│   │   └── interfaces/                   # 合約介面定義
│   ├── script/                    # 部署與驗證腳本
│   └── test/                      # 單元測試
├── scripts/                       # 系統監控腳本
│   └── tx_log.sh                  # 交易狀態查詢與日誌導出
├── logs/                          # 交易日誌與統計紀錄（CSV）
├── .env                           # 環境變數設定（私鑰、合約地址、參數）
├── .env.example                   # 環境變數範本
├── deploy.sh                      # 一鍵部署腳本
└── README.md
```

---

## 智能合約模組

### Vault (`Vault.sol`)
遵循 ERC-4626 協議規範的核心資產金庫。

- **債務基礎記帳 (Debt-based Accounting)**：`totalAssets()` 採用債務基礎記帳，將閒置資產加總各策略的債務總額。此設計杜絕了因 Uniswap V3 LP 價格波動或閃電貸操縱帶來的即時淨值操縱與三明治攻擊。
- **準備金制度**：透過 `minIdleBps` 控制閒置資金比例，確保閒置資產的安全分配。
- **績效費用**：`performanceFeeBps` 從策略淨利潤中抽取績效費，鑄造份額給 `feeRecipient`。
- **PnL 平滑**：EMA 指數移動平均平滑利潤追蹤。
- **ETH 原生支援**：`depositETH()` / `withdrawETH()` / `redeemETH()` 支援原生 ETH 自動封裝/解封。
- **標準 ERC-4626 提領相容**：當金庫內閒置資金（`idleAssets()`）足夠時，用戶可直接提領或贖回。若閒置資金不足以完全支應，系統將拒絕即時的原生 ETH 提領（`withdrawETH`/`redeemETH`）以防止 WETH 解包失敗，並要求 Keeper/Owner 觸發策略資金回收（`requestStrategyRecall`），藉此維護金庫運作的安全與標準兼容性。

### StrategyRouter (`StrategyRouter.sol`)
策略執行授權與調度模組。

- **權限控制**：Executor allowlist + Source-level scoping。
- **損失限制**：`maxLossBps` 單次最大損失比例，可配合 `haltOnLossLimitBreach` 自動暫停。
- **雙路徑**：`executeStrategy`（有資金分配）與 `executeManagedStrategy`（無資金分配，用於 LP 再平衡）。

### AtomicExecutor (`AtomicExecutor.sol`)
原子化交易中樞，確保交易前狀態檢查與執行在同一交易中完成。

- **Pre-check Snapshot**：交易內即時讀取池狀態快照，計算 `executionContextHash`。
- **Slippage 保護**：`minAmountOut` + `minReturnedAssets` + `minProfit` 三層保護。
- **Keeper 權限**：僅授權的 keeper 地址可調用。

### UniswapV3ArbitrageSource (`UniswapV3ArbitrageSource.sol`)
跨池套利合約，實現 WETH → Token → WETH 的雙向閃電兌換。

- **雙池路由**：Buy pool (WETH→Token) + Sell pool (Token→WETH)。
- **Pool 驗證**：透過 factory 合約驗證 pool 地址正確性。
- **Context Hash**：可選的 `expectedContextHash` 驗證，傳入 `bytes32(0)` 跳過（由 AtomicExecutor 在鏈上計算）。

### UniswapV3LPStrategy (`UniswapV3LPStrategy.sol`)
Uniswap V3 單池流動性頭寸管理合約。

- **四種操作模式**：`REBALANCE_ONLY` (0)、`WITHDRAW_ONLY` (1)、`REBALANCE_THEN_WITHDRAW` (2)、`BOOTSTRAP` (3)。
- **自動分拆**：Bootstrap 時自動 50/50 分拆資產。
- **NAV 估算**：`estimatedNavInAsset()` 即時估算流動性頭寸 + 持有資產的總 NAV。
- **記帳追蹤**：`accountedPrincipal`、`cumulativeRealizedPnl`、`latestFloatingPnl`。

---

## 鏈下機器人模組

### 套利掃描引擎 (`main.go`)
- **多交易對掃描**：同時監控 METH/ETH、UNI/ETH、USDC/ETH 多個交易對。
- **Pool 數據快取**：靜態池數據（token0/1, fee）啟動時預讀並快取，每次掃描僅讀取動態數據（liquidity, slot0），降低 60% RPC 調用。
- **淨利潤核算**：扣除 Gas 成本後的淨利潤需達 `MIN_NET_PROFIT_WEI` 門檻方執行。
- **動態交易量**：根據 `ARBITRAGE_MAX_TRADE_BPS` 和 `availableForStrategy` 動態調整交易量。

### 再平衡決策引擎 (`rebalance.go`)
- **ATR（Average True Range）**：追蹤價格波動率，動態調整 LP 區間寬度。
- **ADX（Average Directional Index）**：衡量趨勢強度，強趨勢時暫停建倉以控制無常損失。
- **黃金法則**：預期手續費收益必須覆蓋再平衡成本（gas + 滑點）方允許操作。
- **Cooldown 保護**：所有操作（包含 bootstrap 和 withdraw）都受 cooldown 約束，防止無限循環。

---

## GraphQL API 數據分析後端

本專案新增了基於 Go 語言與 SQLite 資料庫搭建的輕量級事件索引（Indexer）與 GraphQL API 伺服器，用以將鏈上的關鍵數據轉換成結構化的時間序列與歷史統計資訊，供前端介面及分析工具查詢。

### 核心功能

- **鏈上事件即時索引**：持續監聽並解析合約的 `Deposit`、`Withdraw`、`StrategySettled`、`StrategySettledDetails`、`CapitalAllocated`、`PerformanceFeeMinted` 等事件。
- **電視鎖定價值 (TVL) 快照**：定時記錄金庫的 TVL、閒置資產以及策略債務。
- **SQLite 儲存媒介**：採用啟用 WAL 模式的 SQLite 作為高效能、零維運成本的單檔案資料庫。
- **GraphQL 查詢介面**：提供整合後的金庫歷史 APY、TVL 變化趨勢、用戶交易紀錄及機器人套利/再平衡操作明細。
- **GraphiQL 測試沙盒**：內嵌 GraphiQL Playground，方便在瀏覽器中直接運行與測試查詢。

### 執行與使用方式

```bash
# 1. 進入 api 目錄
cd api

# 2. 啟動 API 伺服器（將自動讀取根目錄的 .env 進行設定）
go run ./cmd/api-server/
```

伺服器啟動後，可在瀏覽器中打開 **http://localhost:8080/graphql** 進入互動式查詢沙盒。

### 查詢與整合範例

前端與其他客戶端可以透過發送標準 HTTP POST 請求至 `http://localhost:8080/graphql` 來串接數據，請求主體（Body）為 JSON 格式，如下所示：

#### 1. 查詢金庫統計摘要 (Vault Stats)
此查詢用以獲取金庫的總報告收益、總損失、待分配本金等即時數據：
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

#### 2. 查詢歷史 TVL 趨勢 (TVL History)
查詢特定時間區間內的 TVL 變動，便於前端繪製圖表：
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

#### 3. 查詢用戶歷史交易 (User Transactions)
輸入用戶的錢包地址，獲取其存款及提款事件：
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

#### 4. JavaScript 串接範例
```javascript
const query = `
  query GetVaultStats {
    vaultStats {
      totalReportedProfit
      totalStrategyDebt
    }
  }
`;

fetch("http://localhost:8080/graphql", {
  method: "POST",
  headers: {
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ query }),
})
  .then((res) => res.json())
  .then((data) => console.log(data.data.vaultStats))
  .catch((err) => console.error("Error fetching GraphQL API:", err));
```

---

## 合約部署地址 (Sepolia)

| 合約 | 地址 | Etherscan |
|---|---|---|
| **Vault** | `0x57436623bb4fe74e7dab0d7c643aa5442b10ee17` | [查看](https://sepolia.etherscan.io/address/0x57436623bb4fe74e7dab0d7c643aa5442b10ee17) |
| **BotRegistry** | `0xfd9bd020d168fdb264e344a6fb95bd24603440ce` | [查看](https://sepolia.etherscan.io/address/0xfd9bd020d168fdb264e344a6fb95bd24603440ce) |
| **StrategyRouter** | `0xde0f2145f99b746db09e568e08af70bc5f6c7833` | [查看](https://sepolia.etherscan.io/address/0xde0f2145f99b746db09e568e08af70bc5f6c7833) |
| **OnchainStateLens** | `0xd3627608aa4c5c071fcbe13c4ff5ac83d9beb69b` | [查看](https://sepolia.etherscan.io/address/0xd3627608aa4c5c071fcbe13c4ff5ac83d9beb69b) |
| **AtomicExecutor** | `0x3c9e6b6b25f74e905bb63a35154348e09fc14a7f` | [查看](https://sepolia.etherscan.io/address/0x3c9e6b6b25f74e905bb63a35154348e09fc14a7f) |
| **ArbitrageSource** | `0x7c57878661b537558e0f3eeece0bac6b44365cf3` | [查看](https://sepolia.etherscan.io/address/0x7c57878661b537558e0f3eeece0bac6b44365cf3) |
| **LP Strategy** | `0x13a30c89b07730ade8c8c2f6bf0dd68b9328a702` | [查看](https://sepolia.etherscan.io/address/0x13a30c89b07730ade8c8c2f6bf0dd68b9328a702) |

> **Share Token**: pyETH (Penguin Yield Eth Vault Share)  
> **底層資產**: WETH (Sepolia)  
> **Keeper**: `0x111cF245355BDe9633C530f701B6B64D71a22BCA`

---

## 環境建置與部署

### 前置需求

- [Foundry](https://book.getfoundry.sh/getting-started/installation)（forge, cast）
- [Go 1.22+](https://go.dev/dl/)
- Sepolia ETH（用於 gas）
- Infura / Alchemy RPC URL

### 快速部署

```bash
# 1. 複製環境設定檔
cp .env.example .env
nano .env  # 填寫 RPC_URL, PRIVATE_KEY_HEX 等

# 2. 一鍵部署合約並編譯 bot
chmod +x deploy.sh
./deploy.sh

# 3. 更新 .env 中的合約地址（部署腳本輸出）

# 4. 啟動 bot
cd bot && ./arbitrage-bot
```

### 手動部署

```bash
# 編譯合約
cd contracts && forge build --sizes

# 部署到 Sepolia（含驗證）
forge script script/DeployForkStack.s.sol:DeployForkStack \
  --rpc-url $RPC_URL \
  --private-key $PRIVATE_KEY_HEX \
  --broadcast --verify \
  --etherscan-api-key $ETHERSCAN_API_KEY -vv

# 編譯 bot
cd ../bot && go build -o arbitrage-bot ./cmd/arbitrage-bot
```

---

## 參數設定指南

### 核心參數 (.env)

```env
# === 套利參數 ===
AMOUNT_IN_WEI=100000000000000000   # 單次最大交易量 (0.1 ETH)
MIN_PROFIT_WEI=0                    # 合約層最低利潤 (0 = 不限)
MIN_NET_PROFIT_WEI=50000000000000   # Bot 層最低淨利潤 (扣除 gas)
MAX_LOSS_BPS=500                    # 單次最大損失 (5%)
ARBITRAGE_MAX_TRADE_BPS=2000        # 最大動用 vault 可用資金比例 (20%)
SLIPPAGE_BPS=100                    # 滑點容忍度 (1%)
DEADLINE_SEC=60                     # 交易截止時間
LOOP_INTERVAL_SEC=900               # 掃描間隔 (15 分鐘)

# === 再平衡參數 ===
REBALANCE_ATR_LENGTH=14             # ATR 蠟燭數量
REBALANCE_ATR_PERIOD_SEC=3600       # ATR 蠟燭週期 (1 小時)
REBALANCE_ADX_LENGTH=14             # ADX 蠟燭數量
REBALANCE_ADX_PERIOD_SEC=86400      # ADX 蠟燭週期 (1 天)
REBALANCE_ADX_TREND_THRESHOLD=25.0  # ADX 趨勢門檻 (>25 = 強趨勢)
REBALANCE_COOLDOWN_SEC=3600         # 再平衡冷卻時間 (1 小時)
REBALANCE_EXPECTED_FEE_DAYS=7.0     # 預期手續費收益計算天數
REBALANCE_DAILY_VOLUME_ASSET=50.0   # 模擬日交易量 (WETH)
```

### 參數調校建議

| 場景 | ATR Period | ADX Threshold | Cooldown | 說明 |
|---|---|---|---|---|
| **測試網** | 3600 (1h) | 25.0 | 3600 (1h) | 低波動，保守設定 |
| **主網穩定幣** | 86400 (1d) | 30.0 | 7200 (2h) | 極低波動 |
| **主網 ETH/BTC** | 3600 (1h) | 20.0 | 1800 (30m) | 中等波動 |
| **主網高波動對** | 900 (15m) | 15.0 | 900 (15m) | 高頻調整 |

---

## 系統服務管理

### Systemd 常駐服務

```bash
# 啟動與開機自啟動
export XDG_RUNTIME_DIR=/run/user/$(id -u)
systemctl --user enable --now penguin-bot.service

# 查看即時日誌
journalctl --user -u penguin-bot.service -f

# 重啟服務
systemctl --user restart penguin-bot.service
```

### 手動運行

```bash
cd bot && nohup ./arbitrage-bot > ../logs/bot.log 2>&1 &
```

---

## 交易監控與日誌

系統附帶 CLI 命令列監控腳本，支援從系統日誌提取交易紀錄並向 RPC 節點查詢確認鏈上執行結果。

```bash
# 查詢歷史交易列表與分類統計
./scripts/tx_log.sh query

# 顯示最新 10 筆紀錄
./scripts/tx_log.sh tail 10

# 掃描歷史日誌並重建同步 CSV 檔案
./scripts/tx_log.sh sync

# 即時監聽日誌並同步更新交易狀態
./scripts/tx_log.sh watch

# 查詢指定 Transaction Hash 的鏈上明細
./scripts/tx_log.sh check <TX_HASH>
```

---

## 金庫狀態查詢 (CLI)

使用 Foundry `cast` 工具進行秒級合約狀態查詢：

```bash
source .env

# 金庫總資產規模 (TVL)
cast call $VAULT_ADDRESS "totalAssets()(uint256)" --rpc-url $RPC_URL

# 閒置準備金
cast call $VAULT_ADDRESS "idleAssets()(uint256)" --rpc-url $RPC_URL

# 策略借貸餘額
cast call $VAULT_ADDRESS "totalStrategyDebt()(uint256)" --rpc-url $RPC_URL

# 待沖銷虧損 (需傳入特定策略地址)
cast call $VAULT_ADDRESS "strategyPendingLoss(address)(uint256)" <STRATEGY_ADDRESS> --rpc-url $RPC_URL

# 每股淨值 (1 share = ? WETH)
cast call $VAULT_ADDRESS "convertToAssets(uint256)(uint256)" 1000000000000000000 --rpc-url $RPC_URL

# LP 策略即時 NAV
cast call $REBALANCE_SOURCE_ADDRESS "estimatedNavInAsset()(uint256)" --rpc-url $RPC_URL
```

---

## DApp 整合介面

### 資產存取

```solidity
// 存入原生 ETH（自動封裝為 WETH）
vault.depositETH{value: amount}(receiver);

// 存入 WETH（需先 approve）
vault.deposit(assets, receiver);

// 提領為原生 ETH
vault.withdrawETH(assets, receiver, owner);

// 依份額贖回為原生 ETH
vault.redeemETH(shares, receiver, owner);
```

### 唯讀查詢介面

```solidity
// 總資產規模 (TVL = idle + strategy NAV)
function totalAssets() public view returns (uint256);

// 閒置可提取準備金
function idleAssets() public view returns (uint256);

// 策略借貸本金總額
function totalStrategyDebt() public view returns (uint256);

// 特定策略借出本金
function strategyDebt(address strategy) public view returns (uint256);

// 特定策略待沖銷虧損
function strategyPendingLoss(address strategy) public view returns (uint256);

// EMA 平滑利潤
function smoothedPnl() public view returns (int256);

// Shares → Assets 換算
function convertToAssets(uint256 shares) public view returns (uint256);

// 最大可提取資產 (標準 ERC-4626 接口，不受閒置資金強制性硬性上限)
function maxWithdraw(address owner) public view returns (uint256);
```

---

## 權限控制與安全性

### 合約權限架構

| 角色 | 權限 | 設定方式 |
|---|---|---|
| **Vault Owner** | 設定 router、策略註冊、準備金比例 | `Ownable` (deployer) |
| **Router Owner** | 設定 executor、策略源權限 | `Ownable` (deployer) |
| **Executor** | 透過 router 執行策略 | `router.setExecutor()` |
| **Keeper** | 呼叫 AtomicExecutor 執行套利 | `executor.setKeeper()` |

### 安全機制

- **ReentrancyGuard**：Vault、Router、Source、Strategy 均具備重入防護。
- **損失限制**：`maxLossBps` 單次損失上限 + `haltOnLossLimitBreach` 自動暫停。
- **Balance Delta 驗證**：Router 透過 balance delta 驗證策略實際返還金額，防止回報造假。
- **Context Hash Binding**：AtomicExecutor 將 pre-check 狀態綁定到執行上下文，確保交易一致性。
- **Cooldown 防護**：所有再平衡操作（含 bootstrap/withdraw）受冷卻期約束，防止無限循環。

---

## License

MIT
