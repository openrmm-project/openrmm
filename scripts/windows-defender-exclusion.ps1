<#
  defender-exclusion: exclude the OpenRMM agent directory from Microsoft Defender
  so the agent can install and launch its remote-desktop sidecar.

  Why this is needed: the agent downloads the sidecar to
  <ProgramData>\rmm-agent\rmm-remote.exe.new and runs it once to validate it. A
  service writing an unsigned executable and then executing it is a textbook
  heuristic trigger, and Defender's cloud verdict arrives AFTER the first copy
  has already run — so the first remote session works and every later one has
  its download quarantined mid-flight, leaving the technician with a black
  screen. OpenRMM's own Ed25519 update signature protects the update channel;
  Windows does not know that it exists.

  Scope is deliberately minimal: one path exclusion for the agent directory. No
  process exclusion (that would exempt every file those processes touch), and
  real-time protection is never disabled.

  Idempotent and auditable: prints every action, changes only what is missing.
  Run elevated. Use -Remove to revert.

  Push it through the RMM as a Script-library job, or run it locally.
#>
[CmdletBinding()]
param([switch]$Remove)

$ErrorActionPreference = 'Stop'
$P = '[defender-exclusion]'
function Log($m) { Write-Host "$P $m" }

$isAdmin = ([Security.Principal.WindowsPrincipal] [Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
    [Security.Principal.WindowsBuiltinRole]::Administrator)
if (-not $isAdmin) { Log 'ERROR: must run elevated (Run as Administrator)'; exit 1 }

if (-not (Get-Command Get-MpPreference -ErrorAction SilentlyContinue)) {
    Log 'Microsoft Defender is not present on this host; nothing to do.'
    exit 0
}

$dir = Join-Path $Env:ProgramData 'rmm-agent'
Log "agent directory: $dir"

$current = @((Get-MpPreference).ExclusionPath)
$present = $current -contains $dir
Log ("current exclusion present: " + $present)

try {
    if ($Remove) {
        if (-not $present) { Log 'nothing to remove; already absent'; exit 0 }
        Remove-MpPreference -ExclusionPath $dir
        Log 'exclusion removed'
    } else {
        if ($present) { Log 'exclusion already in place; no change'; exit 0 }
        Add-MpPreference -ExclusionPath $dir
        Log 'exclusion added'
    }
} catch {
    # Exclusions pushed by Intune or Group Policy cannot be changed locally, and
    # Tamper Protection can refuse the write. Say which, rather than failing bare.
    Log "ERROR: Defender refused the change: $($_.Exception.Message)"
    Log 'If this host is managed by Intune or Group Policy, add the exclusion there instead.'
    exit 1
}

$after = @((Get-MpPreference).ExclusionPath)
if (($after -contains $dir) -eq [bool]$Remove) {
    Log 'ERROR: Defender reported success but the exclusion list did not change'
    exit 1
}
Log 'resulting Defender path exclusions:'
$after | ForEach-Object { Log "  $_" }

if (-not $Remove) {
    $sidecar = Join-Path $dir 'rmm-remote.exe'
    if (-not (Test-Path $sidecar)) {
        Log 'sidecar not present yet — the agent downloads it on the next remote session; no restart needed.'
    }
    Log 'Quarantined earlier? Clear it with: Remove-MpThreat  (or restore from Defender > Protection history)'
}
exit 0
