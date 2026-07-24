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
# Ed25519 signature verification runs in-process via this small managed verifier
# (no OpenSSL, so there is nothing extra to install or patch on every endpoint).
# It is embedded in this trusted script — independent of the untrusted download,
# so the agent binary never self-verifies. The implementation is cross-checked
# against Go's crypto/ed25519 in CI; the startup self-test below fails the install
# closed if the verifier ever accepts a bad or rejects a good signature.
$edCs = @'
using System;
using System.Numerics;
using System.Security.Cryptography;

public static class RmmEd25519 {
    static readonly BigInteger P = BigInteger.Pow(2, 255) - 19;
    static readonly BigInteger L = BigInteger.Pow(2, 252) + BigInteger.Parse("27742317777372353535851937790883648493");
    static BigInteger Mod(BigInteger a) { BigInteger r = a % P; if (r.Sign < 0) r += P; return r; }
    static BigInteger ModL(BigInteger a) { BigInteger r = a % L; if (r.Sign < 0) r += L; return r; }
    static BigInteger Inv(BigInteger x) { return BigInteger.ModPow(Mod(x), P - 2, P); }
    static readonly BigInteger D = Mod(-121665 * Inv(121666));
    static readonly BigInteger I = BigInteger.ModPow(BigInteger.Parse("2"), (P - 1) / 4, P);
    static readonly BigInteger By = Mod(4 * Inv(5));
    static readonly BigInteger Bx = RecoverX(By, 0).Value;
    static readonly BigInteger[] B = new BigInteger[] { Bx, By, BigInteger.One, Mod(Bx * By) };

    static BigInteger? RecoverX(BigInteger y, int sign) {
        if (y >= P) return null;
        BigInteger y2 = Mod(y * y);
        BigInteger x2 = Mod((y2 - 1) * Inv(Mod(D * y2 + 1)));
        if (x2 == 0) { if (sign != 0) return null; return BigInteger.Zero; }
        BigInteger x = BigInteger.ModPow(x2, (P + 3) / 8, P);
        if (Mod(x * x - x2) != 0) x = Mod(x * I);
        if (Mod(x * x - x2) != 0) return null;
        if (((int)(x & 1)) != sign) x = P - x;
        return x;
    }
    static BigInteger[] Add(BigInteger[] p1, BigInteger[] p2) {
        BigInteger a = Mod((p1[1] - p1[0]) * (p2[1] - p2[0]));
        BigInteger b = Mod((p1[1] + p1[0]) * (p2[1] + p2[0]));
        BigInteger c = Mod(p1[3] * 2 * D * p2[3]);
        BigInteger dd = Mod(p1[2] * 2 * p2[2]);
        BigInteger e = b - a, f = dd - c, g = dd + c, h = b + a;
        return new BigInteger[] { Mod(e * f), Mod(g * h), Mod(f * g), Mod(e * h) };
    }
    static BigInteger[] Mul(BigInteger s, BigInteger[] pt) {
        BigInteger[] q = new BigInteger[] { BigInteger.Zero, BigInteger.One, BigInteger.One, BigInteger.Zero };
        BigInteger[] p = pt;
        while (s > 0) {
            if ((s & 1) == 1) q = Add(q, p);
            p = Add(p, p);
            s >>= 1;
        }
        return q;
    }
    static byte[] To32LE(BigInteger n) {
        byte[] a = n.ToByteArray();
        byte[] r = new byte[32];
        int len = Math.Min(a.Length, 32);
        Array.Copy(a, r, len);
        return r;
    }
    static BigInteger IntLE(byte[] b) {
        byte[] a = new byte[b.Length + 1];
        Array.Copy(b, a, b.Length);
        return new BigInteger(a);
    }
    static byte[] Encode(BigInteger[] pt) {
        BigInteger zi = Inv(pt[2]);
        BigInteger x = Mod(pt[0] * zi);
        BigInteger y = Mod(pt[1] * zi);
        byte[] enc = To32LE(y);
        enc[31] |= (byte)(((int)(x & 1)) << 7);
        return enc;
    }
    static bool CtEquals(byte[] a, byte[] b) {
        if (a.Length != b.Length) return false;
        int diff = 0;
        for (int i = 0; i < a.Length; i++) diff |= a[i] ^ b[i];
        return diff == 0;
    }
    public static bool Verify(byte[] pub, byte[] msg, byte[] sig) {
        if (pub == null || pub.Length != 32) return false;
        if (sig == null || sig.Length != 64) return false;
        byte[] pk = (byte[])pub.Clone();
        int aSign = (pk[31] >> 7) & 1;
        pk[31] &= 0x7f;
        BigInteger ay = IntLE(pk);
        BigInteger? ax = RecoverX(ay, aSign);
        if (ax == null) return false;
        BigInteger[] A = new BigInteger[] { ax.Value, ay, BigInteger.One, Mod(ax.Value * ay) };

        byte[] Rraw = new byte[32]; Array.Copy(sig, 0, Rraw, 0, 32);
        byte[] Sraw = new byte[32]; Array.Copy(sig, 32, Sraw, 0, 32);
        BigInteger S = IntLE(Sraw);
        if (S >= L) return false;

        byte[] hin = new byte[64 + msg.Length];
        Array.Copy(Rraw, 0, hin, 0, 32);
        Array.Copy(pub, 0, hin, 32, 32);
        Array.Copy(msg, 0, hin, 64, msg.Length);
        byte[] hh;
        using (var sha = SHA512.Create()) hh = sha.ComputeHash(hin);
        BigInteger k = ModL(IntLE(hh));

        BigInteger[] sB = Mul(S, B);
        BigInteger[] kA = Mul(k, A);
        BigInteger[] negkA = new BigInteger[] { Mod(-kA[0]), kA[1], kA[2], Mod(-kA[3]) };
        BigInteger[] Rcheck = Add(sB, negkA);
        return CtEquals(Encode(Rcheck), Rraw);
    }
}
'@
$edArgs = @{ TypeDefinition = $edCs; Language = 'CSharp' }
if ($PSVersionTable.PSEdition -ne 'Core') {
    # Windows PowerShell 5.1 (.NET Framework) must be told to reference System.Numerics;
    # PowerShell 7 (.NET) references BigInteger by default.
    Add-Type -AssemblyName System.Numerics
    $edArgs['ReferencedAssemblies'] = @('System.Numerics')
}
Add-Type @edArgs

function ConvertFrom-HexString([string]$h) {
    $b = New-Object byte[] ($h.Length / 2)
    for ($i = 0; $i -lt $b.Length; $i++) { $b[$i] = [Convert]::ToByte($h.Substring($i * 2, 2), 16) }
    return , $b
}
# Fail-closed self-test: RFC 8032 test vector 1 (empty message) must verify, and a
# single-bit-flipped copy must be rejected. Guards against a broken verifier.
$stPub = ConvertFrom-HexString 'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'
$stSig = ConvertFrom-HexString 'e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b'
$stMsg = New-Object byte[] 0
if (-not [RmmEd25519]::Verify($stPub, $stMsg, $stSig)) {
    throw 'Ed25519 self-test failed: a valid RFC 8032 signature was rejected. Aborting for safety.'
}
$stBad = [byte[]]$stSig.Clone(); $stBad[0] = $stBad[0] -bxor 1
if ([RmmEd25519]::Verify($stPub, $stMsg, $stBad)) {
    throw 'Ed25519 self-test failed: a tampered signature was accepted. Aborting for safety.'
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

function Get-Sha256Bytes([string]$Path) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $fs = [IO.File]::OpenRead($Path)
        try { return $sha.ComputeHash($fs) } finally { $fs.Dispose() }
    } finally { $sha.Dispose() }
}
function Test-Ed25519AnyKey {
    param(
        [Parameter(Mandatory)][byte[]]$Digest,
        [Parameter(Mandatory)][byte[]]$Signature,
        [Parameter(Mandatory)][string[]]$Keys
    )
    foreach ($k in $Keys) {
        try { $rawKey = [Convert]::FromBase64String($k) } catch { throw 'Update public key is not valid base64' }
        if ($rawKey.Length -ne 32) { throw 'Update public key must contain 32 bytes' }
        if ([RmmEd25519]::Verify($rawKey, $Digest, $Signature)) { return $true }
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
        $manifestDigest = Get-Sha256Bytes $Manifest
        if (-not (Test-Ed25519AnyKey -Digest $manifestDigest -Signature $rawSignature -Keys $publicKeys)) {
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
        $artifactDigest = Get-Sha256Bytes $Candidate
        if (-not (Test-Ed25519AnyKey -Digest $artifactDigest -Signature $rawArtifactSignature -Keys $publicKeys)) {
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
