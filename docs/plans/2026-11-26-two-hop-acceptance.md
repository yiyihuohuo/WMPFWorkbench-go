# Two-hop arbitrary launch acceptance

Verified session:

```text
session_id = 08188f640135a074
requested_appid = wxacdc0917b2fdfe6e
listing_state = unlisted
launch_mode = two_hop
```

Launch call:

```json
{
  "appid": "wxacdc0917b2fdfe6e",
  "seconds": 0,
  "hide_window": false,
  "wait_ready": true,
  "restart_on_failure": true
}
```

Result:

```text
appid           = wxacdc0917b2fdfe6e
active_appid    = wxacdc0917b2fdfe6e
requested_appid = wxacdc0917b2fdfe6e
jumped          = true
bridge_hidden   = true
bridge_closed   = true
state           = debug_ready
cdp_websocket   = ws://127.0.0.1:62000
```

## Verified capabilities

- Unlisted app is detected by `pcyyb_get_app_detail`.
- Launch is routed through a hidden bridge app.
- Direct `WeixinJSBridge.invoke('navigateToMiniProgram')` is used.
- Target appid becomes the active appid.
- Session display uses the target appid, not the bridge appid.
- `bridge_hidden` and `bridge_closed` are true after target activation.
- `cdp_connect` succeeds after the two-hop jump.
- `http://127.0.0.1:62000/json/list` serves CDP discovery JSON for diagnostics.
- `runtime_status` shows target appid and two-hop fields.
- GUI renders a direct Chrome DevTools frontend URL:

  ```text
  devtools://devtools/bundled/inspector.html?ws=127.0.0.1:62000
  ```

  and `OpenURL` allows `devtools://` / `chrome-devtools://` schemes.
- `go test ./...` passes.

## Notes

- `hide_window=true` is used internally for the bridge app; after target active,
  the worker reads an unhide flag and stops hiding owned windows.
- The target package is downloaded by WMPF into runtime-profile package storage.
- All 19 MCP tools were reviewed in
  `2026-11-26-two-hop-mcp-adaptation-plan.md`.
