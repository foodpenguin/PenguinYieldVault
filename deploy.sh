#!/usr/bin/env bash
set -euo pipefail

#############################################################################
# MevBot One-Click Deploy Script (Linux)
# Usage:
#   chmod +x deploy.sh
#   ./deploy.sh
#
# Prerequisites: foundry (forge, cast), go 1.22+, .env in bot/ directory
#############################################################################

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$SCRIPT_DIR"
BOT_DIR="$PROJECT_ROOT/bot"
FOUNDRY_DIR="$PROJECT_ROOT/contracts"
ENV_FILE="$BOT_DIR/.env"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }

###--- Step 0: Check prerequisites ---###
info "Checking prerequisites..."

command -v forge >/dev/null 2>&1 || error "forge not found. Install Foundry: https://book.getfoundry.sh/getting-started/installation"
command -v cast  >/dev/null 2>&1 || error "cast not found. Install Foundry."
command -v go    >/dev/null 2>&1 || error "go not found. Install Go 1.22+: https://go.dev/dl/"

info "forge: $(forge --version | head -1)"
info "go:    $(go version)"

###--- Step 1: Load .env ---###
if [ ! -f "$ENV_FILE" ]; then
    error "bot/.env not found. Copy bot/.env.example and fill in your values."
fi

set -a; source "$ENV_FILE"; set +a

[ -z "${RPC_URL:-}"          ] && error "RPC_URL not set in .env"
[ -z "${PRIVATE_KEY_HEX:-}"  ] && error "PRIVATE_KEY_HEX not set in .env"
[[ "$PRIVATE_KEY_HEX" == *"YOUR_PRIVATE_KEY"* ]] && error "PRIVATE_KEY_HEX is still a placeholder!"

KEEPER_ADDRESS=$(cast wallet address "$PRIVATE_KEY_HEX" 2>/dev/null || true)
[ -z "$KEEPER_ADDRESS" ] && error "Cannot derive address from PRIVATE_KEY_HEX"
info "Deployer/Keeper address: $KEEPER_ADDRESS"

###--- Step 2: Deploy contracts ---###
echo ""
info "=== Phase 1: Deploy Contracts to Sepolia ==="

export KEEPER_ADDRESS
export LP_POOL_ADDRESS="${REBALANCE_POOL_ADDRESS:-0x6418EEC70f50913ff0d756B48d32Ce7C02b47C47}"
export POSITION_MANAGER_ADDRESS="${POSITION_MANAGER_ADDRESS:-0x1238536071E1c677A632429e3655c799b22cDA52}"
export VAULT_SEED_WEI="${VAULT_SEED_WEI:-1000000000000000000}"
export MIN_IDLE_BPS="${MIN_IDLE_BPS:-500}"
export SOURCE_CAP_BPS="${SOURCE_CAP_BPS:-1000}"
export LP_CAP_BPS="${LP_CAP_BPS:-9000}"

if [ ! -d "$FOUNDRY_DIR" ]; then
    error "contracts not found. Please run this script from the project root."
fi

cd "$PROJECT_ROOT"
info "Building contracts..."
cd "$FOUNDRY_DIR"
forge build --sizes

info "Deploying via forge script..."
forge script script/DeployForkStack.s.sol:DeployForkStack \
    --rpc-url "$RPC_URL" \
    --private-key "$PRIVATE_KEY_HEX" \
    --broadcast -vv 2>&1 | tee /tmp/mevbot_deploy.log

# Parse deployed addresses from log
BROADCAST_FILE=$(find "$FOUNDRY_DIR/broadcast/DeployForkStack.s.sol" -name "run-latest.json" 2>/dev/null | head -1)
if [ -n "$BROADCAST_FILE" ]; then
    info "Broadcast file: $BROADCAST_FILE"
    info "Parse deployed addresses from forge output above and update bot/.env"
else
    warn "Broadcast file not found. Manually update bot/.env with deployed addresses."
fi

###--- Step 3: Build bot ---###
echo ""
info "=== Phase 2: Build Bot ==="
cd "$BOT_DIR"

if [ ! -f go.mod ]; then
    error "go.mod not found in $BOT_DIR"
fi

info "Running go mod tidy..."
go mod tidy

info "Building arbitrage-bot..."
go build -o arbitrage-bot ./cmd/arbitrage-bot

info "Binary built: $BOT_DIR/arbitrage-bot"

###--- Step 4: Install systemd service (optional) ---###
echo ""
info "=== Phase 3: Install systemd service ==="

SERVICE_FILE="/etc/systemd/system/mevbot.service"
if [ "$(id -u)" -eq 0 ]; then
    cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=MevBot Arbitrage Bot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$(logname 2>/dev/null || echo "$USER")
WorkingDirectory=$BOT_DIR
ExecStart=$BOT_DIR/arbitrage-bot
Restart=on-failure
RestartSec=10
EnvironmentFile=$ENV_FILE

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    info "Service installed at $SERVICE_FILE"
    info "Start: sudo systemctl start mevbot"
    info "Enable: sudo systemctl enable mevbot"
    info "Logs:   sudo journalctl -u mevbot -f"
else
    warn "Not running as root. Skipping systemd install."
    warn "To install manually, run: sudo $0"
    info "Or run directly: cd $BOT_DIR && ./arbitrage-bot"
fi

echo ""
info "=== Deployment Complete ==="
info "1. Update bot/.env with deployed contract addresses"
info "2. Set DRY_RUN=true for initial observation (recommended 24h)"
info "3. Ensure the deployer wallet has sufficient Sepolia ETH for gas"
