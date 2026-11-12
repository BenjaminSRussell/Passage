<#
  Build the MSI, the Temporal MSI and the Burn bundle from an already built (and, for releases, signed) bin directory.
    Build-Packages.ps1 -Version 1.2.3 -Arch x64|arm64 -Variant x64|fips-x64|... -Out C:\out [-Fips]
  Optional inputs come from the environment: NODE_DIR, PYTHON_DIR, TEMPORAL_DIR, POSTGRES_INSTALLER (see Fetch-Optional.ps1).
  Every wix call is checked and named on failure.
#>
param(
    [Parameter(Mandatory)] [string] $Version,
    [Parameter(Mandatory)] [ValidateSet('x64', 'arm64')] [string] $Arch,
    [Parameter(Mandatory)] [string] $Variant,
    [Parameter(Mandatory)] [string] $Out,
    [switch] $Fips
)
$ErrorActionPreference = 'Stop'
$here = Join-Path $PSScriptRoot '..'
function Invoke-Wix([string] $What, [string[]] $WixArgs) {
    Write-Output "wix $($WixArgs -join ' ')"
    & wix @WixArgs
    if ($LASTEXITCODE -ne 0) { throw "$What failed with exit code $LASTEXITCODE" }
}
$bin = Join-Path $Out 'bin'
$common = @('-d', "Version=$Version", '-d', "BinDir=$bin")
if ($Fips) { $common += @('-d', 'Fips=1') }

$msi = Join-Path $Out "Passage-$Version-$Variant.msi"
$d = $common
if ($env:NODE_DIR) { $d += @('-d', "NodeDir=$env:NODE_DIR") }
if ($env:PYTHON_DIR) { $d += @('-d', "PythonDir=$env:PYTHON_DIR") }
Push-Location $here
try {
    Invoke-Wix 'Passage.msi build' (@('build', '-arch', $Arch) + $d + @('-ext', 'WixToolset.Util.wixext', '-ext', 'WixToolset.Firewall.wixext', 'Package.wxs', 'Config.wxs', '-o', $msi))
    $temporalMsi = $null
    if ($env:TEMPORAL_DIR) {
        $temporalMsi = Join-Path $Out "PassageTemporal-$Version-$Variant.msi"
        Invoke-Wix 'PassageTemporal.msi build' (@('build', '-arch', $Arch) + $common + @('-d', "TemporalDir=$env:TEMPORAL_DIR", '-ext', 'WixToolset.Util.wixext', 'Temporal.wxs', '-o', $temporalMsi))
    }
    if ($Arch -eq 'x64') {
        $b = @('-d', "Version=$Version", '-d', "PassageMsi=$msi")
        if ($Fips) { $b += @('-d', 'Fips=1') }
        if ($temporalMsi) { $b += @('-d', "TemporalMsi=$temporalMsi") }
        if ($env:POSTGRES_INSTALLER) { $b += @('-d', "PostgresInstaller=$env:POSTGRES_INSTALLER") }
        Invoke-Wix 'PassageSetup.exe bundle build' (@('build', '-arch', 'x64') + $b + @('-ext', 'WixToolset.Util.wixext', '-ext', 'WixToolset.BootstrapperApplications.wixext', 'Bundle.wxs', '-o', (Join-Path $Out "PassageSetup-$Version-$Variant.exe")))
    }
} finally {
    Pop-Location
}
