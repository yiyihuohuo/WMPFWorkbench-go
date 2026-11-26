# First-main improvement plan

## 1. Purpose

Analyze `<workspace>\First-main` as a source of **ideas only** and plan all
worthwhile improvements for the current WMPFWorkbench project. No implementation
is included in this document. Do not copy First-main code: it is closed source
after v1.1.0 and has no LICENSE.

Current project:

```text
Go + Wails + owned WMPF runtime + CDP bridge + 40 MCP tools
```

First-main:

```text
Python + PySide6 + Frida injection + Python CDP proxy
```

The current project has a more robust architecture. The improvements below
should be implemented with Go/CDP/MCP, not by importing Frida injection or
hardcoded offsets.

## 2. Source evidence

Key First-main evidence:

```text
<workspace>\First-main\src\cloud_audit.py
<workspace>\First-main\src\cloud_audit_inject.js
<workspace>\First-main\src\userscript.py
<workspace>\First-main\src\engine.py
<workspace>\First-main\src\navigator.py
<workspace>\First-main\hook_scripts\nav_inject.js
<workspace>\First-main\src\extractor.py
<workspace>\First-main\src\extract_worker.py
<workspace>\First-main\src\wxapkg.py
<workspace>\First-main\gui.py
```

## 2a. Additional reference projects

The following projects were also used as **interaction/design references only**.
No code, assets, protocol implementation, or product behavior was copied from
them.

```text
Burp Suite / Yakit
  - request/response split view
  - raw HTTP request editing
  - method/host/path/status/size columns
  - filtering, sorting, pagination, batch selection
  - explicit replay workflow

Chrome DevTools frontend
  - inspector.html served over local HTTP
  - ws=<endpoint> query parameter for attaching to a CDP target
  - default-browser launch instead of internal devtools:// scheme
```

These references informed the wx API and cloud function capture UI and the
current DevTools launch flow. The implementation remains Go/CDP/Wails based and
uses the current project's theme, state model, and security constraints.

The current project already has:

```text
CDP bridge / protobuf wire codec
event-driven launch readiness
login error classification
19 MCP tools
two-hop arbitrary mini program launch
GUI theme and copy helpers
wxapkg decrypt/export tasks
Go unit tests
```

Those are not listed as improvements below.

---

## 3. Worthwhile improvements

### 3.1 Runtime cloud function capture and replay

Goal:

```text
Hook wx.cloud.callFunction at runtime
Capture real calls and responses
Allow explicit parameter replay
```

First-main evidence:

```text
src/cloud_audit.py:20-95     global wx.cloud.callFunction hook
src/cloud_audit.py:223-244   call/replay with parameters
src/cloud_audit_inject.js    injected hook script
```

Current gap:

```text
native/internal/source/scan.go only finds static callFunction candidates
```

Design:

- Use CDP `Runtime.evaluate` / `Page.addScriptToEvaluateOnNewDocument`.
- Inject a small wrapper around:

  ```js
  wx.cloud.callFunction
  ```

- Store each call as:

  ```json
  {
    "id": "call-...",
    "appid": "...",
    "route": "pages/...",
    "name": "functionName",
    "args": {},
    "result": {},
    "error": null,
    "captured_at": 0
  }
  ```

- Emit events through the existing CDP event stream.
- Add replay tool that calls the same function with edited args.

Safety:

```text
replay must be explicit
default disabled
redact token/password/code fields in captured args
never auto-replay
```

Suggested MCP tools:

```text
cloud_capture.enable
cloud_capture.disable
cloud_capture.list
cloud_capture.get
cloud_capture.replay
```

Tests:

- wrapper installation;
- capture event parsing;
- replay request construction;
- secret redaction.

### 3.2 Persistent Userscript injection

Goal:

```text
Register scripts that survive page/runtime/context changes.
```

First-main evidence:

```text
src/userscript.py:26-111
src/engine.py:263-296, 311-345
```

It parses Tampermonkey-like metadata:

```text
@name
@match
@run-at
@inject-into
```

and uses:

```text
Page.addScriptToEvaluateOnNewDocument
Runtime.evaluate after setupContext
```

Current gap:

```text
cdp_scripts reads existing scripts; no persistent userscript lifecycle.
```

Design:

- Store userscripts under:

  ```text
  <workbench>/data/desktop/userscripts/
  ```

- Script manifest fields:

  ```json
  {
    "id": "...",
    "name": "...",
    "match": ["*"],
    "runAt": "document_start",
    "injectInto": "page",
    "enabled": true,
    "sourceFile": "..."
  }
  ```

- On `miniapp.connected`, register scripts with CDP.
- On `setupContext`, re-inject scripts into the new context.
- On `addJsContext`, optionally inject into that context.

Suggested MCP tools:

```text
userscript.add
userscript.list
userscript.remove
userscript.enable
userscript.disable
userscript.reload
```

Tests:

- metadata parsing;
- re-injection after setupContext;
- disabled scripts are not injected;
- removal removes CDP registration.

### 3.3 wx.* API categorized capture and replay

Goal:

```text
Capture useful wx.* API calls, classify them, optionally replay.
```

First-main evidence:

```text
gui.py:1671-1777
```

Categories:

```text
native bridge
wx.request
storage
```

Design:

- Wrap selected APIs:

  ```text
  wx.request
  wx.getStorage / setStorage / removeStorage
  native bridge calls
  wx.navigateTo / redirectTo / reLaunch / switchTab
  wx.cloud.callFunction (covered by 3.1)
  ```

- Default state:

  ```text
  request        disabled
  storage        disabled
  native bridge  disabled
  navigation     enabled only when route capture is enabled
  ```

- Event fields:

  ```json
  {
    "id": "event-...",
    "category": "request",
    "api": "wx.request",
    "args": {},
    "result": {},
    "appid": "...",
    "route": "..."
  }
  ```

- Replay must be one-shot and explicit.

Suggested MCP tools:

```text
wxapi.capture.enable
wxapi.capture.disable
wxapi.capture.list
wxapi.capture.replay
```

Tests:

- category filtering;
- default disabled;
- redaction;
- replay does not recurse into another capture event.

### 3.4 Page route inventory and navigation guard

Goal:

```text
Build a route map of the running mini program and classify pages.
```

First-main evidence:

```text
hook_scripts/nav_inject.js:67-140
src/navigator.py:59-112, 142-172
```

Design:

- Read `__wxConfig` from page/app service context.
- Merge:

  ```text
  main package pages
  subpackage pages
  ```

- Classify by path/name:

  ```text
  auth
  home
  list
  detail
  form
  user
  order
  payment
  setting
  other
  ```

- Provide safe navigation chain:

  ```text
  reLaunch → switchTab → redirectTo → navigateTo
  ```

- Navigation guard:

  ```text
  optional: block forced redirects during analysis
  ```

Suggested MCP tools:

```text
route.list
route.navigate
route.guard.enable
route.guard.disable
```

Tests:

- route extraction from `__wxConfig`;
- subpackage merge;
- category heuristic;
- navigation fallback order;
- guard does not affect app launch.

### 3.5 Sensitive scan report enhancements

Goal:

```text
Turn static scanner output into actionable reports.
```

First-main evidence:

```text
src/extractor.py:711-800  custom regex
src/extractor.py:818-941  HTML report
src/extract_worker.py     subprocess progress protocol
gui.py:2016-2110          directory watch/auto decrypt/auto scan
```

Current gap:

```text
native/internal/source/scan.go gives candidate lists but no rich report.
```

Design:

- Add custom scan rules:

  ```json
  {
    "id": "rule-...",
    "name": "...",
    "pattern": "...",
    "severity": "high|medium|low|info",
    "enabled": true
  }
  ```

- Store rules under:

  ```text
  <workbench>/data/desktop/scan-rules.json
  ```

- Add HTML report generation:

  ```text
  summary by severity
  grouped findings
  copy buttons
  source file/line
  redacted matched value
  ```

- Use existing export/decrypt task manager for progress.
- Optional directory watch:

  ```text
  watch export directory
  auto decrypt new wxapkg
  auto scan decoded sources
  ```

Safety:

```text
reports must redact secrets by default
opt-in to include full matches
no automatic upload/export outside workbench
```

Suggested MCP tools:

```text
scan.rules.list
scan.rules.add
scan.rules.remove
scan.run
scan.report
scan.watch.enable
scan.watch.disable
```

Tests:

- rule persistence;
- regex validation;
- HTML report generation;
- redaction;
- directory-watch event deduplication.

### 3.6 Error-specific troubleshooting guidance

Goal:

```text
Turn error classification into concrete user actions.
```

First-main evidence:

```text
gui.py:1019-1021
```

Current project has:

```text
native/internal/core/login_error.go
```

Design:

- Add a diagnostic guide map:

  ```text
  ual_credentials_expired:
    - 重新扫码登录
    - 不要用旧 account.json
    - 重新打开工作台

  sidecar_unavailable:
    - 检查 8788 端口
    - 检查 wxapkg-dump.exe 是否存在
    - 查看 remote_home

  host_timeout:
    - 查看 preload_slot
    - 关闭占用 9421/62000 的程序
    - 只重启本 worktree 的 GUI

  bad_preload:
    - 使用 restart_on_failure
    - 检查 GUI preload pool
  ```

- Display in GUI diagnostic panel.
- Include `diagnostic_code` in MCP output.

Tests:

- each diagnostic code has a guide;
- no raw token/cookie in guide;
- GUI renders guide.

### 3.7 Runtime package discovery improvements

Goal:

```text
Make local package discovery more reliable.
```

First-main evidence:

```text
src/wxapkg.py:212-242  Win/macOS package path discovery
```

Current project is Windows-only, so do not copy macOS behavior. Worthwhile parts:

```text
auto locate runtime-profile packages
auto detect package version directories
surface package path and source in get_app_detail
```

Design:

- Central resolver already planned/partly present. Extend it to return:

  ```text
  package_source
  package_path
  version
  encrypted/plain
  ```

- Use in:

  ```text
  get_app_detail
  extract_api_surface
  decrypt_wxapkg
  export_mini_program
  ```

Tests:

- multiple version directories;
- unlisted app package;
- missing package error.

### 3.8 Typed CDP event subscription and categorization

Goal:

```text
Make injected-hook events first-class MCP events.
```

Design:

- Hook events emit structured events:

  ```text
  runtime.cloud_call
  runtime.wxapi_call
  runtime.route_change
  runtime.guard_block
  ```

- `cdp_events` can filter by these method names.
- GUI event panel can group by category.
- Store only bounded recent events with cursor.

Tests:

- filter by exact method;
- cursor behavior;
- max_bytes behavior;
- no token leakage.

---

## 4. Implementation phases and git commits

Each step must be a separate commit.

### Phase 0: foundation

```text
feat(cdp): add persistent script registration helper
feat(core): add structured runtime event model
test(cdp): event filters and redaction
```

### Phase 1: cloud function capture/replay

```text
feat(cloud): capture wx.cloud.callFunction
feat(mcp): add cloud_capture tools
test(cloud): capture and replay
docs(cloud): cloud function audit
```

### Phase 2: userscripts

```text
feat(userscript): manifest parser and storage
feat(userscript): CDP persistent injection
feat(mcp): add userscript tools
test(userscript): metadata and reinjection
docs(userscript): usage
```

### Phase 3: wx API capture/replay

```text
feat(wxapi): categorized API capture
feat(mcp): add wxapi tools
test(wxapi): category filters and redaction
docs(wxapi)
```

### Phase 4: route map and guard

```text
feat(route): extract route inventory from __wxConfig
feat(route): safe navigation fallback
feat(route): optional guard
test(route)
docs(route)
```

### Phase 5: scanner/reporting

```text
feat(scan): custom rules
feat(scan): HTML report
feat(scan): progress integration
feat(scan): optional directory watch
test(scan)
docs(scan)
```

### Phase 6: diagnostics UX

```text
feat(gui): diagnostic guide panel
feat(mcp): include guide in diagnostic output
test(core): guide coverage
docs(gui): troubleshooting
```

### Phase 7: package discovery

```text
feat(package): central resolver source metadata
feat(mcp): expose package source/path/version
test(package)
docs(package)
```

---

## 5. Acceptance criteria

```text
cloud calls can be captured and replayed explicitly
userscripts survive setupContext/page changes
wx API capture is categorized and default-disabled where noisy
route inventory lists main/subpackage pages
navigation guard can be enabled/disabled
scan rules are configurable and HTML reports are generated
sensitive values are redacted by default
diagnostic codes map to concrete GUI guidance
package tools report source/path/version consistently
all new MCP tools have contract coverage and tests
all commits are atomic and reversible
```

## 6. Anti-patterns to avoid

```text
Do not copy First-main source code.
Do not use Frida hardcoded offsets.
Do not bind services to 0.0.0.0 without auth.
Do not force scene hijacking or debugger skip-pause behavior.
Do not silently swallow exceptions.
Do not build a monolithic GUI.
Do not auto-replay captured calls.
Do not expose raw tokens/cookies in reports or events.
Do not copy macOS-specific behavior into the Windows-only product.
```

## 7. Final note

First-main is useful as a feature checklist for runtime dynamic analysis.
The current project should implement these ideas through its own architecture:

```text
Go service layer
CDP bridge
MCP tools
Wails GUI
unit tests
```

No Frida injection route should be introduced.
