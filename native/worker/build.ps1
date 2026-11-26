param([string]$Devkit = "$PSScriptRoot/../../build/deps/frida-core-17.2.17")
$ErrorActionPreference='Stop'
$root=[IO.Path]::GetFullPath("$PSScriptRoot/../..")
$Devkit=[IO.Path]::GetFullPath($Devkit)
if (!(Test-Path -LiteralPath "$Devkit/frida-core.h")) {throw 'Frida 17.2.17 Windows x64 devkit missing'}
$build="$root/build/worker"
New-Item -ItemType Directory -Force -Path $build | Out-Null
$observe=[IO.File]::ReadAllText("$root/tools/observe_startup_chain.js")
$debug=[IO.File]::ReadAllText("$root/tools/activate_reused_runtime.js")
$hookPath="$root/tools/native_appid_hook.js"
$hook=''
if (Test-Path -LiteralPath $hookPath) { $hook=[IO.File]::ReadAllText($hookPath) }
$scanPath="$root/tools/native_xref_scan.js"
$scan=''
if (Test-Path -LiteralPath $scanPath) { $scan=[IO.File]::ReadAllText($scanPath) }
$header='static const char* observationSource=R"WMPFAGENT('+ $observe +')WMPFAGENT";' + "`n" + 'static const char* debugSource=R"WMPFAGENT('+ $debug +')WMPFAGENT";'
if ($hook) { $header += "`n" + '#define NATIVE_APPID_HOOK_SOURCE R"WMPFAGENT(' + $hook + ')WMPFAGENT"' }
if ($scan) { $header += "`n" + '#define NATIVE_XREF_SCAN_SOURCE R"WMPFAGENT(' + $scan + ')WMPFAGENT"' }
[IO.File]::WriteAllText("$build/agents.h",$header,[Text.UTF8Encoding]::new($false))
$vs=& "${env:ProgramFiles(x86)}/Microsoft Visual Studio/Installer/vswhere.exe" -latest -products '*' -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (!$vs) {throw 'MSVC x64 tools missing'}
$vcvars="$vs/VC/Auxiliary/Build/vcvars64.bat"
$cmd='@call "'+$vcvars+'" >nul'+"`r`n"+'@cl /nologo /EHsc /std:c++17 /utf-8 /W3 /MT /O2 /DWIN32_LEAN_AND_MEAN /I"'+$Devkit+'" /I"'+$build+'" "'+$PSScriptRoot+'/main.cpp" /Fo"'+$build+'/worker.obj" /Fe:"'+$root+'/build/wmpf-observer.exe" /link /OPT:REF /OPT:ICF "'+$Devkit+'/frida-core.lib" user32.lib advapi32.lib shell32.lib ole32.lib ws2_32.lib dnsapi.lib iphlpapi.lib psapi.lib winmm.lib shlwapi.lib crypt32.lib secur32.lib bcrypt.lib setupapi.lib'+"`r`n"+'@exit /b %errorlevel%'
[IO.File]::WriteAllText("$build/compile.cmd",$cmd,[Text.Encoding]::Default)
& cmd.exe /d /c "$build/compile.cmd"
if ($LASTEXITCODE -ne 0) {throw 'Observer build failed'}
Get-Item "$root/build/wmpf-observer.exe" | Select-Object FullName,Length
