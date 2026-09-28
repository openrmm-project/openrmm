# Verify that a stale service is removed only when its ImagePath belongs to the
# installer. Load the two pure helpers without executing the installer itself.
$ErrorActionPreference = 'Stop'
$installer = Join-Path $PSScriptRoot '../deploy/public/install.ps1'
$tokens = $null
$errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile(
    (Resolve-Path $installer), [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw "Installer has $($errors.Count) parse error(s)" }

foreach ($name in @('Get-ServiceProgram', 'Test-OwnedAgentService')) {
    $matches = @($ast.FindAll({ param($node)
        $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name
    }.GetNewClosure(), $true))
    if ($matches.Count -ne 1) { throw "Expected one $name helper" }
    . ([scriptblock]::Create($matches[0].Extent.Text))
}

$expected = Join-Path (Join-Path ([IO.Path]::GetTempPath()) 'openrmm agent') 'rmm-agent.exe'
$other = Join-Path (Join-Path ([IO.Path]::GetTempPath()) 'other agent') 'rmm-agent.exe'
foreach ($path in @("`"$expected`" -server wss://example.test", "$expected -server wss://example.test")) {
    if (-not (Test-OwnedAgentService $path $expected)) { throw 'Expected service was rejected' }
}
foreach ($path in @("`"$other`" -server wss://example.test", "$other -server wss://example.test", 'broken service path')) {
    if (Test-OwnedAgentService $path $expected) { throw 'Foreign or malformed service was accepted' }
}
Write-Host 'Windows service ownership checks passed.'
