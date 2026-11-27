# repeated_reset fix acceptance

## Completed

- `ReadyFailure` now carries launch phase, reset count, login/host/app/setup
  state and last login code.
- `repeated_reset` error messages are now human-readable and explain what was
  not ready.
- Host reset failure requires either:
  - three resets within ten seconds before any launch progress, or
  - five resets within thirty seconds.
- Existing precise per-session cleanup and `WMPF_GAME_TYPE` propagation were
  verified as already implemented and left unchanged.

## Tests

```text
go test ./...   PASS
build-release   PASS
```

## MCP verification

The AppID from the reported failure was retested:

```text
appid        = wxece3a9a4c82f58c9
state        = debug_ready
login_ok     = true
host_connected = true
app_start_seen = true
setup_seen   = true
cdp_ok       = true
preload_slot = preload-5
session_id   = ea1390b3cb4cadb3
```

Additional MCP checks:

```text
login_status              PASS
runtime_status            debug_ready
cdp_connect               PASS
cdp_evaluate              1+1 => 2
```

## Notes

The same AppID previously failed with `repeated_reset: count=3`. After the
release rebuild and a clean launch, it reached `debug_ready`; the likely
contributors were a stale native session/environment and the previous
fail-fast reset rule. The new diagnostics ensure that if it recurs, the failure
message identifies the launch phase and state instead of only a count.

## Diagnostics export

- `ReadyFailure` additionally reports `host_pid`, optional `worker_pid`, and the
  last five payload-free event labels.
- `repeated_reset` advice is phase-aware: `host_bootstrap` / `worker_started`
  direct the user to exit processes, verify package integrity and reset runtime
  data; `host_connected` directs the user to re-login; later phases may suggest
  changing the target.
- The About page has a themed “导出诊断日志（ZIP）” button. The backend picks the
  most recent failed session and packages `events.jsonl`, `worker.json`,
  `failure.json`, optional metadata/bridge logs, matching `wmpf-agent-*.log`
  and `wmpf-devtools-frontend-*.log`, the GUI log tail, `README.txt` and a
  `manifest.json` with sizes and SHA256 hashes.
- Agent logs are capped at 4 MiB tail each and the archive at 64 MiB.

## Diagnostics export verification

```text
go test ./cmd/workbench-gui -run 'TestFindLatestFailedSession|TestCollectAndWriteDiagnosticZip|TestReadDiagnosticFileTakesTail' -v
PASS
go test ./...                         PASS
build-release                         PASS
```

The generated test archive was opened with `archive/zip`; it contained the
expected event stream, worker config, failure record, agent log, UI log, README
and manifest. The built GUI binary contains the new button label, the
`ExportDiagnostics` Wails binding and the archive README text.

```text
dist/trim/wmpf-gui.exe sha256 = 2CD92D0B6EE216AB7C2664C162F88D2F2F249DE0389D9E854C46F08364C79469
```

The native save dialog path was not clicked automatically in this headless
verification; the button handler, Wails binding and ZIP writer were verified
separately by compile-time binding, embedded-asset checks and the archive test.
