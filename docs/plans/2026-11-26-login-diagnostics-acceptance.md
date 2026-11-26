# Login diagnostics acceptance

## Implemented

- `remote_home` config key, default `<workbench>/data/desktop/remote`.
- `WXAPKG_REMOTE_HOME` is set in the GUI process and explicitly passed to the
  wxapkg-dump sidecar child process.
- Legacy `%APPDATA%\wxapkg-dump\remote` accounts are migrated when the new home
  has no `account.json`.
- `dump_workspace` defaults to `<workbench>/data/desktop/workspace`.
- QR images support Data URLs, remote URLs, and raw base64.
- `login_status` now returns structured diagnostics and sidecar metadata.
- `-109` and upstream refresh rejections are mapped to
  `ual_credentials_expired` with `relogin_required=true`.
- GUI account page shows a structured diagnostic and prompts for re-login.
- GUI `RefreshAccount` no longer throws raw upstream text to the user.

## Verification

```text
go test ./...  PASS
build-release  PASS
```

`login_status` after rebuilding and starting the GUI:

```text
remote_home = <workbench>/data/desktop/remote
account_file_exists = true
account_schema = 2
account_state = online
expires_in = 7200
has_access_token = true
has_refresh_token = true
has_login_buffer = true
has_launch_buffer = true
login_service.url = http://127.0.0.1:8788
login_service.version = 1.0.0
login_service.protocol = 1
```

No token or cookie values are exposed in diagnostics.

## Result

This closes the set of login issues that were caused by environment mismatch,
QR image wrapping, and opaque upstream errors. If UAL later rejects refresh
with `-109`, the UI now presents a concrete re-login instruction instead of a
generic sidecar error.
