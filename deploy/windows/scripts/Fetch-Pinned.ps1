<#
  Download one pinned third-party archive or installer and fail unless its SHA-256 matches. Used by the Windows workflow for the
  optional runtimes (Node, python.org embeddable), the Temporal server and the EDB PostgreSQL installer. Nothing is fetched without a hash.
    Fetch-Pinned.ps1 -Url https://... -Sha256 <hex> -Out C:\path\file [-ExpandTo C:\dir]
  -ExpandTo understands .zip and .tar.gz/.tgz (Temporal's Windows release is a tar.gz); anything else is refused so a wrong archive type fails here.
#>
param(
    [Parameter(Mandatory)] [string] $Url,
    [Parameter(Mandatory)] [string] $Sha256,
    [Parameter(Mandatory)] [string] $Out,
    [string] $ExpandTo
)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Out) | Out-Null
Invoke-WebRequest -Uri $Url -OutFile $Out -UseBasicParsing
$actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Out).Hash
if ($actual -ne $Sha256.ToUpperInvariant()) {
    Remove-Item -Force $Out
    throw "SHA-256 mismatch for $Url`: expected $Sha256, got $actual"
}
if ($ExpandTo) {
    New-Item -ItemType Directory -Force -Path $ExpandTo | Out-Null
    $name = $Out.ToLowerInvariant()
    if ($name.EndsWith('.zip')) {
        Expand-Archive -LiteralPath $Out -DestinationPath $ExpandTo -Force
    } elseif ($name.EndsWith('.tar.gz') -or $name.EndsWith('.tgz')) {
        tar -xzf $Out -C $ExpandTo
        if ($LASTEXITCODE -ne 0) { throw "tar failed on $Out with exit code $LASTEXITCODE" }
    } else {
        throw "cannot expand $Out: only .zip and .tar.gz are supported (name the -Out file after the archive type)"
    }
}
Write-Output "verified $Out"
