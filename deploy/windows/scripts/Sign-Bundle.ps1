<#
  Sign a Burn bundle: detach the engine, sign it, reattach, then sign the bundle. Sign-Files.ps1 supplies the certificate from the environment.
    Sign-Bundle.ps1 -Bundle C:\out\PassageSetup-1.2.3-x64.exe -Temp $env:RUNNER_TEMP
#>
param(
    [Parameter(Mandatory)] [string] $Bundle,
    [Parameter(Mandatory)] [string] $Temp
)
$ErrorActionPreference = 'Stop'
$sign = Join-Path $PSScriptRoot 'Sign-Files.ps1'
$engine = Join-Path $Temp 'engine.exe'
wix burn detach $Bundle -engine $engine
if ($LASTEXITCODE -ne 0) { throw "wix burn detach failed with exit code $LASTEXITCODE" }
& $sign -Path $engine
wix burn reattach $Bundle -engine $engine -o $Bundle
if ($LASTEXITCODE -ne 0) { throw "wix burn reattach failed with exit code $LASTEXITCODE" }
& $sign -Path $Bundle
