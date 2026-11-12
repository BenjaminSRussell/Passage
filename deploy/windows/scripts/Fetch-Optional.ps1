<#
  Fetch the optional pinned packages whose URL and SHA-256 are configured (repository variables, passed as environment variables), verify them
  and export their locations to the next workflow steps through GITHUB_ENV: NODE_DIR, PYTHON_DIR, TEMPORAL_DIR, POSTGRES_INSTALLER.
    Fetch-Optional.ps1 -Arch x64|arm64 -Temp $env:RUNNER_TEMP
  Variables: NODE_WIN_URL/_SHA256 (x64) or NODE_WIN_ARM64_URL/_SHA256 (arm64), PYTHON_WIN_URL or PYTHON_WIN_ARM64_URL likewise, and for x64 only
  TEMPORAL_WIN_URL/_SHA256 and POSTGRES_WIN_URL/_SHA256. A missing URL skips that package.
#>
param(
    [Parameter(Mandatory)] [ValidateSet('x64', 'arm64')] [string] $Arch,
    [Parameter(Mandatory)] [string] $Temp
)
$ErrorActionPreference = 'Stop'
$fetch = Join-Path $PSScriptRoot 'Fetch-Pinned.ps1'
function Get-Setting([string] $name) { [Environment]::GetEnvironmentVariable($name) }
function Export-Env([string] $name, [string] $value) { "$name=$value" | Out-File -FilePath $env:GITHUB_ENV -Append -Encoding utf8 }
function Archive-Name([string] $url, [string] $stem) {
    $path = ([Uri]$url).AbsolutePath.ToLowerInvariant()
    if ($path.EndsWith('.tar.gz') -or $path.EndsWith('.tgz')) { return "$stem.tar.gz" }
    return "$stem.zip"
}
$suffix = if ($Arch -eq 'arm64') { '_ARM64' } else { '' }

$url = Get-Setting "NODE_WIN${suffix}_URL"
if ($url) {
    & $fetch -Url $url -Sha256 (Get-Setting "NODE_WIN${suffix}_SHA256") -Out (Join-Path $Temp (Archive-Name $url 'node')) -ExpandTo (Join-Path $Temp 'node-x')
    Export-Env 'NODE_DIR' (Get-ChildItem (Join-Path $Temp 'node-x') -Directory | Select-Object -First 1).FullName
}
$url = Get-Setting "PYTHON_WIN${suffix}_URL"
if ($url) {
    & $fetch -Url $url -Sha256 (Get-Setting "PYTHON_WIN${suffix}_SHA256") -Out (Join-Path $Temp (Archive-Name $url 'python')) -ExpandTo (Join-Path $Temp 'python-x')
    Export-Env 'PYTHON_DIR' (Join-Path $Temp 'python-x')
}
if ($Arch -eq 'x64') {
    $url = Get-Setting 'TEMPORAL_WIN_URL'
    if ($url) {
        & $fetch -Url $url -Sha256 (Get-Setting 'TEMPORAL_WIN_SHA256') -Out (Join-Path $Temp (Archive-Name $url 'temporal')) -ExpandTo (Join-Path $Temp 'temporal-x')
        if (-not (Test-Path (Join-Path $Temp 'temporal-x\temporal-server.exe'))) { throw 'temporal-server.exe is not at the top of the Temporal archive' }
        Export-Env 'TEMPORAL_DIR' (Join-Path $Temp 'temporal-x')
    }
    $url = Get-Setting 'POSTGRES_WIN_URL'
    if ($url) {
        & $fetch -Url $url -Sha256 (Get-Setting 'POSTGRES_WIN_SHA256') -Out (Join-Path $Temp 'postgres-installer.exe')
        Export-Env 'POSTGRES_INSTALLER' (Join-Path $Temp 'postgres-installer.exe')
    }
}
