#!/usr/bin/env bash
# ============================================================
# tx_log.sh — PenguinYieldVault 交易記錄與查詢工具
# 使用方式:
#   ./scripts/tx_log.sh sync      — 讀取歷史日誌同步到 CSV
#   ./scripts/tx_log.sh watch     — 持續監控即時日誌並記錄新交易
#   ./scripts/tx_log.sh query     — 查詢所有已記錄的交易
#   ./scripts/tx_log.sh tail [N]  — 顯示最後 N 筆交易 (預設 20)
#   ./scripts/tx_log.sh check <HASH> — 查詢單筆交易鏈上狀態
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

# 載入環境變數
if [ -f "$ROOT_DIR/.env" ]; then
  set -a; source "$ROOT_DIR/.env"; set +a
fi

LOG_FILE="${TX_LOG_FILE:-$ROOT_DIR/logs/transactions.csv}"
mkdir -p "$(dirname "$LOG_FILE")"

CAST="${HOME}/.foundry/bin/cast"
XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
export XDG_RUNTIME_DIR

# ──────────────────────────────────────
# 輔助: 從 hash 查詢鏈上狀態
# ──────────────────────────────────────
check_tx() {
  local hash="$1"
  if [ -z "$hash" ] || [ "$hash" = "pending" ]; then
    echo "pending"
    return
  fi
  local receipt
  receipt=$($CAST receipt "$hash" --rpc-url "$RPC_URL" --json 2>/dev/null) || { echo "unknown"; return; }
  local status
  status=$(echo "$receipt" | python3 -c "import sys,json; d=json.load(sys.stdin); print('success' if d.get('status')=='0x1' else 'failed')" 2>/dev/null) || status="unknown"
  local gas
  gas=$(echo "$receipt" | python3 -c "import sys,json; d=json.load(sys.stdin); print(int(d.get('gasUsed','0x0'),16))" 2>/dev/null) || gas=0
  echo "${status}|gas=${gas}"
}

# ──────────────────────────────────────
# 寫入 CSV header 若不存在
# ──────────────────────────────────────
init_csv() {
  if [ ! -f "$LOG_FILE" ]; then
    echo "timestamp,type,pair_or_action,tx_hash,gross_wei,net_wei,gas_wei,status,notes" > "$LOG_FILE"
    echo "[tx_log] Created $LOG_FILE"
  fi
}

# ──────────────────────────────────────
# 解析一行 journalctl 日誌並寫入 CSV
# ──────────────────────────────────────
parse_and_log() {
  local line="$1"
  local ts
  ts=$(echo "$line" | grep -oP '^[A-Za-z]+ \d+ \d+:\d+:\d+' || date -u +"%Y-%m-%dT%H:%M:%SZ")

  # 套利機會找到
  if echo "$line" | grep -qE "opportunity found"; then
    local pair gross net
    pair=$(echo "$line" | grep -oP '[\w\/]+(?= opportunity)' || echo "arb")
    gross=$(echo "$line" | grep -oP 'gross=\K[0-9]+' || echo "0")
    net=$(echo "$line" | grep -oP 'net=\K[0-9]+' || echo "0")
    echo "$ts,arb_opportunity,$pair,pending,$gross,$net,0,pending," >> "$LOG_FILE"

  # 套利交易送出
  elif echo "$line" | grep -qE "arb tx sent:"; then
    local pair tx_hash
    pair=$(echo "$line" | grep -oP '[\w\/]+(?= arb tx)' || echo "arb")
    tx_hash=$(echo "$line" | grep -oP '0x[0-9a-fA-F]{64}')
    if [ -n "$tx_hash" ]; then
      local tmp="$LOG_FILE.tmp"
      awk -v h="$tx_hash" -v p="$pair" '
        /arb_opportunity/ && $0 ~ p && /,pending,/ {
          sub(/,pending,/, ","h","); print; next
        }
        { print }
      ' "$LOG_FILE" > "$tmp" && mv "$tmp" "$LOG_FILE"
    fi

  # Rebalance 觸發
  elif echo "$line" | grep -qE "rebalance triggered:"; then
    local action
    action=$(echo "$line" | grep -oP 'action=\K\w+' || echo "rebalance")
    echo "$ts,rebalance,$action,pending,0,0,0,pending," >> "$LOG_FILE"

  # Bootstrap tx 送出
  elif echo "$line" | grep -qE "bootstrap tx sent:"; then
    local tx_hash amount
    tx_hash=$(echo "$line" | grep -oP '0x[0-9a-fA-F]{64}')
    amount=$(echo "$line" | grep -oP 'amountAllocated: \K[0-9]+' || echo "0")
    if [ -n "$tx_hash" ]; then
      local tmp="$LOG_FILE.tmp"
      awk -v h="$tx_hash" -v a="$amount" '
        /rebalance,bootstrap,pending/ {
          sub(/,pending,/, ","h","); sub(/,0,pending,/, ","a",pending,"); print; next
        }
        { print }
      ' "$LOG_FILE" > "$tmp" && mv "$tmp" "$LOG_FILE"
    fi

  # Rebalance tx 送出
  elif echo "$line" | grep -qE "rebalance tx sent:"; then
    local tx_hash
    tx_hash=$(echo "$line" | grep -oP '0x[0-9a-fA-F]{64}')
    if [ -n "$tx_hash" ]; then
      local tmp="$LOG_FILE.tmp"
      awk -v h="$tx_hash" '
        /rebalance,.*,pending/ && !/bootstrap/ {
          sub(/,pending,/, ","h","); print; next
        }
        { print }
      ' "$LOG_FILE" > "$tmp" && mv "$tmp" "$LOG_FILE"
    fi
  fi
}

# ──────────────────────────────────────
# 回溯更新 pending 交易的狀態
# ──────────────────────────────────────
update_pending() {
  local tmp="$LOG_FILE.tmp"
  cp "$LOG_FILE" "$tmp"
  while IFS=',' read -r ts type pair hash gross net gas status notes; do
    [ "$status" = "pending" ] && [ -n "$hash" ] && [ "$hash" != "pending" ] || continue
    local result
    result=$(check_tx "$hash")
    local new_status="${result%%|*}"
    local gas_used="${result##*=}"
    gas_used="${gas_used#gas=}"
    sed -i "s|$hash,.*,pending,|$hash,$gross,$net,$gas_used,$new_status,|" "$LOG_FILE"
  done < <(tail -n +2 "$tmp" 2>/dev/null || true)
  rm -f "$tmp"
}

# ──────────────────────────────────────
# 指令: sync — 讀取歷史日誌
# ──────────────────────────────────────
cmd_sync() {
  echo "[tx_log] Syncing transaction log from journalctl history..."
  echo "timestamp,type,pair_or_action,tx_hash,gross_wei,net_wei,gas_wei,status,notes" > "$LOG_FILE"
  journalctl --user -u penguin-bot.service --no-pager | grep -E "opportunity found|arb tx sent|rebalance triggered|bootstrap tx sent|rebalance tx sent" | while IFS= read -r line; do
    parse_and_log "$line"
  done
  update_pending
  echo "[tx_log] Sync complete! Log updated at $LOG_FILE"
}

# ──────────────────────────────────────
# 指令: watch — 即時監控
# ──────────────────────────────────────
cmd_watch() {
  init_csv
  echo "[tx_log] Watching journalctl for new transactions... (Ctrl-C to stop)"
  echo "[tx_log] Logging to: $LOG_FILE"
  local counter=0
  journalctl --user -u penguin-bot.service -f --no-pager | while IFS= read -r line; do
    parse_and_log "$line"
    ((counter = counter + 1))
    if ((counter % 30 == 0)); then
      update_pending
    fi
  done
}

# ──────────────────────────────────────
# 指令: query — 顯示所有交易
# ──────────────────────────────────────
cmd_query() {
  if [ ! -f "$LOG_FILE" ]; then
    cmd_sync
  fi
  update_pending
  echo ""
  echo "════════════════════════════════════════════════════════════════════════════════════════════"
  echo "   PenguinYieldVault — Transaction Log"
  echo "   File: $LOG_FILE"
  echo "════════════════════════════════════════════════════════════════════════════════════════════"
  printf "\n%-16s %-16s %-16s %-68s %-12s %-10s\n" "Timestamp" "Type" "Pair/Action" "Tx Hash" "Gas Used" "Status"
  printf "%-16s %-16s %-16s %-68s %-12s %-10s\n" "────────────────" "────────────────" "────────────────" "────────────────────────────────────────────────────────────────────" "────────────" "──────────"
  tail -n +2 "$LOG_FILE" | while IFS=',' read -r ts type pair hash gross net gas status notes; do
    local color=""
    case "$status" in
      success) color="\033[32m" ;;
      failed)  color="\033[31m" ;;
      pending) color="\033[33m" ;;
      *)       color="\033[37m" ;;
    esac
    printf "${color}%-16s %-16s %-16s %-68s %-12s %-10s\033[0m\n" "$ts" "$type" "$pair" "$hash" "$gas" "$status"
  done
  echo ""
  # 統計
  local total success failed pending
  total=$(tail -n +2 "$LOG_FILE" | wc -l)
  success=$(tail -n +2 "$LOG_FILE" | grep -c ",success," || true)
  failed=$(tail -n +2 "$LOG_FILE" | grep -c ",failed," || true)
  pending=$(tail -n +2 "$LOG_FILE" | grep -c ",pending," || true)
  echo "  Total: $total  |  ✅ Success: $success  |  ❌ Failed: $failed  |  ⏳ Pending: $pending"
  echo ""
}

# ──────────────────────────────────────
# 指令: tail — 顯示最後 N 筆
# ──────────────────────────────────────
cmd_tail() {
  local n="${1:-20}"
  if [ ! -f "$LOG_FILE" ]; then cmd_sync; fi
  update_pending
  echo "Last $n transactions:"
  head -1 "$LOG_FILE"
  tail -n "$n" "$LOG_FILE"
}

# ──────────────────────────────────────
# 指令: check — 查詢單筆 tx 狀態
# ──────────────────────────────────────
cmd_check() {
  local hash="${1:-}"
  [ -z "$hash" ] && { echo "Usage: $0 check <TX_HASH>"; exit 1; }
  echo "Checking tx: $hash"
  $CAST receipt "$hash" --rpc-url "$RPC_URL" 2>&1 | grep -E "status|gasUsed|blockNumber|revertReason"
}

# ──────────────────────────────────────
# 主入口
# ──────────────────────────────────────
case "${1:-query}" in
  sync)    cmd_sync ;;
  watch)   cmd_watch ;;
  query)   cmd_query ;;
  tail)    cmd_tail "${2:-20}" ;;
  check)   cmd_check "${2:-}" ;;
  *)       echo "Usage: $0 {sync|watch|query|tail [N]|check <hash>}"; exit 1 ;;
esac
