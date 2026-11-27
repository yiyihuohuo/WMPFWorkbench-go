# repeated_reset hardening plan

## Problem

A GUI launch failed with:

```text
自动失败：repeated_reset: count=3
启动失败：repeated_reset: count=3
```

The failure is caused by three `host.Reset` events from the native WMPF host.
The current error reports only the count and gives no phase or launch-state
context. The reset rule also fails immediately at three resets regardless of
whether the launch has progressed.

## Verified facts

- `game_type.txt` is already read by `native/worker/main.cpp` and passed as
  `WMPF_GAME_TYPE`; it is not the missing link.
- `CloseSession` waits for `s.done`, and the worker cleanup waits for an
  explicit cleanup verdict before the process is stopped; per-session precise
  cleanup before retry is already implemented.
- `restart_on_failure=true` makes the first recoverable failure restart once.
  The two GUI log lines are the first failure and the final failure after
  retry, not two independent bugs.

## Workstreams

### 1. Typed diagnostics

Extend `ReadyFailure` with:

```text
Phase
ResetCount
LoginOK
HostConnected
AppStartSeen
SetupSeen
LastLoginCode
```

Populate these from the session when `failReady` is called.

### 2. Human-readable error

Change `ReadyFailure.Error()` so `repeated_reset` explains:

```text
WMPF 宿主在启动阶段连续重置 N 次（阶段=..., 登录=..., 宿主连接=..., 应用启动=..., setup=...）
```

Keep the machine-readable `Reason` unchanged for restart classification.

### 3. Smarter reset rule

Track reset timestamps. Fail when:

```text
3 resets within 10s with no launch progress
or 5 total resets within 30s
```

If the launch has reached login/host/app-start progress, do not fail at the
first burst of three resets.

### 4. Tests and verification

- Update existing repeated-reset test.
- Add tests for:
  - no failure at 3 resets after progress
  - failure at 5 resets
  - diagnostics fields
- Run `go test ./...` and `build-release`.
- Run MCP launch/runtime/cdp checks.
- Commit each step separately.
