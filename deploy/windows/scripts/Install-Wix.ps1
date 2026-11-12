<#
  Install the WiX dotnet tool and the extensions the packages use. Every native call is checked, so the failing call is named.
    Install-Wix.ps1 -Version 6.0.2 [-OsmfAccepted]
  Since WiX v5 the standard bootstrapper application is in WixToolset.BootstrapperApplications.wixext (plural).
#>
param(
    [Parameter(Mandatory)] [string] $Version,
    [switch] $OsmfAccepted
)
$ErrorActionPreference = 'Stop'
function Invoke-Native([string] $What, [scriptblock] $Command) {
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "$What failed with exit code $LASTEXITCODE" }
}
Invoke-Native "dotnet tool install wix $Version" { dotnet tool install --global wix --version $Version }
if ($Version -match '^(\d+)' -and [int]$Matches[1] -ge 7) {
    if (-not $OsmfAccepted) { throw 'WiX 7 and later need the Open Source Maintenance Fee terms accepted: set the WIX_OSMF_ACCEPTED variable (docs/adr/0004-windows-packaging.md).' }
    Invoke-Native 'wix eula accept wix7' { wix eula accept wix7 }
}
foreach ($e in 'Util', 'Firewall', 'BootstrapperApplications') {
    Invoke-Native "wix extension add WixToolset.$e.wixext/$Version" { wix extension add -g "WixToolset.$e.wixext/$Version" }
}
Invoke-Native 'wix extension list' { wix extension list -g }
