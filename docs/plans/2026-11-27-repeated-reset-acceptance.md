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
