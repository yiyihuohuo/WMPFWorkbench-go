# DevTools readiness and GUI launch fix plan

## Problem

A user screenshot showed two simultaneous symptoms:

```text
DevTools 前端服务启动超时
会话仍停在 starting
```

They are separate problems that can appear together.

## Root causes

### 1. Debugger button can be clicked too early

`renderSessions` shows 打开调试器 whenever `cdp_websocket` exists. The session
can be `running` before `miniapp.setup`, so the user can try to open DevTools
while the target is not actually debuggable.

### 2. GUI launch is asynchronous for direct launches

The GUI calls `launch` without `wait_ready`. Direct launches return `starting`
immediately. This is intentional, but the UI then looks stuck if `WaitReady`
events arrive late or never arrive.

### 3. DevTools frontend uses a fixed profile

`devtoolsFrontendURL` starts a headless Chrome/Edge with:

```text
--user-data-dir=<data>/desktop/devtools-frontend-profile
--remote-debugging-port=0
```

If an earlier headless process still holds that profile, the new process exits
or fails to write `DevToolsActivePort`. The current code waits only 8 seconds,
does not capture stderr, and does not verify the frontend over HTTP.

## Workstreams

### A. UI gating

- Show 打开调试器 only when `state === 'debug_ready'` or `debug_connected`.
- Otherwise show 等待就绪.
- Commit separately.

### B. GUI launch readiness

- Pass `wait_ready=true` from the GUI backend launch call.
- Keep the launch button disabled while waiting.
- Surface final `debug_ready`/`error` state instead of intermediate `starting`.
- Preserve existing MCP `launch` behavior.

### C. DevTools frontend service

- Use a unique temporary profile per frontend server.
- Track only the exact headless process started by this app.
- Before starting a new frontend, stop only that tracked PID/tree.
- Capture stdout/stderr to a local log file.
- Increase startup timeout from 8s to 20s.
- Read `DevToolsActivePort`, then verify `GET /json/version` succeeds.
- Clean up the temporary profile when the process is stopped.
- Replace the misleading “install Chrome or Edge” message with a precise
  diagnostic pointing to the log.

### D. Tests and verification

- Add tests for port parsing and frontend HTTP verification.
- Run `go test ./...`.
- Build release.
- Real MCP test:
  - `login_status`
  - `launch` with `wait_ready=true`
  - `runtime_status`
  - `cdp_connect`
  - `cdp_evaluate`
- Verify GUI debug button appears only after ready.

## Safety constraints

- Never kill Chrome/Edge by process name.
- Only stop the exact PID/tree recorded by this app.
- No token/cookie/personal data in logs.
- Keep all temporary profiles outside tracked repo paths.
