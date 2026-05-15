# PenguinYieldVault: 自動化交易與流動性管理系統

PenguinYieldVault 是一套高效能的鏈上自動化策略與流動性提供系統。本專案以 ERC-4626 標準金庫為核心，透過 Go 語言編寫的鏈下機器人即時計算技術指標 (ATR/ADX)，自動執行 Uniswap V3 的流動性區間再平衡與跨池套利。

## 核心系統架構

本系統分為「鏈上智能合約 (On-Chain Smart Contracts)」與「鏈下執行端 (Off-Chain Client)」兩個主要部分。

### 專案結構
- **contracts/**: Foundry 合約專案 (src/script/test/lib/foundry.toml)
- **bot/**: Go 機器人程式碼
- **專案根目錄**: 包含共用的 `.env` 設定與部署腳本。

### 1. 鏈上智能合約 (Solidity)
- **Vault (Vault.sol)**: 遵循 ERC-4626 標準，負責資產管理與收益分配。
- **Strategy Router (StrategyRouter.sol)**: 策略中樞，負責權限控管與風控。
- **Atomic Executor (AtomicExecutor.sol)**: 原子化執行器，確保交易安全性與收益。
- **Uniswap V3 LP Strategy (UniswapV3LPStrategy.sol)**: 自動化流動性再平衡策略。

### 2. 鏈下執行端 (Go 機器人)
- **ATR/ADX 指標運算**: 動態調整做市區間，避免單邊趨勢下的劇烈無常損失。
- **成本收益分析**: 確保每一筆交易在扣除 Gas 與滑點後仍具備正向期望值。

---

## 部署與運行指南

### 1. 環境變數設定
在專案根目錄建立 `.env` 檔案：
```ini
RPC_URL=https://sepolia.infura.io/v3/YOUR_KEY
PRIVATE_KEY_HEX=0x...
ETHERSCAN_API_KEY=...
```

### 2. 執行部署
```bash
./deploy.sh
```

### 3. 當前部屬地址 (Sepolia)
- **Vault**: `0xd38c4f064ae1436bc2d497c5f282ff0280abcecd`
- **StrategyRouter**: `0xc469c8a5769db7fc9e8f1d015794a26a1f840ff0`
- **AtomicExecutor**: `0x8709303256954be145ac2f34fdfd5b7af8084a7f`
- **UniswapV3ArbitrageSource**: `0x82904bead5fb7659b8b5f4034053f9c57e59a749`
- **UniswapV3LPStrategy**: `0xe237e001bad30b65324c19df459c4208a9532b55`

### 4. 24/7 常駐執行與監控 (Linux)
- **啟動服務**: `systemctl --user enable --now penguin-bot.service`
- **查看日誌**: `journalctl --user -u penguin-bot.service -f`
- **查看狀態**: `systemctl --user status penguin-bot.service`

---

## 常用指令與疑難排解

- **查看錢包餘額**: `cast balance 你的地址 --rpc-url $RPC_URL`
- **清理緩存 (磁碟空間不足)**: `rm -rf ~/.cache ~/.foundry/cache && cd contracts && forge clean`
- **手動驗證合約**: `forge verify-contract <ADDRESS> <ContractName> --etherscan-api-key <API_KEY> --chain-id 11155111`

---

## 前端 (客戶端) 串接指南

若您需要開發面向使用者的 Web 前端介面 (DApp)，所有的互動都應圍繞著 **Vault.sol** 進行。

### 1. 充值與提現
- **充值**: 使用者呼叫 `Vault.depositETH()` 存入原生 ETH。
- **提現**: 使用者呼叫 `Vault.withdrawETH()` 或 `Vault.redeemETH()`。

### 2. 儀表板數據讀取
- **總鎖倉量 (TVL)**: `totalAssets()`
- **資產分配**: `idleAssets()` (閒置) 與 `totalStrategyDebt()` (投入策略)。
- **用戶淨值**: 用戶持有的 Shares / `totalSupply()` * `totalAssets()`。
