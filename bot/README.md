# Sepolia Arbitrage + Rebalance Bot (Go + go-ethereum)

這個 bot 支援兩個模式：

1. 套利模式：持續監測 Sepolia 指定的 METH/ETH、UNI/ETH、USDC/ETH 多個 V3 池子，
  用與 `OnchainStateLens` 相同的 fee + liquidity + virtual reserves 報價模型找價差，
  只在「預估淨利 > 門檻」時先做本地簽名確認，再送出 `AtomicExecutor.executeWithSnapshot`。
2. 再平衡模式：以 ATR 動態區間 + ADX 趨勢 + golden rule（預期手續費 vs 發散損失與成本）判斷，
  觸發後送 `StrategyRouter.executeManagedStrategy`（含 `principalToSettle/maxLossBps/minReturnedAssets`）執行 rebalance 或 withdraw。

## 目前流程

### 套利流程

1. 讀取每個池子 `token0/token1/fee/liquidity/slot0`。
2. 對每個交易對做雙池路徑模擬：
  - buy: WETH -> Token
  - sell: Token -> WETH
3. 計算毛利 (`WETHBack - AmountIn`)。
4. 估算 gas 成本後得到淨利。
5. 淨利超過 `MIN_NET_PROFIT_WEI` 時，對機會資料做 ECDSA 簽名（本地確認）。
6. 組 `executionContextHash`（同時綁定買池與賣池快照），並送 `executeWithSnapshot` 交易。

### 再平衡流程

1. 讀取 `REBALANCE_POOL_ADDRESS` 的即時價格（`sqrtPriceX96`）。
2. 依 `REBALANCE_ATR_LENGTH`/`REBALANCE_ATR_PERIOD_SEC` 與 `REBALANCE_ADX_LENGTH`/`REBALANCE_ADX_PERIOD_SEC` 累積 K 線，分別計算 ATR 與 ADX。
3. 以 `ATR * REBALANCE_ATR_MULTIPLIER` 建立區間並限制在 `REBALANCE_RANGE_BPS_MIN/MAX`。
4. 價格進入邊界觸發區（`REBALANCE_TRIGGER_ZONE_BPS`）或越界時：
  - 若 ADX < `REBALANCE_ADX_TREND_THRESHOLD`，視為震盪 → 不動作。
  - 若 ADX ≥ 閾值，進入 golden rule 檢查：
    - 預期手續費：`dailyVolume * feeRate * liquidityShare * expectedDays`
    - 發散損失 + gas + swap 成本
5. golden rule 通過 → `executeManagedStrategy`（action=rebalance）。
6. golden rule 不通過 → `executeManagedStrategy`（action=withdraw，可選 burn LP）。

## 前置需求

- Go 1.22+
- 可用的 Sepolia RPC (`RPC_URL`)
- 套利模式需已部署 `AtomicExecutor` 與可呼叫 source
- 再平衡模式需已部署 `StrategyRouter` 與 LP source（`UniswapV3LPStrategy`）
- `StrategyRouter` owner 需先設定 `setExecutor(executor,true)` 與 `setExecutorSource(executor, source, true)`
- 有 Sepolia ETH 的執行錢包

## 安裝

```bash
go mod tidy
```

## 設定

複製 `.env.example` 後填值：

- `RPC_URL`: Sepolia RPC
- `PRIVATE_KEY_HEX`: 交易簽名私鑰（不含空白）
- `ENABLE_ARBITRAGE_BOT`: 是否啟用套利（預設 `true`）
- `ENABLE_REBALANCE_BOT`: 是否啟用再平衡（預設 `false`）
- `DRY_RUN`: `true` 只掃描不送交易，`false` 才會送交易

套利模式關鍵參數：

- `ATOMIC_EXECUTOR_ADDRESS`: 你的 `AtomicExecutor` 合約地址
- `SOURCE_ADDRESS`: 註冊在 Router 的 source 地址
- `VAULT_ADDRESS`: Vault 合約地址（用來抓 `totalAssets/availableForStrategy` 做資金上限）

再平衡模式關鍵參數：

- `STRATEGY_ROUTER_ADDRESS`
- `REBALANCE_SOURCE_ADDRESS`（可省略，預設沿用 `SOURCE_ADDRESS`）
- `REBALANCE_POOL_ADDRESS`
- `POSITION_MANAGER_ADDRESS`

可選參數：

- `AMOUNT_IN_WEI` 預設 `0.1 ETH`
- `MIN_PROFIT_WEI` 傳給合約的最小利潤門檻
- `MIN_NET_PROFIT_WEI` Bot 端（含 gas）最小淨利門檻
- `MAX_LOSS_BPS` 套利路徑的本金最大可接受虧損（會換算成 `minReturnedAssets`）
- `ARBITRAGE_MAX_TRADE_BPS` 單筆套利可用資金上限（占 Vault `totalAssets`，同時受 `availableForStrategy` 限制）
- `SLIPPAGE_BPS` 預設 `50`（0.5%）
- `DEADLINE_SEC` 預設 `30`
- `LOOP_INTERVAL_SEC` 預設 `5`
- `MAX_GAS_LIMIT` 預設 `700000`
- `STRATEGY_MODE`: `amm`、`dummy` 或 `raw`
  - `amm`（預設）: 自動組雙腿 AMM swap 參數，給 `UniswapV3ArbitrageSource` 使用
  - `dummy`: 教學用固定 payout（本地測試方便）
  - `raw`: 直接使用 `RAW_STRATEGY_PARAMS_HEX`
- `REBALANCE_GAS_LIMIT` 再平衡估算 gas 上限
- `REBALANCE_ATR_LENGTH` ATR 長度（預設 `14`）
- `REBALANCE_ATR_PERIOD_SEC` ATR 週期秒數（預設 `3600`）
- `REBALANCE_ATR_MULTIPLIER` ATR 倍數（預設 `2.0`）
- `REBALANCE_RANGE_BPS_MIN/MAX` ATR 區間的最小/最大範圍（bps）
- `REBALANCE_ADX_LENGTH` ADX 長度（預設 `14`）
- `REBALANCE_ADX_PERIOD_SEC` ADX 週期秒數（預設 `86400`）
- `REBALANCE_ADX_TREND_THRESHOLD` ADX 進場閾值（預設 `25`）
- `REBALANCE_EXPECTED_FEE_DAYS` 預期計算天數
- `REBALANCE_DAILY_VOLUME_ASSET` 估算單日成交量（以策略資產計價）
- `REBALANCE_TRIGGER_ZONE_BPS` 觸發邊界比例（預設 `2000` = 20%）
- `REBALANCE_COST_MULTIPLIER_BPS` 成本倍數門檻（預設 `15000` = 1.5x）
- `REBALANCE_PAYBACK_THRESHOLD_HOURS` 回本時限（小時）
- `REBALANCE_STATE_FILE` ATR 狀態檔位置
- `REBALANCE_PRINCIPAL_TO_SETTLE` 回傳資產要結算掉的策略本金
- `REBALANCE_MAX_LOSS_BPS` 再平衡路徑可接受虧損上限
- `REBALANCE_MIN_RETURNED_ASSETS` 再平衡回款最小值
- `REBALANCE_BURN_POSITION` withdraw 時是否 burn LP

新增套利目標池（METH/WETH）：

- `0x84F491DD1e1Bb2b251bEA2CAb9ac6849E94bfBC5`（0.3%）
- `0x972894Ed8c33AC5041795a8022fca3908cfe7a8C`（1%）

新增目標代幣：

- `METH = 0x4f7A67464B5976d7547c860109e4432d50AfB38e`
- `WETH = 0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14`

## 執行

在 `bot/` 目錄執行時，程式會自動載入同目錄的 `.env`（已存在的系統環境變數會優先生效）。

```bash
go run ./cmd/arbitrage-bot
```

## 重要提醒

- 目前「簽名確認」是 bot 本地的確認機制，並非鏈上驗簽流程。
- 若改成真實套利 source，請確認 source 合約有把 `executionContextHash` 驗證綁死，避免 pre-check 與實際執行脫鉤。
- 建議先用 `DRY_RUN=true` 觀察幾輪，再切 `false`。
- 再平衡成本估算中的 `E_cost` 以 WETH 單位計算，若 strategy asset 非 WETH，bot 目前會保守略過再平衡。
