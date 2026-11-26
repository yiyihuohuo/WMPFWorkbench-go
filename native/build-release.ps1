# Builds the self-contained release. The native helpers (Frida worker, host,
# offline wxapkg worker) are embedded into the Go binaries, so the shipped
# package is two exes: the desktop GUI and the MCP server.
param(
    [switch]$WithRuntime,
    [string]$Out = 'dist',
    # Path to the wxapkg-dump sidecar binary to bundle. When omitted it is looked
    # up next to the repo, because the login/export features cannot work without it.
    [string]$WxapkgDumpExe = ''
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$build = Join-Path $root 'build'
$bundleBin = Join-Path $root 'native\internal\bundle\bin'
New-Item -ItemType Directory -Force -Path $build, $bundleBin | Out-Null

$env:GOPROXY = if ($env:GOPROXY) { $env:GOPROXY } else { 'https://goproxy.cn,direct' }
$env:GOSUMDB = 'off'

# 1. Native helpers (built once, then embedded).
if (-not (Test-Path (Join-Path $build 'wmpf-observer.exe'))) {
    & (Join-Path $root 'native\worker\build.ps1')
}
if (-not (Test-Path (Join-Path $root 'hostcpp\wmpfhost_integrated.exe'))) {
    Push-Location (Join-Path $root 'hostcpp')
    try { cmd /d /c build.bat } finally { Pop-Location }
}
if (-not (Test-Path (Join-Path $build 'wxapkg-offline.exe'))) {
    Push-Location (Join-Path $root 'vendor\KillWxapkg')
    try {
        go build -trimpath -ldflags='-s -w' -o (Join-Path $build 'wxapkg-offline.exe') ./cmd/offline
        if ($LASTEXITCODE) { throw 'wxapkg-offline build failed' }
    } finally { Pop-Location }
}
Copy-Item (Join-Path $build 'wmpf-observer.exe') $bundleBin -Force
Copy-Item (Join-Path $root 'hostcpp\wmpfhost_integrated.exe') $bundleBin -Force
Copy-Item (Join-Path $build 'wxapkg-offline.exe') $bundleBin -Force

# The wxapkg-dump sidecar is a Rust binary built from a sibling checkout; it
# powers QR login and package export, so shipping without it is a broken build.
if (-not $WxapkgDumpExe) {
    $parent = Split-Path -Parent $root          # repo's parent
    $grand = Split-Path -Parent $parent         # workspace root (sibling checkouts live here)
    foreach ($candidate in @(
        (Join-Path $parent 'wxapkg-dump-bin\wxapkg-dump.exe'),
        (Join-Path $parent 'wxapkg-dump\target\release\wxapkg-dump.exe'),
        (Join-Path $grand 'wxapkg-dump-bin\wxapkg-dump.exe'),
        (Join-Path $grand 'wxapkg-dump\target\release\wxapkg-dump.exe'),
        (Join-Path $root 'vendor\wxapkg-dump\wxapkg-dump.exe')
    )) {
        if (Test-Path -LiteralPath $candidate) { $WxapkgDumpExe = $candidate; break }
    }
}
if (-not $WxapkgDumpExe -or -not (Test-Path -LiteralPath $WxapkgDumpExe)) {
    throw 'wxapkg-dump.exe not found. Build it (cargo build --release -p wxapkg-cli) and pass -WxapkgDumpExe <path>; login/export require it.'
}
Copy-Item -LiteralPath $WxapkgDumpExe (Join-Path $bundleBin 'wxapkg-dump.exe') -Force
Write-Output ("bundled wxapkg-dump.exe: {0}" -f $WxapkgDumpExe)

# 2. Icon from the original project (no custom artwork).
$rsrc = (Get-Command rsrc -ErrorAction SilentlyContinue).Source
if (-not $rsrc) { $rsrc = Join-Path (go env GOPATH) 'bin\rsrc.exe' }
$syso = Join-Path $root 'native\cmd\workbench-gui\rsrc.syso'
if (Test-Path $rsrc) {
    & $rsrc -ico (Join-Path $root 'native\cmd\workbench-gui\assets\app_icon.ico') -o $syso
    if ($LASTEXITCODE) { throw 'icon resource generation failed' }
}

# 3. Single Go executable with embedded helpers (GUI + MCP stdio/http via -mcp).
Push-Location (Join-Path $root 'native')
try {
    go build -trimpath -tags production -ldflags '-H windowsgui -s -w' -o (Join-Path $build 'wmpf-gui.exe') ./cmd/workbench-gui
    if ($LASTEXITCODE) { throw 'wmpf-gui build failed' }
} finally { Pop-Location }

# 4. Package: the self-contained GUI executable plus notices.
$trim = Join-Path $root (Join-Path $Out 'trim')
New-Item -ItemType Directory -Force -Path $trim | Out-Null
Copy-Item (Join-Path $build 'wmpf-gui.exe') $trim -Force
Copy-Item (Join-Path $root 'LICENSE') $trim -Force
Copy-Item (Join-Path $root 'NOTICE') $trim -Force
Copy-Item (Join-Path $root 'docs\THIRD_PARTY_NOTICES.md') $trim -Force

$manifest = Get-ChildItem $trim -File | ForEach-Object {
    [pscustomobject]@{ name = $_.Name; bytes = $_.Length; sha256 = (Get-FileHash $_.FullName -Algorithm SHA256).Hash }
}
$manifest | ConvertTo-Json -Depth 3 | Set-Content (Join-Path $trim 'manifest.json') -Encoding UTF8
$total = ($manifest | Measure-Object bytes -Sum).Sum
Write-Output ("trim: {0} files, {1} bytes" -f $manifest.Count, $total)

if ($WithRuntime) {
    $runtimeSource = Join-Path $root 'runtime'
    if (-not (Test-Path $runtimeSource)) { throw 'runtime directory not found' }
    $full = Join-Path $root (Join-Path $Out 'full')
    New-Item -ItemType Directory -Force -Path $full | Out-Null
    Copy-Item (Join-Path $trim '*') $full -Force
    robocopy $runtimeSource (Join-Path $full 'runtime') /E /NFL /NDL /NJH /NJS | Out-Null
    $fullTotal = (Get-ChildItem $full -Recurse -File | Measure-Object Length -Sum).Sum
    Write-Output ("full: {0} bytes (including runtime)" -f $fullTotal)
}
