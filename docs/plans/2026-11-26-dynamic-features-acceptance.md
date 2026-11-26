# Dynamic analysis features acceptance

Version:

```text
1.0.5
```

## Implemented

- Runtime cloud function capture and explicit replay:
  `cloud_capture.install/list/replay`.
- Persistent userscripts:
  `userscript.add/list/remove/enable/disable/reload`.
- Categorized wx API capture and explicit replay:
  `wxapi.capture/list/replay`.
- Route inventory and navigation guard:
  `route.list/navigate/guard`.
- Scan rules, directory scan, and HTML report:
  `scan.rules/run/report`.
- Local package metadata resolver:
  `package.resolve`.
- GUI "动态分析" page in separate assets:
  - `frontend/dist/css/dynamic.css`
  - `frontend/dist/js/dynamic.js`
- GUI generic backend binding:
  `Invoke(tool, argsJSON)`.
- MCP tool count increased from 19 to 38.

## Architecture notes

- All runtime hooks are injected through CDP into the page and its mini program
  iframes. No Frida injection or hardcoded offsets were introduced.
- Hook scripts are installed with `Page.addScriptToEvaluateOnNewDocument` and
  immediately evaluated with `Runtime.evaluate`.
- Captured values are returned in full; this product is an authorized analysis tool and does not mask captured arguments/results.
- Replay is explicit only; no automatic replay is performed.
- GUI assets use existing theme variables from `index.html`, so the new page
  follows light/dark/system themes.

## Verification

```text
go test ./...             PASS
build-release             PASS
cloud_capture.install     installed=true
cloud_capture.list        calls=[]
userscript.add/list/reload PASS
wxapi.capture/list        PASS
route.list                PASS, routes from __wxConfig
route.guard               PASS
package.resolve           PASS, package_source=workbench/...
scan.rules/run/report     PASS
```

Real two-hop launch remained healthy:

```text
launch_mode = two_hop
active_appid = wxacdc0917b2fdfe6e
state = debug_ready
cdp_websocket = ws://127.0.0.1:62000
```

Temporary userscripts created during testing were removed.

## Result

All worthwhile First-main ideas planned in
`2026-11-26-first-main-improvement-plan.md` are now implemented in the current
Go/CDP/MCP/Wails architecture, without copying First-main code.
