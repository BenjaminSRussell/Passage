<#
  Fail when any .exe, .dll or .msi under the given directories lacks a valid, timestamped Authenticode signature. With -AllowUnsigned (pull
  requests from forks, which have no secrets) it reports the unsigned files and passes; release builds never pass that switch.
    Test-Signed.ps1 -Root out [-AllowUnsigned]
#>
param(
    [Parameter(Mandatory)] [string[]] $Root,
    [switch] $AllowUnsigned
)
$ErrorActionPreference = 'Stop'
$bad = @()
$count = 0
foreach ($r in $Root) {
    foreach ($f in Get-ChildItem -Path $r -Recurse -File -Include *.exe, *.dll, *.msi) {
        $count++
        $sig = Get-AuthenticodeSignature -LiteralPath $f.FullName
        if ($sig.Status -ne 'Valid' -or -not $sig.TimeStamperCertificate) {
            $bad += "$($f.FullName): $($sig.Status)$(if (-not $sig.TimeStamperCertificate) { ' (no timestamp)' })"
        }
    }
}
if ($count -eq 0) { throw "no binaries found under $($Root -join ', ')" }
if ($bad.Count -gt 0) {
    $bad | ForEach-Object { Write-Output "UNSIGNED $_" }
    if ($AllowUnsigned) { Write-Output "$($bad.Count) of $count files are unsigned; allowed for this build."; exit 0 }
    throw "$($bad.Count) of $count files are not signed"
}
Write-Output "all $count binaries are signed and timestamped"
