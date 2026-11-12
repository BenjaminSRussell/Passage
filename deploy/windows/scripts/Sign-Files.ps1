<#
  Authenticode-sign files with signtool (SHA-256, RFC 3161 timestamp). The certificate comes from a base64 PFX in an environment
  variable (the workflow maps the repository secrets onto them); it is written to a temp file that is deleted afterwards.
    Sign-Files.ps1 -Path out\bin\*.exe,out\Passage.msi
  Environment: SIGNING_PFX_BASE64, SIGNING_PFX_PASSWORD, TIMESTAMP_URL (default DigiCert).
#>
param([Parameter(Mandatory)] [string[]] $Path)
$ErrorActionPreference = 'Stop'
if (-not $env:SIGNING_PFX_BASE64) { throw 'SIGNING_PFX_BASE64 is not set: cannot sign.' }
$ts = if ($env:TIMESTAMP_URL) { $env:TIMESTAMP_URL } else { 'http://timestamp.digicert.com' }
$signtool = (Get-ChildItem -Path "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" | Sort-Object FullName -Descending | Select-Object -First 1).FullName
if (-not $signtool) { throw 'signtool.exe not found (Windows SDK).' }
$pfx = Join-Path $env:RUNNER_TEMP 'signing.pfx'
[IO.File]::WriteAllBytes($pfx, [Convert]::FromBase64String($env:SIGNING_PFX_BASE64))
try {
    $files = @(Get-ChildItem -Path $Path -File)
    if ($files.Count -eq 0) { throw "nothing to sign under $Path" }
    foreach ($f in $files) {
        & $signtool sign /fd SHA256 /tr $ts /td SHA256 /f $pfx /p $env:SIGNING_PFX_PASSWORD $f.FullName
        if ($LASTEXITCODE -ne 0) { throw "signtool failed on $($f.Name)" }
    }
} finally {
    Remove-Item -Force -ErrorAction SilentlyContinue $pfx
}
