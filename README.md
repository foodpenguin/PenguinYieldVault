# PenguinYieldVault: 鏈上流動性管理與做市策略金庫

PenguinYieldVault 是一套自動化的鏈上做市與流動性再平衡系統。本專案以 ERC-4626 標準金庫為資產管理核心，結合 Go 語言編寫的鏈下後台程序，透過運算技術指標 (ATR/ADX)，自動化執行 Uniswap V3 流動性區間的動態再平衡與跨池套利操作。

---

## 核心系統架構與專案目錄

本系統採用「鏈上智能合約 (On-Chain Contracts)」與「鏈下後台執行程序 (Off-Chain Go Bot)」分離協作的技術架構，達到合約邏輯安全防護與鏈下高頻計算效率的平衡。

```
PenguinYieldVault/
├── bot/                     # Go 機器人程序模組 (行情監控、指標計算、交易構建)
├── contracts/               # Solidity 智能合約模組 (基於 Foundry 開發環境)
│   ├── src/                 # 合約原始碼 (Vault, StrategyRouter, AtomicExecutor 等)
│   ├── script/              # 部署與設定自動化腳本
│   └── test/                # 單元測試與整合驗證
├── scripts/                 # 系統監控與日誌處理腳本
│   └── tx_log.sh            # 交易執行狀態回查與日誌導出腳本
├── logs/                    # 交易日誌與統計紀錄 (CSV 格式)
├── .env.example             # 環境變數設定範本
└── README.md                # 專案文檔
```

### 1. 鏈上智能合約模組 (Solidity)
- **Vault (`Vault.sol`)**：遵循 ERC-4626 協議規範，負責資產託管、計算準備金比率 (`minIdleBps`) 及調撥多策略的資金負債。
- **Strategy Router (`StrategyRouter.sol`)**：策略執行授權與調度模組。負責驗證調用者權限與實施各策略的單次最大損失比例 (`maxLossBps`) 控制。
- **Atomic Executor (`AtomicExecutor.sol`)**：原子化交易中樞，確保交易結果符合最低預期淨利潤 (`minProfit`)，未達標則執行回滾 (Revert)。
- **Uniswap V3 Arbitrage Source (`UniswapV3ArbitrageSource.sol`)**：跨池套利合約，實現 WETH 與各代幣池間的雙向閃電兌換。
- **Uniswap V3 LP Strategy (`UniswapV3LPStrategy.sol`)**：流動性頭寸管理合約，負責提供、撤出流動性與再平衡操作。

### 2. 鏈下執行端模組 (Go 機器人)
- **技術指標動態運算 (ATR / ADX)**：監控市場波動率與趨勢強度。在單邊強趨勢下暫停建倉或調整區間跨度，以控制無常損失 (Impermanent Loss)。
- **成本與淨利潤核算**：發起交易前估算當前網路 Base Fee、Priority Fee 與預期滑點，符合 `MIN_NET_PROFIT_WEI` 門檻方進行上鏈廣播。

---

## 部署與運行設定指南

### 1. 環境變數設定 (.env)
複製設定檔範本並填寫金鑰與節點 RPC 資訊：
```bash
cp .env.example .env
nano .env
```
> **測試網執行注意事項**：
> 在 Sepolia 測試網環境中，因流動性池缺乏真實市場交易量，機器人在評估做市黃金法則時若計算不出手續費收益，會依據設定邏輯頻繁觸發撤資與重新建倉 (`withdraw` 與 `bootstrap` 循環)。
> 建議在 `.env` 中設定測試網模擬參數：
> `REBALANCE_DAILY_VOLUME_ASSET=50.0` 與 `LOOP_INTERVAL_SEC=60`，以利系統維持正常頭寸管理。

### 2. 部署合約地址 (Sepolia 測試網)
當前合約部署與關聯設定如下：
- **Vault**：`0xd38c4f064ae1436bc2d497c5f282ff0280abcecd`
- **BotRegistry**：`0x43882b30a442b347bc0bf315a392b02222a8048e`
- **StrategyRouter**：`0xc469c8a5769db7fc9e8f1d015794a26a1f840ff0`
- **AtomicExecutor**：`0x8709303256954be145ac2f34fdfd5b7af8084a7f`
- **UniswapV3ArbitrageSource**：`0x82904bead5fb7659b8b5f4034053f9c57e59a749`
- **UniswapV3LPStrategy**：`0xe237e001bad30b65324c19df459c4208a9532b55`

### 3. Linux 系統常駐服務管理
系統使用 systemd 使用者層級常駐服務運行：
```bash
# 啟動與開機自啟動
export XDG_RUNTIME_DIR=/run/user/$(id -u)
systemctl --user enable --now penguin-bot.service

# 查看即時日誌
journalctl --user -u penguin-bot.service -f
```

---

## 交易紀錄與狀態監控系統 (`scripts/tx_log.sh`)

系統附帶 CLI 命令列監控腳本，支援自系統日誌提取交易紀錄並向 RPC 節點查詢確認鏈上執行結果與實際 Gas 消耗量。

```bash
# 1. 查詢歷史交易列表與分類統計
./scripts/tx_log.sh query

# 2. 顯示最新 10 筆紀錄
./scripts/tx_log.sh tail 10

# 3. 掃描歷史日誌並重建同步 CSV 檔案
./scripts/tx_log.sh sync

# 4. 即時監聽日誌並同步更新交易狀態
./scripts/tx_log.sh watch

# 5. 查詢指定 Transaction Hash 的鏈上明細
./scripts/tx_log.sh check <TX_HASH>
```

---

## 金庫狀態 CLI 實時查詢工具 (Using Cast)

本系統支援透過 Foundry 的 `cast` 工具進行秒級的合約狀態實時查詢。在主機終端機中，您可以直接複製並運行以下命令，快速獲取金庫的 TVL、閒置資產、策略負債與歷史未沖銷虧損等核心數據：

### 1. 查詢金庫總資產規模 (TVL)
```bash
source .env && cast call $VAULT_ADDRESS "totalAssets()" --rpc-url $RPC_URL | xargs cast --to-dec | awk '{print $1/10^18 " WETH"}'
```

### 2. 查詢金庫閒置準備金 (可供隨時提款餘額)
```bash
source .env && cast call $VAULT_ADDRESS "idleAssets()" --rpc-url $RPC_URL | xargs cast --to-dec | awk '{print $1/10^18 " WETH"}'
```

### 3. 查詢策略合約當前借貸餘額 (未歸還本金)
```bash
source .env && cast call $VAULT_ADDRESS "totalStrategyDebt()" --rpc-url $RPC_URL | xargs cast --to-dec | awk '{print $1/10^18 " WETH"}'
```

### 4. 查詢待沖銷歷史虧損 (Pending Loss)
```bash
source .env && cast call $VAULT_ADDRESS "pendingLoss()" --rpc-url $RPC_URL | xargs cast --to-dec | awk '{print $1/10^18 " WETH"}'
```

---

## 前端 (DApp) 開發與客戶端介面指南

面向終端使用者的 Web 介面或應用程式，資產存取與份額兌換均需透過 **`Vault.sol`** 合約調用。

### 1. 資產存入與提領
- **存入 (Deposit)**：調用 `Vault.depositETH(address receiver)` 存入原生 ETH，合約自動封裝為 WETH 並鑄造對應的 ERC-4626 份額 (Shares)。
- **提領 (Withdraw/Redeem)**：調用 `Vault.withdrawETH(uint256 assets, ...)` 或 `Vault.redeemETH(uint256 shares, ...)`，合約銷毀 Shares 並返還原生 ETH。

### 2. 金庫數據與指標計算
- **總資產管理規模 (TVL)**：調用 `Vault.totalAssets()`。
- **閒置準備資產**：`Vault.idleAssets()` (保留於 Vault 合約未放貸或投入策略的資產額度)。
- **策略分配總額**：`Vault.totalStrategyDebt()` (已分配至各下層策略的資產合計)。
- **單一用戶資產價值**：`( 用戶持有的 Shares * totalAssets() ) / totalSupply()`。

---

## 權限控制與安全性說明
- **Router 執行器權限**：執行策略或重平衡指令的錢包帳號，必須通過 `StrategyRouter.setExecutor(address, true)` 授權。
- **策略資金動態分配**：若下層策略合約在 Router 中設為託管模式 (`managedOnlySource = true`)，將無法由機器人動態調撥資金；需調用 `StrategyRouter.setManagedOnlySource(strategy, false)` 開放權限。
