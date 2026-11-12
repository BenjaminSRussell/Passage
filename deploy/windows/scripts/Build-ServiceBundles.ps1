<#
  Build the TypeScript and Python service bundles into the private runtimes that Fetch-Optional.ps1 unpacked, so that the MSI's
  NodeServices and PythonServices features carry the services the conf.d files (deploy/windows/features) start.
    Build-ServiceBundles.ps1 -Arch x64|arm64 -Repo $env:GITHUB_WORKSPACE
  Inputs from the environment: NODE_DIR and PYTHON_DIR (unset means the feature is not built and nothing is bundled for it).

  Node (NODE_DIR\apps\<service>): each service is built with its own lockfile (npm ci, npm run build), then its production
  dependencies are installed next to dist\ with npm ci --omit=dev. The layout is the one features\node.yaml names:
    apps\workflow-ts\dist\worker.js     (x64 only: @temporalio/core-bridge has no Windows arm64 native module)
    apps\agents-ts\dist\main.js, apps\agents-ts\migrations
  Python (PYTHON_DIR\Lib\site-packages): routing-py and scheduler-py are built as wheels and installed with their dependencies as
  Windows binary wheels for the embeddable distribution's Python version, and the distribution's ._pth file is extended so
  python.exe finds them. On arm64 a dependency without a win_arm64 wheel (OR-Tools) leaves the Python feature out of the package
  (PYTHON_DIR is cleared) rather than shipping services that cannot start.
  Test-ServiceBundles.ps1 then starts what was built.
#>
param(
    [Parameter(Mandatory)] [ValidateSet('x64', 'arm64')] [string] $Arch,
    [Parameter(Mandatory)] [string] $Repo
)
$ErrorActionPreference = 'Stop'
function Invoke-Checked([string] $What, [scriptblock] $Block) {
    & $Block
    if ($LASTEXITCODE -ne 0) { throw "$What failed with exit code $LASTEXITCODE" }
}
function Export-Env([string] $name, [string] $value) {
    if ($env:GITHUB_ENV) { "$name=$value" | Out-File -FilePath $env:GITHUB_ENV -Append -Encoding utf8 }
    [Environment]::SetEnvironmentVariable($name, $value)
}

if ($env:NODE_DIR) {
    $services = @('agents-ts')
    if ($Arch -eq 'x64') { $services = @('workflow-ts') + $services }
    else { Write-Warning 'workflow-ts is not bundled for arm64: the Temporal SDK has no Windows arm64 native module (see features\node-arm64.yaml)' }
    foreach ($svc in $services) {
        $src = Join-Path $Repo "services\$svc"
        $dest = Join-Path $env:NODE_DIR "apps\$svc"
        Push-Location $src
        try {
            Invoke-Checked "$svc npm ci" { npm ci --no-audit --no-fund }
            Invoke-Checked "$svc build" { npm run build }
        } finally { Pop-Location }
        if (Test-Path $dest) { Remove-Item -Recurse -Force $dest }
        New-Item -ItemType Directory -Force -Path $dest | Out-Null
        Copy-Item (Join-Path $src 'package.json'), (Join-Path $src 'package-lock.json') $dest
        Copy-Item -Recurse (Join-Path $src 'dist') (Join-Path $dest 'dist')
        if (Test-Path (Join-Path $src 'migrations')) { Copy-Item -Recurse (Join-Path $src 'migrations') (Join-Path $dest 'migrations') }
        Push-Location $dest
        try {
            Invoke-Checked "$svc production dependencies" { npm ci --omit=dev --no-audit --no-fund }
        } finally { Pop-Location }
        # The Temporal native bridge ships every platform's binary (about 150 MB); keep the Windows x64 one only.
        $releases = Join-Path $dest 'node_modules\@temporalio\core-bridge\releases'
        if (Test-Path $releases) {
            Get-ChildItem $releases -Directory | Where-Object { $_.Name -ne 'x86_64-pc-windows-msvc' } | Remove-Item -Recurse -Force
            if (-not (Test-Path (Join-Path $releases 'x86_64-pc-windows-msvc'))) { throw "$svc`: the Temporal native bridge for Windows x64 is missing" }
        }
        "bundled $svc into $dest"
    }
}

if ($env:PYTHON_DIR) {
    # The embeddable zip names its version: python312.zip and python312._pth for 3.12.
    $pth = Get-ChildItem $env:PYTHON_DIR -Filter 'python3*._pth' | Select-Object -First 1
    if (-not $pth) { throw "no python3*._pth in $env:PYTHON_DIR: is PYTHON_WIN_URL the python.org embeddable distribution?" }
    $tag = $pth.BaseName.Substring('python'.Length)       # 312
    $pyver = "$($tag.Substring(0, 1)).$($tag.Substring(1))" # 3.12
    $platform = if ($Arch -eq 'arm64') { 'win_arm64' } else { 'win_amd64' }
    $site = Join-Path $env:PYTHON_DIR 'Lib\site-packages'
    $wheels = Join-Path $env:RUNNER_TEMP 'py-wheels'
    New-Item -ItemType Directory -Force -Path $site, $wheels | Out-Null
    # The services themselves are pure Python: build their wheels with the runner's Python, then install them and every dependency
    # as binary wheels for the target interpreter (no source builds for another platform).
    foreach ($svc in @('routing-py', 'scheduler-py')) {
        Invoke-Checked "$svc wheel" { python -m pip wheel --no-deps --wheel-dir $wheels (Join-Path $Repo "services\$svc") }
    }
    $built = (Get-ChildItem $wheels -Filter '*.whl').FullName
    python -m pip install --target $site --platform $platform --python-version $pyver --implementation cp --only-binary=:all: --upgrade $built
    if ($LASTEXITCODE -ne 0) {
        if ($Arch -eq 'arm64') {
            Write-Warning "a Python service dependency has no $platform wheel for Python $pyver; the Python services feature is left out of the arm64 package"
            Export-Env 'PYTHON_DIR' ''
            return
        }
        throw "pip install of the Python services for $platform / $pyver failed with exit code $LASTEXITCODE"
    }
    # The ._pth file replaces sys.path in the embeddable distribution: add site-packages and turn on `import site`.
    $lines = Get-Content $pth.FullName | ForEach-Object { if ($_ -match '^\s*#\s*import site\s*$') { 'import site' } else { $_ } }
    if ($lines -notcontains 'Lib\site-packages') { $lines = @($lines[0], 'Lib\site-packages') + $lines[1..($lines.Count - 1)] }
    if ($lines -notcontains 'import site') { $lines += 'import site' }
    Set-Content -Path $pth.FullName -Value $lines -Encoding ascii
    "bundled routing-py and scheduler-py into $site for Python $pyver ($platform)"
}
