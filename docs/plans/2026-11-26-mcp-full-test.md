# MCP full tool test

All 19 MCP tools were invoked through the desktop MCP endpoint.

## Results

| Tool | Result |
|---|---|
| `search_mini_programs` | PASS, local search returned a structured result |
| `get_app_detail` | PASS, unlisted app classified as `unlisted` with local package metadata |
| `launch` | FAIL, context deadline: login/AutoLogin succeeded but no current appid `devtools.Start` or `miniapp.setup` arrived |
| `close_runtime` | PASS, reports unknown session for an empty session id |
| `jump_mini_program` | Reached argument/tool path, but CDP was not connected in that session |
| `login_status` | PASS, structured diagnostics present |
| `cdp_local_targets` | PASS, returns owned-runtime target list |
| `cdp_connect` | Correctly failed when no runtime target was connected |
| `cdp_evaluate` | Correctly returned `not_connected` |
| `cdp_send` | Correctly returned `not_connected` |
| `cdp_events` | Correctly returned `not_connected` |
| `cdp_scripts` | Correctly returned `not_connected` |
| `extract_api_surface` | PASS |
| `decrypt_wxapkg` | PASS, `action=list` |
| `index` | PASS, `action=stats` |
| `runtime_status` | PASS |
| `netlog` | PASS, `action=discover` |
| `export_mini_program` | PASS, blocked as expected without appid/username |
| `export_status` | PASS, `action=list` |

## Notes

- CDP failures were caused by a runtime-target state, not by tool dispatch:
  the bridge websocket existed, but no applet debug target was live.
- A second launch with `wait_ready=false` produced a running session with a
  bridge websocket, but `cdp_connect` still reported `CDP runtime not connected`,
  and the remaining CDP tools correctly reported `not_connected`.
- The launch failure path showed `GetUserInfo status=2`, then successful
  AutoLogin recovery to `status=0`, but only `preload-5`/`preload-6`
  `devtools.Start` events appeared; no current-app start/setup event arrived.

## Fix and retest

Two launch-state bugs were fixed after the first full test:

1. `loginOK` now accepts typed host stdout from AutoLogin:
   `[authcb] Login ... error=0`, `[authcb] GetUserInfo status=0`, and the
   post-AutoLogin recovery line. Previously readiness only accepted
   `ilink.ThirdAppLogin.result code=0`, so AutoLogin sessions could reach
   `miniapp.setup` but never become ready.
2. The host now rechecks `GetUserInfo` after AutoLogin up to eight times and
   only skips ManualLogin when runtime state actually reaches `status=0`.
   If AutoLogin callback succeeds but the runtime remains `status=2`, the host
   falls back to the official manual login buffer path.

Retest results:

```text
listed launch   -> debug_ready, login_ok=true, app_start_seen=true, setup_seen=true, cdp_ok=true
cdp_connect     -> PASS
cdp_evaluate    -> returns 2
cdp_send        -> returns 4
cdp_events      -> PASS
cdp_scripts     -> PASS
unlisted launch -> two_hop, active target appid, jumped=true, bridge_hidden/closed=true, debug_ready
```

`jump_mini_program` legacy DOM path still reports `navigateToMiniProgram:fail cancel`;
the product two-hop path is `launch(unlisted appid)`, which now passes.

## DevTools frontend URL

Command-line `devtools://` navigation is blocked by Chromium and produced a
blank tab. The GUI now starts a headless Chromium with a workbench-local profile
and `--remote-debugging-port=0`, reads its `DevToolsActivePort`, then opens:

```text
http://127.0.0.1:<frontend-port>/devtools/inspector.html?ws=<cdp-endpoint>
```

This uses Chromium's bundled DevTools frontend and connects it to the active
WMPF CDP websocket bridge. The GUI session row now displays the raw websocket
endpoint and the button passes it to `OpenDebugger`.

## Bridge mini program shutdown

The two-hop launcher app is now actually closed after the target app becomes
active. The host watches:

```text
WMPF_CLOSE_APPID_FILE
```

and calls:

```text
IAppletManagerV3::CloseApplet(launcher_appid)
```

Observed host log:

```text
[close] CloseApplet appid=wx1644df8e4fe6f725 ret=0xcd4ff001
```

After closing the target app with `Page.close`, `cdp_connect` returns:

```text
CDP runtime not connected
```

It no longer falls back to the bridge mini program.

## Sensitive data

No account identifiers, token values, cookies, or account file paths are
included in this report or in the published clean branch.
