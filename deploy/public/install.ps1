# RMM-AI agent installer (Windows).
# Usage (PowerShell as Administrator):
#   iwr -useb http://SERVER/install.ps1 | iex                 # then it prompts, OR pass args:
#   & ([scriptblock]::Create((iwr -useb http://SERVER/install.ps1))) -Server http://SERVER -Token <ENROLL>
param(
    [string]$Server,
    [string]$Token,
    [string]$Site  # accepted for parity; site binding actually comes from the enroll token
)

$ErrorActionPreference = "Stop"

if (-not $Server) { $Server = Read-Host "Server URL (e.g. http://192.168.0.40:3000)" }
if (-not $Token)  { $Token  = Read-Host "Enrollment token" }
if (-not $Server) { Write-Error "Server is required"; exit 1 }
if (-not $Token)  { Write-Error "Token is required";  exit 1 }

# Require admin (installing a Windows service needs elevation).
$isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()
          ).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
if (-not $isAdmin) { Write-Error "Run this in an elevated PowerShell (Run as Administrator)."; exit 1 }

$Dir   = Join-Path $Env:ProgramData "rmm-agent"
$Exe   = Join-Path $Dir "rmm-agent.exe"
$State = Join-Path $Dir "state.json"
$Bin   = "rmm-agent-windows-amd64.exe"
$Ws    = $Server -replace '^https','wss' -replace '^http','ws'

Write-Host "RMM-AI agent installer"
Write-Host "  server : $Server"
Write-Host "  binary : $Bin"

New-Item -ItemType Directory -Force -Path $Dir | Out-Null

Write-Host "Downloading agent..."
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Invoke-WebRequest -UseBasicParsing -Uri "$Server/dl/$Bin" -OutFile $Exe

Write-Host "Installing as a Windows service..."
& $Exe -server "$Ws/ws/agent" -state $State -enroll-token $Token -action install
& $Exe -action start

Write-Host "Done. The device should appear under Devices shortly."
