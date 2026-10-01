# Verify that the installer's directory ACL leaves standard users no access to
# the agent directory or to a state file that already exists in it. state.json
# holds the agent's bearer token, so read access is impersonation of the device.
# Loads Set-AgentDirectoryAcl from the installer without running the installer.
$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot '../deploy/public/install.ps1'
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile(
    (Resolve-Path $installer), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw "Installer has $($errors.Count) parse error(s)" }
$matches = @($ast.FindAll({ param($node)
    $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Set-AgentDirectoryAcl'
}, $true))
if ($matches.Count -ne 1) { throw 'Expected one Set-AgentDirectoryAcl helper' }
. ([scriptblock]::Create($matches[0].Extent.Text))

$users = New-Object Security.Principal.SecurityIdentifier('S-1-5-32-545')
$system = New-Object Security.Principal.SecurityIdentifier('S-1-5-18')
function Get-Grantees([string]$Path) {
    @((Get-Acl -Path $Path).Access |
        Where-Object { $_.AccessControlType -eq 'Allow' } |
        ForEach-Object { $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value })
}

$dir = Join-Path ([IO.Path]::GetTempPath()) ("rmm-acl-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $dir | Out-Null
try {
    # An endpoint installed by an older installer: Users may read, inherited.
    $acl = Get-Acl -Path $dir
    $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule(
        $users, 'ReadAndExecute', $inherit, 'None', 'Allow')))
    Set-Acl -Path $dir -AclObject $acl
    $state = Join-Path $dir 'state.json'
    Set-Content -Path $state -Value '{"agent_token":"x"}'
    if ((Get-Grantees $state) -notcontains $users.Value) { throw 'Setup: state.json should start readable by Users' }

    Set-AgentDirectoryAcl -Path $dir 3>$null

    foreach ($path in @($dir, $state)) {
        $grantees = Get-Grantees $path
        if ($grantees -contains $users.Value) { throw "$path is still readable by BUILTIN\Users" }
        if ($grantees -notcontains $system.Value) { throw "$path lost SYSTEM access" }
    }
    Write-Host 'Agent directory ACL checks passed.'
} finally {
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $dir
}
