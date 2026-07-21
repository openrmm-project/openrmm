# OpenRMM agent installer (Windows, run as Administrator).
param(
    [string]$Server,
    [string]$Token,
    [string]$Site,
    [string]$UpdatePublicKey,
    [switch]$RequireTls,
    [switch]$LabMode,
    [switch]$ResetIdentity
)

$ErrorActionPreference = 'Stop'
if (-not $Server) { $Server = Read-Host 'Server URL (https://rmm.example.com)' }
if (-not $Token) { $Token = Read-Host 'Enrollment token' }
if (-not $Server) { throw 'Server is required' }
if (-not $Token) { throw 'Token is required' }
$Server = $Server.TrimEnd('/')
if ($RequireTls -and $Server -notlike 'https://*') { throw '-RequireTls needs an HTTPS server URL' }
if (-not $LabMode) {
    if ($Server -notlike 'https://*') { throw 'Production install requires HTTPS; use -LabMode only in an isolated lab' }
    if (-not $UpdatePublicKey) { throw 'Production install requires -UpdatePublicKey' }
}

$isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltinRole]::Administrator)
if (-not $isAdmin) { throw 'Run this in an elevated PowerShell (Run as Administrator).' }
if (-not (Get-Command openssl -ErrorAction SilentlyContinue)) {
    throw 'OpenSSL 3.x or newer is required for Ed25519 signature verification. Install it and ensure openssl.exe is on PATH.'
}
$OpenSslVersion = (& openssl version)
if ($LASTEXITCODE -ne 0 -or $OpenSslVersion -notmatch '^OpenSSL ([3-9]|[1-9][0-9])\.') {
    throw "OpenSSL 3.x or newer is required for Ed25519 verification; found: $OpenSslVersion"
}

# -UpdatePublicKey may carry several trusted keys separated by commas, semicolons
# or whitespace. During a key rotation the operator pins the new key alongside the
# old one; a signature is accepted if it verifies under any pinned key, so the
# fleet converges without a reinstall.
$publicKeys = @()
if ($UpdatePublicKey) {
    $publicKeys = @($UpdatePublicKey -split '[,;\s]+' | Where-Object { $_ })
}

$Dir = Join-Path $Env:ProgramData 'rmm-agent'
$Exe = Join-Path $Dir 'rmm-agent.exe'
$State = Join-Path $Dir 'state.json'
$Health = Join-Path $Dir 'health.json'
$Backup = "$Exe.install-old"
$SignaturePath = "$Exe.sig"
$SignatureBackup = "$SignaturePath.install-old"
$StateBackup = "$State.install-old"
$OutboxBackup = "$State.outbox.install-old"
$Arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq 'Arm64') { 'arm64' } else { 'amd64' }
$Bin = "rmm-agent-windows-$Arch.exe"
$Ws = $Server -replace '^https', 'wss' -replace '^http', 'ws'
$transactionStarted = $false
$swapped = $false
$committed = $false
$hadOld = $false
$hadState = $false
$hadOutbox = $false
$serviceExisted = $false
$serviceInstalledByThisRun = $false
$oldServicePath = $null
$TempDir = $null

function Invoke-Agent {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$AgentArgs)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        & $Exe @AgentArgs 2>&1 | ForEach-Object { Write-Host "  $_" }
        return $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
}

function Restore-Install {
    Write-Warning 'Install did not become healthy; rolling back.'
    if ($serviceExisted) {
        Stop-Service -Name 'rmm-agent' -Force -ErrorAction SilentlyContinue
        $service = Get-Service -Name 'rmm-agent' -ErrorAction SilentlyContinue
        if ($service) { $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30)) }
    } elseif ($serviceInstalledByThisRun -and (Test-Path $Exe)) {
        Invoke-Agent -action stop | Out-Null
        Invoke-Agent -action uninstall | Out-Null
    }
    if (Test-Path $Backup) {
        Remove-Item -Force -ErrorAction SilentlyContinue $Exe
        Move-Item -Force $Backup $Exe
    } elseif (-not $hadOld -and $swapped) {
        Remove-Item -Force -ErrorAction SilentlyContinue $Exe
    }
    Remove-Item -Force -ErrorAction SilentlyContinue $SignaturePath
    if (Test-Path $SignatureBackup) { Move-Item -Force $SignatureBackup $SignaturePath }
    if ($hadState) {
        Remove-Item -Force -ErrorAction SilentlyContinue $State
        Move-Item -Force $StateBackup $State
    }
    if ($hadOutbox) {
        Remove-Item -Force -ErrorAction SilentlyContinue "$State.outbox"
        Move-Item -Force $OutboxBackup "$State.outbox"
    }
    if ($serviceExisted -and $hadOld -and (Test-Path $Exe)) {
        & sc.exe config rmm-agent binPath= $oldServicePath | Out-Null
        if ($LASTEXITCODE -ne 0) { Write-Warning 'Could not restore the previous service ImagePath' }
        Start-Service -Name 'rmm-agent' -ErrorAction SilentlyContinue
    }
}

function ConvertTo-ServiceCommand {
    param([string]$Executable, [string[]]$Arguments)
    $all = @($Executable) + $Arguments
    $quoted = foreach ($value in $all) {
        if ($value.Contains('"')) { throw 'Service arguments may not contain quotes' }
        '"' + $value + '"'
    }
    return $quoted -join ' '
}

function Test-Ed25519AnyKey {
    param(
        [Parameter(Mandatory)][string]$DigestPath,
        [Parameter(Mandatory)][string]$SignaturePath,
        [Parameter(Mandatory)][string[]]$Keys,
        [Parameter(Mandatory)][string]$WorkDir
    )
    $spkiPrefix = [byte[]](0x30,0x2a,0x30,0x05,0x06,0x03,0x2b,0x65,0x70,0x03,0x21,0x00)
    $derPath = Join-Path $WorkDir 'verify-public.der'
    foreach ($k in $Keys) {
        try { $rawKey = [Convert]::FromBase64String($k) } catch { throw 'Update public key is not valid base64' }
        if ($rawKey.Length -ne 32) { throw 'Update public key must contain 32 bytes' }
        [IO.File]::WriteAllBytes($derPath, $spkiPrefix + $rawKey)
        & openssl pkeyutl -verify -pubin -inkey $derPath -keyform DER -rawin -in $DigestPath -sigfile $SignaturePath | Out-Null
        if ($LASTEXITCODE -eq 0) { return $true }
    }
    return $false
}

try {
    New-Item -ItemType Directory -Force -Path $Dir | Out-Null
    if (Test-Path $Backup) { throw "Unresolved installer backup exists at $Backup" }
    if (Test-Path $SignatureBackup) { throw "Unresolved signature backup exists at $SignatureBackup" }
    if (Test-Path $StateBackup) { throw "Unresolved state backup exists at $StateBackup" }
    if (Test-Path $OutboxBackup) { throw "Unresolved outbox backup exists at $OutboxBackup" }
    if (Test-Path "$Exe.update-pending") { throw 'A self-update transaction is still pending' }
    $TempDir = Join-Path $Dir ("install." + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $TempDir | Out-Null
    $Candidate = Join-Path $TempDir $Bin
    $Manifest = Join-Path $TempDir 'SHA256SUMS'
    $SignatureText = Join-Path $TempDir 'SHA256SUMS.sig'
    $ArtifactSignatureText = Join-Path $TempDir "${Bin}.sig"

    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Write-Host "Downloading and verifying $Bin..."
    Invoke-WebRequest -UseBasicParsing -Uri "$Server/dl/$Bin" -OutFile $Candidate
    Invoke-WebRequest -UseBasicParsing -Uri "$Server/dl/SHA256SUMS" -OutFile $Manifest
    $entries = @(Get-Content $Manifest | ForEach-Object {
        if ($_ -match '^([0-9a-fA-F]{64})\s+(.+)$' -and $Matches[2] -eq $Bin) { $Matches[1].ToLowerInvariant() }
    })
    if ($entries.Count -ne 1) { throw "Manifest does not contain exactly one entry for $Bin" }
    $actual = (Get-FileHash -Algorithm SHA256 $Candidate).Hash.ToLowerInvariant()
    if ($actual -ne $entries[0]) { throw "SHA-256 mismatch for $Bin" }

    if ($UpdatePublicKey) {
        Invoke-WebRequest -UseBasicParsing -Uri "$Server/dl/SHA256SUMS.sig" -OutFile $SignatureText
        $signatureFields = ((Get-Content -Raw $SignatureText).Trim() -split '\s+')
        if ($signatureFields.Count -ne 2 -or $signatureFields[0] -ne 'rmm-ed25519-sha256-v1') {
            throw 'Invalid SHA256SUMS signature format'
        }
        try {
            $rawSignature = [Convert]::FromBase64String($signatureFields[1])
        } catch {
            throw 'Signature is not valid base64'
        }
        if ($rawSignature.Length -ne 64) { throw 'Ed25519 signature must contain 64 bytes' }
        [IO.File]::WriteAllBytes((Join-Path $TempDir 'signature.bin'), $rawSignature)
        & openssl dgst -sha256 -binary -out (Join-Path $TempDir 'manifest.digest') $Manifest
        if ($LASTEXITCODE -ne 0) { throw 'Could not hash checksum manifest' }
        if (-not (Test-Ed25519AnyKey -DigestPath (Join-Path $TempDir 'manifest.digest') `
                -SignaturePath (Join-Path $TempDir 'signature.bin') -Keys $publicKeys -WorkDir $TempDir)) {
            throw 'Ed25519 manifest signature verification failed (no pinned key matched)'
        }
        Invoke-WebRequest -UseBasicParsing -Uri "$Server/dl/${Bin}.sig" -OutFile $ArtifactSignatureText
        $artifactSignatureFields = ((Get-Content -Raw $ArtifactSignatureText).Trim() -split '\s+')
        if ($artifactSignatureFields.Count -ne 2 -or $artifactSignatureFields[0] -ne 'rmm-ed25519-sha256-v1') {
            throw 'Invalid artifact signature format'
        }
        try {
            $rawArtifactSignature = [Convert]::FromBase64String($artifactSignatureFields[1])
        } catch {
            throw 'Artifact signature is not valid base64'
        }
        if ($rawArtifactSignature.Length -ne 64) { throw 'Ed25519 artifact signature must contain 64 bytes' }
        [IO.File]::WriteAllBytes((Join-Path $TempDir 'artifact-signature.bin'), $rawArtifactSignature)
        & openssl dgst -sha256 -binary -out (Join-Path $TempDir 'artifact.digest') $Candidate
        if ($LASTEXITCODE -ne 0) { throw 'Could not hash agent artifact' }
        if (-not (Test-Ed25519AnyKey -DigestPath (Join-Path $TempDir 'artifact.digest') `
                -SignaturePath (Join-Path $TempDir 'artifact-signature.bin') -Keys $publicKeys -WorkDir $TempDir)) {
            throw 'Ed25519 artifact signature verification failed (no pinned key matched)'
        }
    } else {
        Write-Warning 'Lab mode without an update key verifies only the downloaded checksum manifest.'
    }

    if (Test-Path $Exe) {
        $currentVersion = (& $Exe -print-version).Trim()
        if ($LASTEXITCODE -ne 0 -or -not $currentVersion) { throw 'Installed agent version could not be read' }
        & $Candidate -check-upgrade-from $currentVersion
        if ($LASTEXITCODE -ne 0) { throw 'Refusing agent downgrade' }
        $hadOld = $true
    }

    Write-Host 'Installing agent transactionally...'
    $transactionStarted = $true
    $existingService = Get-CimInstance Win32_Service -Filter "Name='rmm-agent'" -ErrorAction SilentlyContinue
    if ($existingService -and -not $hadOld) { throw 'Service exists but its executable is missing' }
    if ($existingService) {
        $serviceExisted = $true
    }
    if ($hadOld) {
        if ($serviceExisted) {
            Stop-Service -Name 'rmm-agent' -Force
            (Get-Service -Name 'rmm-agent').WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
            $oldServicePath = (Get-CimInstance Win32_Service -Filter "Name='rmm-agent'").PathName
        }
    }
    if (Test-Path $State) {
        Copy-Item $State $StateBackup
        $hadState = $true
    }
    if (Test-Path "$State.outbox") {
        Copy-Item "$State.outbox" $OutboxBackup
        $hadOutbox = $true
    }
    if ($hadOld) {
        Move-Item $Exe $Backup
        if (Test-Path $SignaturePath) { Move-Item $SignaturePath $SignatureBackup }
        $swapped = $true
    }
    Move-Item $Candidate $Exe
    if (Test-Path $ArtifactSignatureText) { Copy-Item $ArtifactSignatureText $SignaturePath }
    $swapped = $true

    if ($ResetIdentity) {
        Remove-Item -Force -ErrorAction SilentlyContinue $State, "$State.outbox"
    }

    $securityArgs = @('-require-tls', '-update-verify', 'strict', '-update-public-key', $UpdatePublicKey)
    if ($LabMode) {
        $securityArgs = @('-lab-mode', '-update-verify', 'off')
        if ($UpdatePublicKey) { $securityArgs = @('-lab-mode', '-update-verify', 'strict', '-update-public-key', $UpdatePublicKey) }
    }
    if (-not (Test-Path $State)) {
        if ((Invoke-Agent -server "$Ws/ws/agent" -state $State -enroll-token $Token @securityArgs -enroll-only) -ne 0) {
            throw 'Enrollment failed'
        }
    }

    Remove-Item -Force -ErrorAction SilentlyContinue $Health
    $installArgs = @('-server', "$Ws/ws/agent", '-state', $State, '-health-file', $Health) + $securityArgs
    if ($serviceExisted) {
        $newServicePath = ConvertTo-ServiceCommand -Executable $Exe -Arguments $installArgs
        & sc.exe config rmm-agent binPath= $newServicePath | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Service configuration update failed' }
        Start-Service -Name 'rmm-agent'
    } else {
        if ((Invoke-Agent @installArgs -action install) -ne 0) { throw 'Service install failed' }
        $serviceInstalledByThisRun = $true
        if ((Invoke-Agent -action start) -ne 0) { throw 'Service start failed' }
    }

    Write-Host 'Waiting for an authenticated agent connection...'
    $candidateVersion = (& $Exe -print-version).Trim()
    $healthy = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        if (Test-Path $Health) {
            try {
                $healthData = Get-Content -Raw $Health | ConvertFrom-Json
                if ($healthData.version -eq $candidateVersion -and $healthData.agent_id) { $healthy = $true; break }
            } catch { }
        }
        Start-Sleep -Seconds 1
    }
    if (-not $healthy) { throw 'Agent did not report a healthy authenticated connection' }

    $committed = $true
    Remove-Item -Force -ErrorAction SilentlyContinue $Backup, $SignatureBackup, $StateBackup, $OutboxBackup
    Write-Host 'Agent installed, connected, and healthy.'
} catch {
    if ($transactionStarted -and -not $committed) { Restore-Install }
    throw
} finally {
    if ($TempDir -and (Test-Path $TempDir)) { Remove-Item -Recurse -Force $TempDir }
}
