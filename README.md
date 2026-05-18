# PenguinYieldVault: 鏈上自動化做市策略與流動性再平衡金庫

**PenguinYieldVault** 是一套具備機構級風險控管與極速執行能力的鏈上做市流動性管理與跨池套利金庫系統。本專案以 ERC-4626 標準金庫為資產核心，結合 Go 語言開發的高並發鏈下做市機器人，透過即時計算價格趨勢與波動率指標 (ATR/ADX)，自動化執行 Uniswap V3 的流動性區間動態再平衡與跨池套利。

---

## 🏗️ 核心系統架構與專案目錄

本系統採用「鏈上智能合約 (On-Chain Contracts)」與「鏈下高速執行端 (Off-Chain Go Bot)」分離協作的先進架構，在確保資金絕對安全的同時達到最高的執行效率與利潤率。

```
PenguinYieldVault/
├── bot/                     # Go 機器人程式碼 (指標運算、行情分析、交易廣播)
├── contracts/               # Solidity 合約專案 (Foundry 架構)
│   ├── src/                 # 智能合約原始碼 (Vault, StrategyRouter, AtomicExecutor 等)
│   ├── script/              # 部署與維護腳本
│   └── test/                # 單元測試與整合測試
├── scripts/                 # 維運自動化與監控工具
│   └── tx_log.sh            # 交易即時監控、鏈上狀態回查與日誌導出腳本
├── logs/                    # 系統交易日誌與統計數據 (CSV 格式)
├── .env.example             # 環境變數設定範本
└── README.md                # 專案文檔
```

### 1. 鏈上智能合約模組 (Solidity)
- **Vault (`Vault.sol`)**：遵循 ERC-4626 標準，管理投資者本金、閒置準備金比率 (`minIdleBps`) 及多策略負債分配。
- **Strategy Router (`StrategyRouter.sol`)**：策略執行授權與分配總樞紐。負責對各策略合約進行嚴格的風控與損失上限 (`maxLossBps`) 攔截。
- **Atomic Executor (`AtomicExecutor.sol`)**：閃電式執行保護中樞，確保交易在滿足最小淨利潤 (`minProfit`) 條件下執行，否則即刻 Revert 以保護資產。
- **Uniswap V3 Arbitrage Source (`UniswapV3ArbitrageSource.sol`)**：雙腿連環套利合約，實現 WETH → Token → WETH 的零風險獲利。
- **Uniswap V3 LP Strategy (`UniswapV3LPStrategy.sol`)**：流動性池動態管理合約。負責自動化提領、初始化與區間再平衡。

### 2. 鏈下高速執行端 (Go 機器人)
- **技術指標動態運算 (ATR / ADX)**：監控市場趨勢強弱。在單邊強趨勢下停止開倉或加寬做市區間，防止劇烈的無常損失 (Impermanent Loss)。
- **精準 Gas 成本與收益預算**：每一筆重平衡或套利在發起前，皆即時估算鏈上 Base Fee、優先費 (Priority Tip) 與滑點，確保達到 `MIN_NET_PROFIT_WEI` 門檻。

---

## 🚀 部署與常駐運行指南

### 1. 環境變數設定 (.env)
在專案根目錄複製設定範本並填入您的金鑰與 RPC 資訊：
```bash
cp .env.example .env
nano .env
```
> **⚠️ 測試網特別注意**：
> 在 Sepolia 測試網中，由於流動性池每日真實交易量極低，機器人在計算重平衡黃金法則時若發現沒有手續費收益，會誤判池子虧損而頻繁發起撤資與重新建倉 (`withdraw` ↔ `bootstrap` 循環)。
> 請確保在 `.env` 中設定測試網參數：
> `REBALANCE_DAILY_VOLUME_ASSET=50.0` 及 `LOOP_INTERVAL_SEC=60`，以利系統順利模擬收益並維持部位。

### 2. 當前已部署正式地址 (Sepolia 測試網)
合約架構已成功部署並完美串接：
- **Vault**：`0xd38c4f064ae1436bc2d497c5f282ff0280abcecd`
- **BotRegistry**：`0x43882b30a442b347bc0bf315a392b02222a8048e`
- **StrategyRouter**：`0xc469c8a5769db7fc9e8f1d015794a26a1f840ff0`
- **AtomicExecutor**：`0x8709303256954be145ac2f34fdfd5b7af8084a7f`
- **UniswapV3ArbitrageSource**：`0x82904bead5fb7659b8b5f4034053f9c57e59a749`
- **UniswapV3LPStrategy**：`0xe237e001bad30b65324c19df459c4208a9532b55`

### 3. Linux 24/7 常駐服務啟動
本系統已建置為 systemd 使用者層級常駐服務，自動崩潰重啟：
```bash
# 啟動與開機自啟
export XDG_RUNTIME_DIR=/run/user/$(id -u)
systemctl --user enable --now penguin-bot.service

# 查看即時日誌
journalctl --user -u penguin-bot.service -f
```

---

## 📊 交易日誌追蹤與監控系統 (`scripts/tx_log.sh`)

為方便管理者追蹤所有自動化上鏈操作的狀態與結果，系統附帶強大的 CLI 監控工具，會自動從 journalctl 提取交易紀錄並向 Infura 回查成功狀態與實際 Gas 消耗。

```bash
# 1. 查詢所有歷史交易列表與總計統計 (彩色表格展示)
./scripts/tx_log.sh query

# 2. 顯示最後 10 筆交易
./scripts/tx_log.sh tail 10

# 3. 掃描過去所有 journalctl 日誌，完整重新建檔並同步最新狀態
./scripts/tx_log.sh sync

# 4. 即時監聽日誌並動態更新新交易
./scripts/tx_log.sh watch

# 5. 快速查詢單筆 Transaction Hash 的鏈上細節
./scripts/tx_log.sh check <TX_HASH>
```

---

## 🔗 前端 (DApp) 開發與客戶端串接指南

若您需要為使用者開發前端儀表板，所有的互動與資產存取皆應透過 **`Vault.sol`** 合約。

### 1. 用戶充值與提現
- **存入 (Deposit)**：呼叫 `Vault.depositETH(address receiver)` 存入原生 ETH，合約會自動封裝為 WETH 並發放 ERC-4626 金庫份額 (Shares)。
- **提款 (Withdraw/Redeem)**：呼叫 `Vault.withdrawETH(uint256 assets, ...)` 或 `Vault.redeemETH(uint256 shares, ...)`，自動銷毀 Shares 並返還原生 ETH。

### 2. 金庫資產與收益分析
- **總鎖倉資產 (TVL)**：呼叫 `Vault.totalAssets()`。
- **閒置準備金比率**：`Vault.idleAssets()` (留存於 Vault 未放貸資金)。
- **策略投入資金**：`Vault.totalStrategyDebt()` (已分配至策略進行做市或套利的資金總和)。
- **用戶個人資產淨值**：`( 用戶持有的 Shares * totalAssets() ) / totalSupply()`。

---

## 🛡️ 安全性與權限管理
- **Router 執行權限**：執行任何策略必須經由 `StrategyRouter.setExecutor(address, true)` 授權。
- **策略資金動態調撥**：新建立的策略預設若標記為託管模式 (`managedOnlySource = true`)，將無法由機器人動態調撥資金；上線前需呼叫 `StrategyRouter.setManagedOnlySource(strategy, false)` 解鎖。
