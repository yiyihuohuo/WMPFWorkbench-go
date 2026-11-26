# Portability review

## Fixed in this pass

- Replaced hardcoded `<temp>` in the worker flag paths with the
  process temp directory from `GetTempPathW`, matching Go's `os.TempDir`.
  This affects unhide, targeted hook, native hook mode, and xref scan flags.
- Added configurable ports:
  - `target_port` default `9421`
  - `cdp_port` default `62000`
  hostruntime now builds `TargetAddress` and `CDPAddress` from config.
- `cdp_connect` with the default CDP base now prefers the `cdp_websocket`
  reported by a running session, so a configured non-default port works.
- Added configurable `bridge_appid` with the current listed bridge app as
  default. Two-hop routing no longer requires code edits to change bridge app.
- Removed the hardcoded worktree path from `research_mint_test.go`; it now uses
  a temp config path.
- GUI DevTools launcher falls back to `http://<cdp-host>/json/list` when neither
  Chrome nor Edge is found, instead of failing outright.

## Remaining intentional requirements

1. Windows-only runtime
   - WMPF/WeChatAppEx is Windows-only.
   - The bundled `wmpfhost_integrated.exe`, `wmpf-observer.exe`, and
     `wxapkg-dump.exe` must be present under the extracted app directory.
   - `dump_exe`, `WXAPKG_DUMP_EXE`, `WMPF_HOST_PATH`, and
     `WMPF_OBSERVER_PATH` remain available as overrides.

2. Account provisioning
   - The product uses the normal `wxapkg-dump` QR login flow.
   - Account state is read from the sidecar-selected `account.json` under the
     `wxapkg-dump` remote home, or from `WXAPKG_REMOTE_HOME` when set.
   - No openid or nickname is hardcoded in product code.

3. Chrome/Edge for direct DevTools frontend
   - Preferred: Chrome/Edge found in Program Files or PATH.
   - Fallback: system browser opens the CDP discovery page.

4. Local loopback ports
   - Defaults remain `127.0.0.1:9421`, `127.0.0.1:62000`, `8788`, `18767`.
   - These can conflict on a machine using the same ports; `target_port`,
     `cdp_port`, `dump_port`, `mcp_port`, and `login_port` are configurable.

## Verification

```text
go test ./...  PASS
worker build   PASS
```

No product code uses a literal user home path or the developer worktree path
anymore. Test fixtures still use example appids/openids, which is expected.
