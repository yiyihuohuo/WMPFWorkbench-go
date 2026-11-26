# GUI and source completeness plan

## Goal

Fix every issue found in the GUI/source review, not just the top items:

- broken sidebar title for the dynamic page;
- flat, visually weak navigation;
- dynamic analysis page is a raw tool console;
- missing loading/empty/error/detail states;
- scan report cannot open;
- decrypt_wxapkg `source` argument is advertised but ignored;
- MCP descriptions make two-hop source decryption hard to discover;
- userscript, cloud replay, wx API replay, route and diagnostics need closed
  loops;
- all changes must keep light/dark/system themes coherent and avoid growing
  `index.html` into another monolith.

## Current confirmed defects

### D1. Dynamic nav title becomes blank

`index.html` overwrites every nav label from a `titles` map, but `dynamic` is
missing. In non-compact mode `textContent` becomes `undefined`.

### D2. Flat navigation

Seven top-level buttons are mixed together. Runtime, account, analysis and
system pages have no grouping.

### D3. Dynamic analysis is a raw JSON console

`dynamic.js` sends every result to a `<pre>` with `JSON.stringify`. There are no
tables, details, counts, filters, empty states, loading states or safe repeated
action guards.

### D4. Scan report button is silently blocked

`dynamic.js` opens `file:///...`, while `Backend.OpenURL` only allows
`http://` and `https://`.

### D5. decrypt_wxapkg source is fake

Contract advertises `source=auto/runtime_profile/export`, but
`wxapkg.Options` has no Source field and `Start` ignores it.

### D6. decrypt_wxapkg appid semantics are contradictory

Description says appid is required, but schema marks it optional and the
service falls back to `activeAppID()`.

### D7. Two-hop source discovery is weak

`package.resolve` has a one-line description. Agents are not told that a
two-hop target is already present under runtime-profile and should not be
exported again.

### D8. `get_app_detail` cannot use the active session

It always requires `appid`, unlike `decrypt_wxapkg` and other tools.

### D9. Replay results are not read

Cloud and wx API replay only submit a promise and expose `submitted=true`.
They do not read `__wmpfCloudReplay` / `__wmpfWxReplay`.

### D10. Userscript reinjection is manual only

Persistent scripts are installed on demand, but new `setupContext` events do
not trigger an automatic re-apply.

### D11. Dynamic page lacks target context

No global display of active appid, launch mode, CDP state or package source.

### D12. Route page lacks grouping/search

Routes are returned as a flat list and categorized only by simple regex.

### D13. Diagnostics are static

The GUI shows a hardcoded guide list instead of deriving actions from the
current login/session diagnostic.

## Target information architecture

```text
工作台
  运行概览
  扫码与账号
  搜索与打开

分析
  动态分析
    ├ 云函数
    ├ Userscript
    ├ wx API
    ├ 路由地图
    └ 敏感扫描
  会话与调试
  运行日志

系统
  设置
  关于
```

Use a grouped sidebar with:

- 180px visual density;
- group labels;
- icon + label rows;
- current target card;
- status footer;
- accent color adapted from First-main's green/neutral palette;
- light/dark/system theme support.

## File layout

Do not keep growing `index.html` or one large `dynamic.js`.

```text
frontend/dist/css/theme.css
frontend/dist/css/nav.css
frontend/dist/css/pages/dynamic.css
frontend/dist/js/dynamic/common.js
frontend/dist/js/dynamic/cloud.js
frontend/dist/js/dynamic/userscript.js
frontend/dist/js/dynamic/wxapi.js
frontend/dist/js/dynamic/route.js
frontend/dist/js/dynamic/scan.js
frontend/dist/js/dynamic/diagnostics.js
frontend/dist/js/dynamic/index.js
```

`index.html` only keeps shell markup and navigation. Page behavior lives in
separate files.

## Workstream A: navigation and theme

- Add `dynamic` to the title map and remove unsafe `undefined` assignment.
- Convert flat nav to grouped nav.
- Add group headings and icons.
- Add current target card.
- Add theme swatches for follow-system/light/dark.
- Move shared tokens to `theme.css`, nav styles to `nav.css`.
- Keep existing page IDs and JS behavior compatible.

Acceptance:

```text
dynamic nav always has text
light/dark/system all keep contrast
sidebar groups are visually distinct
no existing page loses functionality
```

## Workstream B: dynamic page shell

- Add a second-level nav inside 动态分析.
- Add target context bar:
  - active appid
  - launch mode
  - listing state
  - CDP connection
  - package source
- Disable actions when no CDP session exists.
- Add shared loading/error/empty state helpers.
- Add copy buttons for important identifiers and JSON.

Acceptance:

```text
dynamic page tells the user what target is active
all actions display loading and error states
no action can be clicked repeatedly while pending
```

## Workstream C: cloud and wx API product UI

- Render captured calls as tables:
  - time, name/api, category, duration, status.
- Click row to show details.
- Add copy and explicit replay actions.
- Read replay result after submission.
- Add category counts and filters for wx API.
- Keep high-noise categories off by default.

Acceptance:

```text
captured calls are browsable without raw JSON
replay result is visible
cloud hook reports whether installation succeeded
```

## Workstream D: userscript lifecycle

- Add auto re-apply on `miniapp.setup` / `setupContext`.
- Store match patterns and apply only to matching active appid.
- Add source preview and enable/disable controls.
- Add syntax validation before save.
- Show last injection result.

Acceptance:

```text
enabled matching scripts re-inject after setup
disabled scripts never inject
invalid JS is rejected before persistence
```

## Workstream E: route and diagnostics

- Group routes by main package/subpackage.
- Add category filter and search.
- Show guard state.
- Derive diagnostics from current session/login diagnostic code.
- Add copyable next actions.

Acceptance:

```text
route list is searchable and grouped
guard state is visible
diagnostic guide reflects the current error
```

## Workstream F: scan and report

- Add directory picker backend method.
- Add scan progress/task state.
- Add severity filters.
- Open report through a backend method that only allows generated reports.
- Show finding detail and copy button.

Acceptance:

```text
scan directory can be chosen in GUI
report opens in default browser
report button never fails silently
```

## Workstream G: decrypt source semantics

- Add `Source` to `wxapkg.Options`.
- Implement `auto` and `runtime_profile`.
- Remove unsupported `export` from contract unless a real implementation exists.
- Make `appid` optional consistently:
  - omit means active session appid.
- Return package source and version in start response.

Acceptance:

```text
source=runtime_profile selects only runtime profile packages
source=auto selects all available local sources
appid omitted works after two-hop launch
start response exposes version and source
```

## Workstream H: MCP discoverability

Update descriptions:

- `decrypt_wxapkg`: two-hop target is already local; use this tool, do not
  export again.
- `package.resolve`: explicitly says it resolves runtime-profile packages for
  unlisted two-hop targets.
- `get_app_detail`: appid optional, uses active session.
- `launch`: independent launch precheck does not apply to two-hop/unlisted
  targets.

Add response fields:

```text
package_source
package_version
package_path
launch_mode
```

Acceptance:

```text
an agent can go from runtime_status -> package.resolve -> decrypt_wxapkg
without guessing or using export
```

## Workstream I: tests and verification

Every workstream gets:

```text
unit tests
go test ./...
build-release
real MCP/desktop smoke test
separate git commit
```

Final acceptance:

```text
no blank nav labels
dynamic page usable without raw JSON as the primary UI
light/dark/system themes coherent
scan report opens
decrypt source works
two-hop package is discoverable through MCP
all old MCP tools still pass
```
