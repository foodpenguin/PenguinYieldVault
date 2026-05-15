param(
    [Parameter(Mandatory = $true)] [string]$EtherscanApiKey,
    [Parameter(Mandatory = $true)] [string]$Vault,
    [Parameter(Mandatory = $true)] [string]$Registry,
    [Parameter(Mandatory = $true)] [string]$Router,
    [Parameter(Mandatory = $true)] [string]$Lens,
    [Parameter(Mandatory = $true)] [string]$Executor,
    [Parameter(Mandatory = $true)] [string]$Source,

    [string]$Weth = "0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14",
    [string]$SwapRouter = "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"
)

$ErrorActionPreference = "Stop"
Set-Location "$PSScriptRoot/.."

$env:ETHERSCAN_API_KEY = $EtherscanApiKey

# 1) Vault(address asset_, string name_, string symbol_)
$vaultArgs = cast abi-encode "constructor(address,string,string)" $Weth "MEV Vault Share" "mvSHARE"
forge verify-contract $Vault src/Vault.sol:Vault --chain sepolia --constructor-args $vaultArgs

# 2) BotRegistry() - no constructor args
forge verify-contract $Registry src/BotRegistry.sol:BotRegistry --chain sepolia

# 3) StrategyRouter(address registry_, address vault_)
$routerArgs = cast abi-encode "constructor(address,address)" $Registry $Vault
forge verify-contract $Router src/StrategyRouter.sol:StrategyRouter --chain sepolia --constructor-args $routerArgs

# 4) OnchainStateLens() - no constructor args
forge verify-contract $Lens src/OnchainStateLens.sol:OnchainStateLens --chain sepolia

# 5) AtomicExecutor(address router_, address lens_)
$executorArgs = cast abi-encode "constructor(address,address)" $Router $Lens
forge verify-contract $Executor src/AtomicExecutor.sol:AtomicExecutor --chain sepolia --constructor-args $executorArgs

# 6) UniswapV3ArbitrageSource(address swapRouter_, address asset_, string sourceId_)
$sourceArgs = cast abi-encode "constructor(address,address,string)" $SwapRouter $Weth "amm-arbitrage"
forge verify-contract $Source src/UniswapV3ArbitrageSource.sol:UniswapV3ArbitrageSource --chain sepolia --constructor-args $sourceArgs

Write-Host "All verify commands submitted."
