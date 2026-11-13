<#
  Start the bundled TypeScript and Python services with the start commands the conf.d files resolve to, on the build runner (x64).
    Test-ServiceBundles.ps1 -ServerExe C:\out\bin\passage-server.exe -Repo $env:GITHUB_WORKSPACE -Temp $env:RUNNER_TEMP
  Inputs from the environment: NODE_DIR and PYTHON_DIR, as Build-ServiceBundles.ps1 left them.

  It lays out a throwaway install directory whose runtime\node and runtime\python are junctions to the bundles, copies
  deploy/windows/features/*.yaml into its conf.d, and asks `passage-server services` for the resolved children. Then:
    Python services   are started exactly as listed and must answer their readiness path within 120 s.
    workflow-ts       is started exactly as listed, against a Temporal address where nothing listens: it must get as far as
                      connecting (its modules and the Temporal native bridge loaded) and fail on the connection, not on a
                      missing module. A real Temporal server is not part of the build.
    agents-ts         needs Postgres, the MCP gateway and the realm's keys to start; its entry point is imported with the
                      bundled node.exe instead (main.js starts nothing when imported), which loads its whole module graph.
  Any failure throws with the child's output.
#>
param(
    [Parameter(Mandatory)] [string] $ServerExe,
    [Parameter(Mandatory)] [string] $Repo,
    [Parameter(Mandatory)] [string] $Temp
)
$ErrorActionPreference = 'Stop'
$root = Join-Path $Temp 'bundle-test'
if (Test-Path $root) { Remove-Item -Recurse -Force $root }
$confd = Join-Path $root 'data\conf.d'
New-Item -ItemType Directory -Force -Path (Join-Path $root 'install\bin'), (Join-Path $root 'install\runtime'), $confd | Out-Null
$features = @()
if ($env:NODE_DIR) {
    New-Item -ItemType Junction -Path (Join-Path $root 'install\runtime\node') -Target $env:NODE_DIR | Out-Null
    Copy-Item (Join-Path $Repo 'deploy\windows\features\node.yaml') $confd
    $features += '  node: true'
}
if ($env:PYTHON_DIR) {
    New-Item -ItemType Junction -Path (Join-Path $root 'install\runtime\python') -Target $env:PYTHON_DIR | Out-Null
    Copy-Item (Join-Path $Repo 'deploy\windows\features\python.yaml') $confd
    $features += '  python: true'
}
if ($features.Count -eq 0) { 'no Node or Python runtime configured: nothing to test'; return }

$install = (Join-Path $root 'install') -replace '\\', '/'
$config = Join-Path $root 'data\passage.yaml'
@("installDir: $install", 'temporalHostPort: 127.0.0.1:1', 'temporalNamespace: default', 'features:') + $features | Set-Content -Path $config -Encoding utf8
$env:PASSAGE_CONFIG = $config
$env:PASSAGE_DATA_DIR = Join-Path $root 'data'
$listing = & $ServerExe services
if ($LASTEXITCODE -ne 0) { throw "passage-server services failed with exit code $LASTEXITCODE" }
$children = $listing | Out-String | ConvertFrom-Json

function Start-Child($child, [string] $log) {
    $psi = [System.Diagnostics.ProcessStartInfo]::new($child.exe)
    foreach ($a in $child.args) { $psi.ArgumentList.Add($a) }
    foreach ($p in $child.env.PSObject.Properties) { $psi.Environment[$p.Name] = $p.Value }
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.UseShellExecute = $false
    $proc = [System.Diagnostics.Process]::Start($psi)
    $out = $proc.StandardOutput.ReadToEndAsync()
    $err = $proc.StandardError.ReadToEndAsync()
    return @{ Process = $proc; Out = $out; Err = $err; Log = $log }
}
function Stop-Child($c) {
    if (-not $c.Process.HasExited) { $c.Process.Kill($true); $c.Process.WaitForExit() }
    return "$($c.Out.Result)`n$($c.Err.Result)"
}

$tested = 0
foreach ($child in $children) {
    switch -Wildcard ($child.name) {
        '*-py' {
            $c = Start-Child $child
            $url = "http://127.0.0.1:$($child.adminPort)$($child.readyPath)"
            $deadline = (Get-Date).AddSeconds(120)
            $ok = $false
            while ((Get-Date) -lt $deadline -and -not $c.Process.HasExited) {
                try {
                    if ((Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 3).StatusCode -eq 200) { $ok = $true; break }
                } catch { Start-Sleep -Milliseconds 500 }
            }
            $output = Stop-Child $c
            if (-not $ok) { throw "$($child.name) did not answer $url`: $($child.exe) $($child.args -join ' ')`n$output" }
            "$($child.name): started with '$($child.args -join ' ')' and answered $($child.readyPath)"
            $tested++
        }
        'workflow-ts' {
            $c = Start-Child $child
            $exited = $c.Process.WaitForExit(60000)
            $output = Stop-Child $c
            if ($output -match 'ERR_MODULE_NOT_FOUND|Cannot find (module|package)|PrebuildError|No prebuilt module') {
                throw "workflow-ts bundle is incomplete: $output"
            }
            if ($exited -and $output -notmatch '(?i)connect|transport') {
                throw "workflow-ts stopped for a reason other than the unreachable Temporal server: $output"
            }
            "workflow-ts: started with '$($child.args -join ' ')', loaded its modules and the native bridge, and reached the Temporal connect"
            $tested++
        }
        'agents-ts' {
            $entry = ([System.Uri]::new($child.args[0])).AbsoluteUri
            $output = & $child.exe --input-type=module -e "await import('$entry'); console.log('agents-ts modules loaded');" 2>&1 | Out-String
            if ($LASTEXITCODE -ne 0) { throw "agents-ts bundle does not load: $output" }
            "agents-ts: $($output.Trim())"
            $tested++
        }
    }
}
if ($tested -eq 0) { throw 'passage-server services listed no Node or Python service to test' }
"tested $tested bundled services"
