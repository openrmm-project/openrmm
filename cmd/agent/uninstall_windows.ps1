#Requires -RunAsAdministrator
# Removes the OpenRMM agent from this endpoint.
#
# Written to C:\ProgramData\rmm-agent by the agent itself, which is what the
# Programs and Features entry runs. The agent owns this file rather than the
# installer because agents update themselves and installers do not re-run: an
# endpoint installed before this existed would otherwise never get it.
#
# `rmm-agent.exe -action uninstall` only drops the service definition. A real
# uninstall also takes the binaries, the enrolment state — which holds
# credentials that still authenticate to the server — the sidecar, the Defender
# exclusion and this entry with it.
$ErrorActionPreference = 'Stop'
$dir = Join-Path $Env:ProgramData 'rmm-agent'
$exe = Join-Path $dir 'rmm-agent.exe'

if (Get-Service -Name 'rmm-agent' -ErrorAction SilentlyContinue) {
    Stop-Service -Name 'rmm-agent' -Force -ErrorAction SilentlyContinue
    if (Test-Path $exe) { & $exe -action uninstall | Out-Null }
    # In case the service outlives the binary that registered it.
    & sc.exe delete rmm-agent | Out-Null
}

# An exclusion for a directory that no longer exists is a standing hole.
if (Get-Command Get-MpPreference -ErrorAction SilentlyContinue) {
    try {
        if (@((Get-MpPreference).ExclusionPath) -contains $dir) {
            Remove-MpPreference -ExclusionPath $dir -ErrorAction Stop
            Write-Host "Defender: removed the exclusion for $dir."
        }
    } catch {
        Write-Warning "Could not remove the Defender exclusion: $($_.Exception.Message)"
    }
}

Remove-Item -Path 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\OpenRMMAgent' `
    -Recurse -Force -ErrorAction SilentlyContinue

# This script lives in the directory it deletes, so the removal is handed to a
# detached shell that outlives it.
$removal = '/c timeout /t 3 /nobreak >nul & rmdir /s /q "' + $dir + '"'
Start-Process -WindowStyle Hidden -FilePath 'cmd.exe' -ArgumentList $removal

Write-Host 'OpenRMM agent removed.'
Write-Host 'Local removal revokes nothing: delete the device in the OpenRMM console'
Write-Host 'so its credentials stop being accepted.'
