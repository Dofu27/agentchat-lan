param(
  [string]$InstallDir = "$env:LOCALAPPDATA\AgentChat",
  [string]$Role = "hub",
  [string]$HubUrl = "",
  [switch]$NoStartup
)

$ErrorActionPreference = "Stop"
$source = Split-Path -Parent $MyInvocation.MyCommand.Path
$console = Join-Path $source "AgentChatConsole.exe"
$cli = Join-Path $source "agentchat.exe"
if (-not (Test-Path $console) -or -not (Test-Path $cli)) {
  throw "AgentChatConsole.exe and agentchat.exe must sit next to this script."
}

$validRoles = @("hub", "client", "bridge", "observer")
if ($validRoles -notcontains $Role) {
  throw "Role must be one of: $($validRoles -join ', ')"
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item -LiteralPath $console -Destination (Join-Path $InstallDir "AgentChatConsole.exe") -Force
Copy-Item -LiteralPath $cli -Destination (Join-Path $InstallDir "agentchat.exe") -Force

$configPath = Join-Path $InstallDir "desktop.json"
if (-not (Test-Path $configPath)) {
  $config = [ordered]@{
    title = "AgentChat Console"
    width = 1180
    height = 820
    role = $Role
    agentchat_path = (Join-Path $InstallDir "agentchat.exe")
  }
  if ($HubUrl) { $config.hub_url = $HubUrl }
  $json = $config | ConvertTo-Json -Depth 8
  [IO.File]::WriteAllText($configPath, $json, (New-Object Text.UTF8Encoding($false)))
}

$ws = New-Object -ComObject WScript.Shell
$startMenu = Join-Path ([Environment]::GetFolderPath("Programs")) "AgentChat"
New-Item -ItemType Directory -Force -Path $startMenu | Out-Null
$shortcut = $ws.CreateShortcut((Join-Path $startMenu "AgentChat Console.lnk"))
$shortcut.TargetPath = Join-Path $InstallDir "AgentChatConsole.exe"
$shortcut.Arguments = "--config `"$configPath`""
$shortcut.WorkingDirectory = $InstallDir
$shortcut.Description = "AgentChat LAN console"
$shortcut.Save()

if (-not $NoStartup) {
  $startup = [Environment]::GetFolderPath("Startup")
  $run = $ws.CreateShortcut((Join-Path $startup "AgentChat Console.lnk"))
  $run.TargetPath = Join-Path $InstallDir "AgentChatConsole.exe"
  $run.Arguments = "--config `"$configPath`""
  $run.WorkingDirectory = $InstallDir
  $run.Description = "AgentChat LAN console"
  $run.Save()
}

Write-Host "Installed AgentChat Console to $InstallDir"
Write-Host "Config: $configPath"
Write-Host "Role: $Role"
if (-not $NoStartup) { Write-Host "Startup entry installed." }
