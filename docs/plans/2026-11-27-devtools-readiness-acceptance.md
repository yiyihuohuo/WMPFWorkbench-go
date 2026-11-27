# DevTools readiness fix acceptance

## Fixes

- Debugger action is shown only when session state is `debug_ready` or
  `debug_connected`. Otherwise the GUI shows `等待就绪`.
- GUI launch now passes `wait_ready=true` and `restart_on_failure=true` with a
  120 second watchdog. The launch button stays disabled until the final state.
- DevTools frontend now uses a unique OS temp profile instead of a fixed
  workbench profile.
- Only the exact headless Chrome/Edge process started by this app is stopped,
  via its recorded PID/tree. No process-name kill is used.
- stdout/stderr are captured into a sidecar log.
- `DevToolsActivePort` is parsed and `GET /json/version` is verified before the
  frontend URL is opened.
- Startup timeout increased from 8s to 20s.
- Error message now points to the frontend log instead of misleadingly saying
  only to install Chrome/Edge.

## Verification

```text
go test ./...             PASS
build-release             PASS
```

MCP calls:

```text
login_status              PASS
launch(wait_ready=true)   state=debug_ready
runtime_status            state=debug_ready, setup_seen=true
cdp_connect               PASS
cdp_evaluate              1+1 => 2
```

Observed session:

```text
session_id   = b6bc3d2dc2328266
active_appid = wxacdc0917b2fdfe6e
launch_mode  = two_hop
state        = debug_ready
```

GUI verification:

```text
打开调试器 button visible only after debug_ready
clicking opened/started isolated headless Chrome frontend
profile = %TEMP%\wmpf-devtools-frontend-*
```

No broad Chrome/Edge process kill was used.
