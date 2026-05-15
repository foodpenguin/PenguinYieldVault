<#
.SYNOPSIS
    MevBot One-Click Deploy Script (Windows PowerShell)
.DESCRIPTION
    Deploys contracts to Sepolia, builds Go bot, and provides run instructions.
.NOTES
    Prerequisites: foundry (forge, cast), Go 1.22+, bot/.env configured
#>

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot
if (-not $ProjectRoot) { $ProjectRoot = $PSScriptRoot }
if (-not $ProjectRoot) { $ProjectRoot = Get-Location }
$BotDir = Join-Path $ProjectRoot "bot"
$FoundryDir = Join-Path $ProjectRoot "contracts"
$EnvFile = Join-Path $BotDir ".env"

function Write-Info  { param($m) Write-Host "[INFO] $m" -ForegroundColor Green }
function Write-Warn  { param($m) Write-Host "[WARN] $m" -ForegroundColor Yellow }
function Write-Err   { param($m) Write-Host "[ERROR] $m" -ForegroundColor Red; exit 1 }

#--- Step 0: Prerequisites ---#
Write-Info "Checking prerequisites..."

if (-not (Get-Command forge -ErrorAction SilentlyContinue)) {
    Write-Err "forge not found. Install Foundry: https://book.getfoundry.sh/getting-started/installation"
}
if (-not (Get-Command cast -ErrorAction SilentlyContinue)) {
    Write-Err "cast not found. Install Foundry."
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Err "go not found. Install Go 1.22+: https://go.dev/dl/"
}

Write-Info "forge: $(forge --version | Select-Object -First 1)"
Write-Info "go:    $(go version)"

#--- Step 1: Load .env ---#
if (-not (Test-Path $EnvFile)) {
    Write-Err "bot/.env not found. Create it from the template and fill your values."
}

Get-Content $EnvFile | ForEach-Object {
    $line = $_.Trim()
    if ($line -and -not $line.StartsWith("#")) {
        $key, $value = $line -split "=", 2
        $key = $key.Trim()
        $value = $value.Trim().Trim('"').Trim("'")
        if ($key -and -not [System.Environment]::GetEnvironmentVariable($key)) {
            [System.Environment]::SetEnvironmentVariable($key, $value)
        }
    }
}

$RpcUrl = [System.Environment]::GetEnvironmentVariable("RPC_URL")
$PrivateKey = [System.Environment]::GetEnvironmentVariable("PRIVATE_KEY_HEX")

if (-not $RpcUrl) { Write-Err "RPC_URL not set in .env" }
if (-not $PrivateKey) { Write-Err "PRIVATE_KEY_HEX not set in .env" }
if ($PrivateKey -match "YOUR_PRIVATE_KEY") { Write-Err "PRIVATE_KEY_HEX is still a placeholder!" }

$KeeperAddress = cast wallet address $PrivateKey 2>$null
if (-not $KeeperAddress) { Write-Err "Cannot derive address from PRIVATE_KEY_HEX" }
Write-Info "Deployer/Keeper address: $KeeperAddress"

#--- Step 2: Deploy contracts ---#
Write-Host ""
Write-Info "=== Phase 1: Deploy Contracts to Sepolia ==="

$env:KEEPER_ADDRESS = $KeeperAddress
$env:LP_POOL_ADDRESS = if ($env:REBALANCE_POOL_ADDRESS) { $env:REBALANCE_POOL_ADDRESS } else { "0x6418EEC70f50913ff0d756B48d32Ce7C02b47C47" }
$env:POSITION_MANAGER_ADDRESS = if ($env:POSITION_MANAGER_ADDRESS) { $env:POSITION_MANAGER_ADDRESS } else { "0x1238536071E1c677A632429e3655c799b22cDA52" }
$env:VAULT_SEED_WEI = if ($env:VAULT_SEED_WEI) { $env:VAULT_SEED_WEI } else { "1000000000000000000" }
$env:MIN_IDLE_BPS = if ($env:MIN_IDLE_BPS) { $env:MIN_IDLE_BPS } else { "500" }
$env:SOURCE_CAP_BPS = if ($env:SOURCE_CAP_BPS) { $env:SOURCE_CAP_BPS } else { "1000" }
$env:LP_CAP_BPS = if ($env:LP_CAP_BPS) { $env:LP_CAP_BPS } else { "9000" }

if (-not (Test-Path $FoundryDir)) {
    Write-Err "contracts not found. Please run this script from the project root."
}

Set-Location $FoundryDir

Write-Info "Building contracts..."
forge build --sizes

Write-Info "Deploying via forge script..."
forge script script/DeployForkStack.s.sol:DeployForkStack `
    --rpc-url $RpcUrl `
    --private-key $PrivateKey `
    --broadcast -vv

Write-Info "Check contracts/broadcast/ directory for deployed addresses."
Write-Info "Update bot/.env with the contract addresses from the output above."

#--- Step 3: Build bot ---#
Write-Host ""
Write-Info "=== Phase 2: Build Bot ==="

Set-Location $BotDir

Write-Info "Running go mod tidy..."
go mod tidy

Write-Info "Building arbitrage-bot.exe..."
go build -o arbitrage-bot.exe ./cmd/arbitrage-bot

Write-Info "Binary built: $BotDir\arbitrage-bot.exe"

#--- Step 4: Run instructions ---#
Write-Host ""
Write-Info "=== Deployment Complete ==="
Write-Info "Next steps:"
Write-Info "  1. Update bot/.env with deployed contract addresses"
Write-Info "  2. Run the bot:  cd bot && .\arbitrage-bot.exe"
Write-Info "  3. Keep DRY_RUN=true for initial 24h observation"
Write-Info "  4. Ensure the deployer wallet has sufficient Sepolia ETH"

Set-Location $ProjectRoot
