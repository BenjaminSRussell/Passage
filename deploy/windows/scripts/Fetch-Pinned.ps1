<#
  Download one pinned third-party archive or installer and fail unless its SHA-256 matches. Used by the Windows workflow for the
  optional runtimes (Node, python.org embeddable), the Temporal server and the EDB PostgreSQL installer. Nothing is fetched without a hash.
    Fetch-Pinned.ps1 -Url https://... -Sha256 <hex> -Out C:\path\file [-ExpandTo C:\dir]
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
    Expand-Archive -LiteralPath $Out -DestinationPath $ExpandTo -Force
}
Write-Output "verified $Out"
