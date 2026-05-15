# PenguinYieldVault: 自動化交易與流動性管理系統

PenguinYieldVault 是一套高效能的鏈上自動化策略與流動性提供系統。本專案以 ERC-4626 標準金庫為核心，透過 Go 語言編寫的鏈下機器人即時計算技術指標 (ATR/ADX)，自動執行 Uniswap V3 的流動性區間再平衡與跨池套利。

## 核心系統架構

本系統分為「鏈上智能合約 (On-Chain Smart Contracts)」與「鏈下執行端 (Off-Chain Client)」兩個主要部分。

### 專案結構
- contracts/：Foundry 合約專案（src/script/test/lib/foundry.toml）
- bot/：Go 機器人
- 專案根目錄為 PenguinYieldVault；手動執行 forge 請先進入 contracts/

### 1. 鏈上智能合約 (Solidity)
所有合約皆具有嚴格的權限控管與資金安全機制：

- **Vault (Vault.sol)**
  - 遵循 ERC-4626 標準的收益聚合金庫。
  - 負責接收使用者的 WETH 存款，並發行對應的 Vault Shares 作為憑證。
  - 內建閒置資金 (Idle Assets) 與策略負債 (Strategy Debt) 管理機制。只有受信任的路由合約可以動用金庫資金。
  - 具備平滑化利潤 (Smoothed PnL) 分發機制，防止惡意用戶透過閃電貸獲取非預期的利潤。

- **Strategy Router (StrategyRouter.sol)**
  - 系統的策略路由中樞，也是鏈下機器人唯一的操作入口。
  - 負責驗證機器人的執行權限，並限制單次執行的最大虧損幅度 (Loss Limit)。
  - 提供即時結算與資金分配通道，確保機器人的每一次操作都在風控參數的規範內。

- **Atomic Executor (AtomicExecutor.sol)**
  - 專為套利與高頻交易設計的原子化執行器。
  - 採用鏈下快照 (Off-chain Snapshot) 與鏈上環境雜湊對比 (Context Hash Validation) 技術，確保交易執行時的滑點與預期收益符合機器人的預算，防止遭遇三明治攻擊。

- **Uniswap V3 LP Strategy (UniswapV3LPStrategy.sol)**
  - 全自動化的 Uniswap V3 流動性管理策略。
  - 支援資金自動建倉 (Auto-Bootstrap)：當合約內無活躍倉位且有可用資金時，會自動將單邊資產對半兌換並建立初始流動性。
  - 支援動態區間再平衡 (Dynamic Range Rebalance) 與無常損失防護。

### 2. 鏈下執行端 (Go 機器人)
基於 Go 語言 (Go 1.22+) 開發的高效能背景服務：

- **技術指標運算引擎**
  - **ATR (Average True Range)**: 即時計算市場波動率，並根據波動率自動放大或縮小 Uniswap V3 的做市區間 (Tick Range)。
  - **ADX (Average Directional Index)**: 分析市場趨勢強度。當市場處於單邊強烈趨勢時 (ADX 過高)，機器人會暫停再平衡以防止承受過大的無常損失；僅在震盪市中頻繁提供流動性。
- **動態成本計算 (黃金法則)**
  - 在執行任何再平衡交易前，機器人會模擬計算「預期手續費收益」是否大於「交易 Gas 成本 + 滑點 + 預期無常損失」。只有在長期期望值為正的情況下才會送出交易。

---

## 部署與運行指南

### 前置需求
- **Foundry**: 用於編譯與部署智能合約 (`forge`, `cast` 命令)。
- **Go 1.22 或以上版本**: 用於編譯鏈下機器人。
- **RPC 節點**: Infura、Alchemy 或任何支援 Sepolia 測試網的 RPC 節點。

### 1. 環境變數設定
進入 `bot/` 目錄，複製或建立 `.env` 檔案，並填寫以下核心變數：

```ini
# 網路與認證
RPC_URL=https://sepolia.infura.io/v3/YOUR_INFURA_KEY
PRIVATE_KEY_HEX=0x你的部署錢包私鑰
CHAIN_ID=11155111

# 功能開關
ENABLE_ARBITRAGE_BOT=true
ENABLE_REBALANCE_BOT=false
DRY_RUN=true # 測試期間請設為 true，機器人將只會記錄日誌而不會發送真實交易
```

### 2. 執行一鍵部署腳本
我們提供了跨平台的自動化部署腳本。該腳本將依序完成：合約編譯、Sepolia 測試網部署、Go 語言機器人編譯。

**Windows 使用者 (PowerShell)**:
```powershell
.\deploy.ps1
```

**Linux 使用者 (Bash)**:
```bash
chmod +x deploy.sh
./deploy.sh
```
(Linux 用戶若以 root 權限執行腳本，系統會自動將機器人註冊為 systemd 背景服務，實現 24 小時不間斷運行與當機自動重啟。)

### 3. 更新合約地址
部署完成後，終端機會輸出金庫與策略合約的部署地址。請將這些地址回填到 `bot/.env` 檔案中：
```ini
ATOMIC_EXECUTOR_ADDRESS=0x...
SOURCE_ADDRESS=0x...
VAULT_ADDRESS=0x...
STRATEGY_ROUTER_ADDRESS=0x...
```

### 4. 啟動機器人
更新地址後，進入 `bot/` 資料夾啟動機器人：
```bash
cd bot
./arbitrage-bot
```

---

## 前端 (客戶端) 串接指南

若您需要開發面向使用者的 Web 前端介面 (DApp)，所有的互動都應圍繞著 **Vault.sol** 進行。

### 1. 充值與提現 (Deposit & Withdraw)
- **充值**: 使用者呼叫 `Vault.depositETH()` 存入原生 ETH (合約會自動轉換為 WETH)。
- **提現**: 使用者呼叫 `Vault.withdrawETH(uint256 amount)` 或 `Vault.redeemETH(uint256 shares)` 提取資金。

### 2. 儀表板數據讀取 (唯讀)
前端不需關心策略的複雜運作細節，只需從金庫讀取以下資訊即可呈現完整的用戶報表：
- **總鎖倉量 (TVL)**: 呼叫 `totalAssets()`，代表金庫中所有資產的總和 (包含閒置資金與已投入策略的資金)。
- **資產分配狀態**: 
  - 閒置資金 (未承擔風險): `idleAssets()`
  - 已投入策略資金: `totalStrategyDebt()`
- **用戶資產淨值 (NAV)**: 將用戶持有的 Vault Shares 餘額除以 `totalSupply()`，再乘上 `totalAssets()` 即可得出用戶實際的本金加利潤。
- **整體利潤**: 呼叫 `smoothedPnl()` 獲取平滑化後的系統總利潤，此數值可用於計算年化報酬率 (APY)。

---

## 策略詳細配置參數參考

若需微調機器人的交易邏輯，可於 `.env` 中修改以下參數：

| 變數名稱 | 說明 |
|---|---|
| `MIN_NET_PROFIT_WEI` | 套利機器人的最低淨利潤要求。扣除 Gas 成本後，若利潤低於此數值則放棄交易。 |
| `MAX_LOSS_BPS` | 允許單次策略執行的最大滑點或虧損幅度 (基點，100 = 1%)。 |
| `REBALANCE_ATR_MULTIPLIER` | ATR 乘數。數值越大，做市區間越寬，手續費收益越低但無常損失風險越小。 |
| `REBALANCE_ADX_TREND_THRESHOLD` | 判斷趨勢行情的 ADX 閾值。超過此數值代表單邊趨勢強烈，機器人將暫停建倉。 |
| `LOOP_INTERVAL_SEC` | 機器人掃描鏈上區塊的間隔時間 (秒)。建議 Sepolia 設為 10，主網設為 12。 |
