# Native observer build and verification

This isolated C++ process replaces Python Frida orchestration. The existing JS
agents are compiled into the executable; the C++ host remains a separate process.
This stage is a local integration prototype, not a distributable release.

## Reproducible inputs

- MSVC 2022 x64, Windows SDK, C++17, static CRT.
- Frida core devkit **17.2.17**, windows-x86_64:
  https://github.com/frida/frida/releases/download/17.2.17/frida-core-devkit-17.2.17-windows-x86_64.exe
- Archive SHA-256:
  `5f6a5170629dcec99e2958f42b4e35c79abef2d16e9fd3627a3f900b54005477`
- Extract into `build/deps/frida-core-17.2.17`; never execute an unverified archive.
- `native/worker/build.ps1` embeds the checked-in agents and builds
  `build/wmpf-observer.exe`. Generated objects/headers and devkit are ignored.

```powershell
./native/worker/build.ps1
cd native
$env:WMPF_TEST_OBSERVER = '<root>/build/wmpf-observer.exe'
go test ./internal/process -v -count=1
```

The test spawns only this worker's built-in fixture host and browser. It verifies
spawn, attach, child gating, scripts, resume, graceful stop, actual process-handle
and Job Object cleanup, and a hard worker crash. It does not start an official
client or demonstrate real WMPF debug capability.

Go places the worker in a kill-on-close job before creating its gate file. The
worker places the suspended host in another kill-on-close job before resume.
Only retained process handles are terminated; an unrelated PID is never killed.
Go must impose an outer shutdown deadline because native Frida synchronous calls
can block. `cleanup.verified` requires actual signaled handles and zero job count;
absence of this event is not proof of cleanup.

## Distribution constraints

Frida core: https://github.com/frida/frida-core/tree/17.2.17
Frida Gum: https://github.com/frida/frida-gum/tree/17.2.17
The devkit bundles dependencies, including GLib (LGPL-2.1-or-later in its header).
Frida core/Gum and every bundled dependency require a complete license/source
inventory before distribution. With static linkage, preserve matching sources,
build recipes and relinkable application objects where the applicable LGPL
requires them. Isolating the worker does not remove license obligations. Do not
apply anti-reverse protection to third-party library code or required relinkable
objects. See `docs/FRIDA_RELINK.md` for the corresponding-source, build recipe
and relinkable-object instructions that satisfy these obligations.

The static binary is currently about 64.6 MiB. Do not advertise a smaller total
application until measuring the complete release, including Runtime and WebView2.
