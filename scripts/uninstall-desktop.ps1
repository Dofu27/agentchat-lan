param(
  [string]$InstallDir = "$env:LOCALAPPDATA\AgentChat",
  [switch]$RemoveData
)

$ErrorActionPreference = "Stop"
$startup = Join-Path ([Environment]::GetFolderPath("Startup")) "AgentChat Console.lnk"
$startMenu = Join-Path ([Environment]::GetFolderPath("Programs")) "AgentChat\AgentChat Console.lnk"
Remove-Item -LiteralPath $startup -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $startMenu -Force -ErrorAction SilentlyContinue

if ($RemoveData) {
  Remove-Item -LiteralPath $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
  Write-Host "Removed $InstallDir"
} else {
  Remove-Item -LiteralPath (Join-Path $InstallDir "AgentChatConsole.exe") -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath (Join-Path $InstallDir "agentchat.exe") -Force -ErrorAction SilentlyContinue
  Write-Host "Removed binaries; kept configuration and data in $InstallDir"
}
