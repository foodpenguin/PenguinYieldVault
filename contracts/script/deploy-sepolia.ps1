param(
    [Parameter(Mandatory = $true)]
    [string]$PrivateKey,

    [Parameter(Mandatory = $true)]
    [string]$KeeperAddress,

  [Parameter(Mandatory = $true)]
  [string]$LpPoolAddress,

  [Parameter(Mandatory = $true)]
  [string]$PositionManagerAddress,

    [string]$RpcUrl = "https://ethereum-sepolia-rpc.publicnode.com",
    [string]$VaultSeedWei = "5000000000000000000",
    [string]$MinIdleBps = "500",
  [string]$SourceCapBps = "1000",
  [string]$LpCapBps = "9000"
)

$ErrorActionPreference = "Stop"
Set-Location "$PSScriptRoot/.."

$env:KEEPER_ADDRESS = $KeeperAddress
$env:LP_POOL_ADDRESS = $LpPoolAddress
$env:POSITION_MANAGER_ADDRESS = $PositionManagerAddress
$env:VAULT_SEED_WEI = $VaultSeedWei
$env:MIN_IDLE_BPS = $MinIdleBps
$env:SOURCE_CAP_BPS = $SourceCapBps
$env:LP_CAP_BPS = $LpCapBps

forge script script/DeployForkStack.s.sol:DeployForkStack `
  --rpc-url $RpcUrl `
  --private-key $PrivateKey `
  --broadcast -vv

Write-Host "Done. Addresses are in broadcast/DeployForkStack.s.sol/11155111/run-latest.json"
