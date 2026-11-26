# Login diagnostics and reliability plan

## Problem

Users report that QR login does not work or accounts cannot be used, while the
developer's own login works. The likely causes are environmental/state related:

- sidecar account home differs from the workbench-owned directory;
- QR image is a URL but the client wraps it as base64;
- sidecar port conflicts or stale sidecar instance;
- `-109` UAL rejection is shown as a generic error;
- `login_status` only exposes a thin account view and no actionable diagnostics.

## Product model

The real flow is:

```text
GUI QR login
→ wxapkg-dump sidecar
→ account state stored under a workbench-owned remote home
→ account.Client reads the same account.json
→ WMPF launch uses launch_buffer from that account
```

No fixed openid or nickname is allowed in product code.

## Step 1: unify remote home

- Add config key `remote_home`, default `<workbench>/data/desktop/remote`.
- If `WXAPKG_REMOTE_HOME` is already set, respect it.
- Set `WXAPKG_REMOTE_HOME` in the GUI process so both `account.Client` and the
  spawned sidecar use the same directory.
- Add `RemoteHome` to `dumpclient.Options` and set it explicitly in the child
  process environment.
- Migrate an existing `%APPDATA%\wxapkg-dump\remote` account into the new home
  when the new home has no `account.json`.

## Step 2: QR image normalization

`LoginQRCreate` must support three sidecar outputs:

```text
data:image/...  -> pass through
http(s)://...   -> pass through as URL
raw base64      -> prefix data:image/png;base64,
```

Add `image_kind` to the mapped result while keeping `image_base64` for GUI
compatibility.

## Step 3: structured login diagnostics

Add `account.InspectDumpAccount()` returning non-sensitive metadata:

```text
remote_home
account_file
exists
schema
state
has_access_token
has_refresh_token
has_login_buffer
has_launch_buffer
expires_in
```

Extend `dumpAccountForStatus` with:

```text
remote_home
account_file
account_file_exists
sidecar_base_url
sidecar_version
warnings
credential metadata
diagnostic_code
```

Never expose token values.

## Step 4: classify `-109`

Add a shared login error classifier:

```text
upstream_rejected + code -109
→ code = ual_credentials_expired
→ relogin_required = true
→ message = "UAL 凭据被上游拒绝，请重新扫码登录"
```

Apply it to:

- `login_status`;
- `CheckAccount`;
- login QR generation/poll errors.

## Step 5: GUI display

- Show structured login error in the account panel.
- Show remote home and sidecar version in diagnostics.
- When `relogin_required=true`, prompt the user to regenerate the QR code.
- Keep QR countdown behavior unchanged.

## Step 6: tests and acceptance

Tests:

- remote home default and override;
- QR data URL / URL / raw base64 normalization;
- `-109` classifier;
- login status diagnostic fields;
- no fixed openid/nickname in product code.

Acceptance:

```text
go test ./... PASS
build-release PASS
GUI login_status exposes actionable diagnostics
QR URL rendering does not become a broken data URL
-109 produces relogin_required instead of a raw stack-like error
```

Each step must be committed separately.
