<#
.SYNOPSIS
    Regression test for the embedded Ed25519 verifier in deploy/public/install.ps1.

    Extracts the verifier's C# (the $edCs here-string) verbatim from the installer,
    compiles it the same way the installer does (referencing System.Numerics on
    Windows PowerShell 5.1), and cross-checks it against the real signer (rmm-sign,
    Go crypto/ed25519): every genuine signature must verify and every tampered one
    must be rejected. Also re-runs the RFC 8032 self-test. Run under both
    `powershell` (5.1) and `pwsh` (7) in CI.
#>
param(
    [string]$Installer = 'deploy/public/install.ps1',
    [Parameter(Mandatory)][string]$SignExe,
    [int]$Count = 25
)
$ErrorActionPreference = 'Stop'

$raw = Get-Content -Raw $Installer
$m = [regex]::Match($raw, "(?s)\`$edCs = @'\r?\n(.*?)\r?\n'@")
if (-not $m.Success) { throw 'Could not extract $edCs verifier from the installer' }
$edCs = $m.Groups[1].Value

$edArgs = @{ TypeDefinition = $edCs; Language = 'CSharp' }
if ($PSVersionTable.PSEdition -ne 'Core') {
    Add-Type -AssemblyName System.Numerics
    $edArgs['ReferencedAssemblies'] = @('System.Numerics')
}
Add-Type @edArgs
Write-Host "Edition=$($PSVersionTable.PSEdition) PSVersion=$($PSVersionTable.PSVersion)"

function FromHex([string]$h) {
    if (-not $h) { return , (New-Object byte[] 0) }
    $b = New-Object byte[] ($h.Length / 2)
    for ($i = 0; $i -lt $b.Length; $i++) { $b[$i] = [Convert]::ToByte($h.Substring($i * 2, 2), 16) }
    return , $b
}
function Sha256([string]$Path) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $fs = [IO.File]::OpenRead($Path); try { return $sha.ComputeHash($fs) } finally { $fs.Dispose() } }
    finally { $sha.Dispose() }
}

# RFC 8032 test vector 1 (empty message).
$rfcPub = FromHex 'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'
$rfcSig = FromHex 'e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b'
if (-not [RmmEd25519]::Verify($rfcPub, (New-Object byte[] 0), $rfcSig)) { throw 'RFC 8032 vector rejected' }
$rfcBad = [byte[]]$rfcSig.Clone(); $rfcBad[0] = $rfcBad[0] -bxor 1
if ([RmmEd25519]::Verify($rfcPub, (New-Object byte[] 0), $rfcBad)) { throw 'Tampered RFC 8032 vector accepted' }
Write-Host 'RFC 8032 self-test: OK'

# Differential test against the real signer (rmm-sign / Go crypto/ed25519).
$keys = & $SignExe -gen-key
$pub = ($keys | Where-Object { $_ -like 'RMM_UPDATE_PUBLIC_KEY=*' }) -replace '^RMM_UPDATE_PUBLIC_KEY=', ''
$env:RMM_SIGNING_KEY = (($keys | Where-Object { $_ -like 'RMM_SIGNING_KEY=*' }) -replace '^RMM_SIGNING_KEY=', '')
$pubBytes = [Convert]::FromBase64String($pub)

$rng = [System.Random]::new()
$pass = 0
for ($n = 0; $n -lt $Count; $n++) {
    $f = Join-Path ([System.IO.Path]::GetTempPath()) ("edtest-$n.bin")
    $len = 1 + $rng.Next(4096)
    $buf = New-Object byte[] $len; $rng.NextBytes($buf)
    [IO.File]::WriteAllBytes($f, $buf)
    & $SignExe $f | Out-Null
    $fields = ((Get-Content -Raw "$f.sig").Trim() -split '\s+')
    if ($fields[0] -ne 'rmm-ed25519-sha256-v1') { throw "unexpected sig format: $($fields[0])" }
    $sig = [Convert]::FromBase64String($fields[1])
    $digest = Sha256 $f

    if (-not [RmmEd25519]::Verify($pubBytes, $digest, $sig)) { throw "genuine signature #$n rejected" }
    # Flip all bits of one byte (index computed once) so the tamper always changes
    # the value — XORing a re-rolled index could otherwise be a no-op.
    $si = $rng.Next(64); $badSig = [byte[]]$sig.Clone(); $badSig[$si] = $badSig[$si] -bxor 0xFF
    if ([RmmEd25519]::Verify($pubBytes, $digest, $badSig)) { throw "tampered signature #$n accepted" }
    $di = $rng.Next(32); $badDigest = [byte[]]$digest.Clone(); $badDigest[$di] = $badDigest[$di] -bxor 0xFF
    if ([RmmEd25519]::Verify($pubBytes, $badDigest, $sig)) { throw "signature over tampered digest #$n accepted" }
    Remove-Item $f, "$f.sig" -Force -ErrorAction SilentlyContinue
    $pass++
}
Write-Host "Differential test vs rmm-sign: $pass/$Count triples OK (genuine accepted, tampered rejected)"
Write-Host 'Embedded Ed25519 verifier: PASS'
