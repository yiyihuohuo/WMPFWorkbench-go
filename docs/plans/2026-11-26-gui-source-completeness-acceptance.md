# GUI and source completeness acceptance

## GUI navigation

- Fixed the blank `dynamic` nav label by using a complete title map and
  `.navText` nodes instead of overwriting button text.
- Replaced flat navigation with three groups:

  ```text
  工作台
  分析
  系统
  ```

- Added icons, group labels, hover/active states and a current-target card.
- Adopted the First-main-inspired green accent while keeping the existing
  light/dark/system theme tokens.

## Dynamic analysis page

- Split the former monolithic `dynamic.js` into:

  ```text
  js/dynamic/common.js
  js/dynamic/cloud.js
  js/dynamic/userscript.js
  js/dynamic/wxapi.js
  js/dynamic/route.js
  js/dynamic/scan.js
  js/dynamic/index.js
  ```

- Added a second-level sub-navigation inside 动态分析.
- Added target context bar:
  - active appid
  - launch mode / listing state
  - CDP state
  - package source
- Replaced raw JSON result blocks with tables, details, badges and empty states.
- Added loading/disabled states for async actions.
- Scan target now defaults to the currently opened mini program; no manual
  directory selection is required. If source is not decrypted yet, scan starts
  the decrypt task automatically.
- Added safe report opening.
- Added replay-result readback for cloud and wx API.
- Added route search/grouping and guard state.
- Removed the diagnostics subpage from 动态分析 as requested.
- Expanded default scan rules from 4 to 26, covering cloud keys, tokens,
  database URLs, PII and common credentials.
- Localized all MCP tool descriptions and parameter descriptions into Chinese.

## Userscript lifecycle

- Matching enabled scripts now reapply automatically after `setupContext`.
- Script match patterns are honored when applying to the active appid.

## Source/decrypt semantics

- `wxapkg.Options` now has a real `Source` field.
- `source=auto` and `source=runtime_profile` are implemented.
- Unsupported `source=export` is rejected explicitly.
- `decrypt_wxapkg` returns:

  ```text
  source_mode
  package_sources
  version
  ```

- `package.resolve` can infer the active session appid.
- `get_app_detail` can infer the active session appid.
- MCP descriptions now explain the two-hop chain:
  `runtime_status` → `package.resolve` → `decrypt_wxapkg`, without re-export.

## Verification

```text
go test ./...  PASS
node --check dynamic modules PASS
build-release PASS
```

Real two-hop session:

```text
active_appid = wxacdc0917b2fdfe6e
state = debug_ready
launch_mode = two_hop
```

Real source chain:

```text
package.resolve appid omitted
→ appid = wxacdc0917b2fdfe6e
→ package_source = workbench/9460ec1243c7

decrypt_wxapkg action=start source=runtime_profile appid omitted
→ version = 331
→ source_mode = runtime_profile
→ package_sources = [workbench/9460ec1243c7]
→ total_packages = 2

status:
→ state = succeeded
→ done_packages = 2/2
→ restored_files = 441
```

## Result

All items in `2026-11-26-gui-source-completeness-plan.md` are implemented.
