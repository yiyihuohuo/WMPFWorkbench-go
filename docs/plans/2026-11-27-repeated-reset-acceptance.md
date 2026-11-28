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
- All exported text is redacted before hashing/writing: `uin`, `openid`, token
  and cookie-shaped values are removed, and local paths are replaced with
  `<APP_ROOT>`, `<TEMP>` and `<USER>`. Event names, error codes and timestamps
  are preserved because they carry the root-cause evidence without identifying
  the operator.

## Diagnostics export verification

```text
go test ./cmd/workbench-gui -run 'TestFindLatestFailedSession|TestCollectAndWriteDiagnosticZip|TestRedactDiagnosticText|TestReadDiagnosticFileTakesTail' -v
PASS
go test ./...                         PASS
build-release                         PASS
```

The generated test archive was opened with `archive/zip`; it contained the
expected event stream, worker config, failure record, agent log, UI log, README
and manifest. The built GUI binary contains the new button label, the
`ExportDiagnostics` Wails binding and the archive README text.

```text
dist/trim/wmpf-gui.exe sha256 = 5305661747A788D6A64D5ED23A2BC6FC0ED49EA27326F2C577CFE50BC06D6310
```

The native save dialog path was not clicked automatically in this headless
verification; the button handler, Wails binding and ZIP writer were verified
separately by compile-time binding, embedded-asset checks and the archive test.

## 1.0.5 asset overwrite with redacted build

The existing `1.0.5` release was overwritten; no new version or tag was created.

```text
release          = 1.0.5
asset            = WMPFWorkbench-go-1.0.5-win-x64.zip
old asset id     = 798958000
new asset id     = 799181310
asset bytes      = 67161455
local sha256     = 5D5B7305F1DC71CC1D1BCA009B30579E5F15995797C4C208A8A38C5103F49033
remote sha256    = 5D5B7305F1DC71CC1D1BCA009B30579E5F15995797C4C208A8A38C5103F49033
match            = true
```

The release body now mentions the diagnostics export and redaction. Because the
asset was deleted and re-uploaded, its download count was reset; the version
name, tag and asset filename remain unchanged.

## Failed session export path fix

The first exported build still reported `未找到失败会话` after a real launch
failure. The exporter looked for `<session>/failure.json`, while hostruntime
writes `<session>/observation/failure.json` (`manager.go:1275`, `manager.go:1290`).
The unit fixtures had used the same wrong path, so they did not catch the drift.

`diagnosticFailurePath` now prefers `observation/failure.json` and falls back to
the session-root file for older artifacts. Both discovery and packaging use it.
Tests were updated to the real path and a legacy-path case was added.

## Runtime 5.10.7300.414 hash mismatch diagnosis

The reported mismatch is not a code regression. The expected hash in
`validate.go`:

```text
a66d9388ff0b6fbd79045551e9d5b22dadc460ebd1b5659733361921617e22f6
```

is the Git LFS object ID of the real `runtime/flue.dll`. The user-provided actual
hash:

```text
4925257155dab73da434d5108f4988534049b15a3c9192631fff40c5896b2810
```

is the SHA-256 of the 134-byte Git LFS pointer text served by
`raw.githubusercontent.com`. The pointer says:

```text
version https://git-lfs.github.com/spec/v1
oid sha256:a66d9388ff0b6fbd79045551e9d5b22dadc460ebd1b5659733361921617e22f6
size 210967088
```

`.gitattributes` marks `runtime/flue.dll` as LFS. Downloading the repository as
a ZIP or fetching raw files therefore yields pointer text instead of the DLL.
Fetching the LFS object through `media.githubusercontent.com` produced a
210,967,088-byte file with SHA-256 `a66d...`, exactly the expected value.
Correct acquisition is `git lfs install` followed by clone/pull, or using the
LFS-resolved media URL; copying pointer text into `runtime/` will always fail
preflight.

## 1.0.5 asset overwrite with export path fix

The `1.0.5` asset was overwritten again with the export path fix; the version
name, tag and asset filename remain unchanged.

```text
old asset id  = 799181310
new asset id  = 799899619
asset bytes   = 67161383
sha256        = 052830E22DA17277A659576C7B033A6D9D22C269BF1EA563CE6727F04E153D65
remote match  = true
```

Source was pushed to both branches:

```text
main                          = 8920f42616dd0c8c368f70f181977349db6835a0
stable/two-hop-release-public = 8920f42616dd0c8c368f70f181977349db6835a0
```

## Full runtime download release

A direct-download release was published in the runtime repository so users no
longer need `git lfs` to obtain the real runtime files:

```text
https://github.com/yiyihuohuo/WMPF-Runtime-5.10.7300.414/releases/tag/runtime-5.10.7300.414-full
```

Assets:

```text
WMPF-Runtime-5.10.7300.414-full.zip
  size   = 188874181
  sha256 = E37F981843C2C710F8D5970671C88B3805D31C44328EA176DBE9D8D102C6C3C7

SHA256SUMS.txt
  size   = 3919
  entries = 43
```

The ZIP was downloaded back from GitHub and verified:

```text
zip_remote hash match = true
runtime/flue.dll size = 210967088
runtime/flue.dll sha256 = a66d9388ff0b6fbd79045551e9d5b22dadc460ebd1b5659733361921617e22f6
all 43 files in SHA256SUMS = valid
```

Users can download the ZIP directly in a browser, extract it, and copy the
`host/` and `runtime/` directories into the workbench's
`runtime/5.10.7300.414/` directory.

## Export tolerates missing session files

A user-reported case showed that `failure.json` was found correctly but
`observation/events.jsonl` did not exist for that failed session. The exporter
treated `events.jsonl` and `worker.json` as required, so a single missing file
aborted the whole export with `The system cannot find the file specified`.

All session files are now optional. Missing files are recorded in
`manifest.json` under `missing_files` and in `missing.txt` inside the archive;
export succeeds as long as at least one diagnostic file exists. The archive
README explains that missing files do not invalidate the remaining evidence.
Add `TestCollectWithoutEventsFileStillExports` covers the reported case.

## Old sidecar 405 and safe replacement

The reported account check failures were:

```text
wxapkg-dump 响应无效（HTTP 405）
```

The bundled sidecar source contains `POST /api/ilink/account/keepalive`, and
`OPTIONS` on that route returns `allow: POST`. An older sidecar has the dynamic
`/api/ilink/account/{openid}` DELETE route instead, so the same POST is
answered with 405. The GUI previously reused whatever process already listened
on port 8788 after checking only `/api/ilink/status`; it therefore kept the old
process forever.

The old behavior also extracted embedded helpers in place to
`<data>/bin/<name>`, which cannot replace a running executable on Windows.

Changes:

- `bundle.Ensure` releases every bundle into
  `<data>/bin/versions/<content-hash>/`. Running files are never overwritten;
  the current version and one previous version are kept for rollback.
- `dumpclient.Ensure` probes `OPTIONS /api/ilink/account/keepalive`.
  - 2xx or `Allow: POST` means compatible.
  - 405 without POST or 404 means an old sidecar.
  - Any other status/network error is inconclusive and never triggers a kill.
- An old sidecar is stopped only when all of these hold:
  - the listener image is exactly `wxapkg-dump.exe`;
  - its full image path is under `<data>/bin`;
  - its path differs from the bundled helper about to be launched;
  - the process is still the same PID/path on a second check.
- If the path is outside `<data>/bin`, or the path equals the bundled helper
  (meaning extraction failed), the process is not touched and the error tells
  the user to update/reinstall.
- `ClassifyLoginError` now reports `sidecar_outdated` instead of the misleading
  "重新扫码" message.
- GUI startup now surfaces `bundle.Ensure` failures in the log instead of
  silently falling back to old helpers.

Tests cover versioned extraction without overwriting a legacy file, cleanup,
OPTIONS tri-state probing, netstat/process path lookup, path boundary checks,
same-path protection, and refusal to kill an outside process.

## Final 1.0.5 asset for export and sidecar fixes

```text
old asset id = 800892305
new asset id = 800990158
asset bytes  = 67177948
sha256       = 1b6a3bd2ea0a3a158553a17202ab2747d2aa319ed346d6c47cbd5c80bfbcf7d9
GitHub digest = sha256:1b6a3bd2ea0a3a158553a17202ab2747d2aa319ed346d6c47cbd5c80bfbcf7d9
source HEAD  = 3698d9b
```

GitHub's asset digest matches the locally built ZIP; both source branches were
fast-forwarded to `3698d9b`.

## Exported session selection bug

The user-provided `wmpf-diagnostics-d10b62ba682b595d.zip` contained a failure
from `2026-11-23T13:25:38+08:00` for `wx01c9bc28d8206fa6`, while the UI log in
the same archive showed a fresh `2026-11-28 12:52` failure for
`wx4a4c2f53af93d4d3` with `host_pid=12304` and the same
`ReLaunch -> InitCore -> ReBind -> LaunchAndBind -> Reset` chain.

`findLatestFailedSession` was ranking sessions by `failure.json` mtime. A stale
session with a newer file timestamp won over the real latest failure. The
selection now ranks by `failed_at` parsed from `failure.json`, using file mtime
only for old artifacts without a parseable timestamp. The test intentionally
inverts mtimes to prove `failed_at` wins.

The stale ZIP contains only `failure.json`, `ui-log.txt`, `manifest.json`,
`README.txt` and `missing.txt`; the missing event/worker files are now listed
rather than aborting export. It is not sufficient to analyze the fresh
`host.Reset`, so the user needs to export again with the rebuilt package.

## Comprehensive audit remediation

A four-track audit covered startup failures, diagnostics export, sidecar update
safety, and GUI/account UX. The following user-visible defects were fixed
together rather than one at a time:

- `failReady` now persists a `ReadyFailure` immediately. Repeated reset,
  bad preload, host timeout, watchdog and other soft failures all write
  `failure.json`; the first cause wins and later events cannot overwrite it.
- `failure.json` now carries `failure_reason`, `phase`, `reset_count`,
  `recent_reset_count`, `host_pid`, `initial_host_pid`, `worker_pid`,
  `host_rebind_seen` and `last_events`. Writes are atomic and write errors are
  logged.
- Host rebuild events (`ReLaunch/InitCore/ReBind/LaunchAndBind`) are treated as
  progress, so phase is no longer misreported as `worker_started` with advice
  to reset runtime data. Host PID is updated on rebuild while the initial PID
  is preserved. Reset wording now states the real 30s/10s windows.
- Diagnostics export keeps a tail of oversized UI logs and records omissions;
  manifest lists all generated files; `failed_at` accepts multiple formats and
  resolves same-second ties; attempt-2 worker/metadata files are collected;
  export is serialized and writes the ZIP atomically; redaction covers
  camelCase keys, unionid/sessionid/sign and multi-value cookies.
- Sidecar lifetime is decoupled from a single export task context; readiness
  checks HTTP identity rather than TCP only; old process termination rechecks
  the PID/path immediately before kill; netstat uses the absolute System32
  path; unknown port owners are no longer called "old wxapkg-dump"; OPTIONS
  probes carry the admin key and reject generic CORS 2xx; version fallback can
  find versioned binaries even when the env path is stale; MCP no longer
  silently ignores bundle extraction failures; session IDs used in temp paths
  are whitelisted.
- Login classification recognizes HTTP 405 and inconclusive probes, and no
  longer echoes raw backend errors into UI state.
- GUI fixes cover duplicate log suppression, clipboard failure reporting,
  per-ref avatar lookup, launch error reasons in the UI, real log clearing,
  QR generation races, export field polling overwrite, filtered search counts,
  export polling timeout, delete-account false results, status colors,
  `bundle_error` visibility, debugger fallback reporting and dynamic scan
  error display.

Residual risks documented by the audit: PID reuse cannot be eliminated
entirely without a kernel-level handle-based kill; the frontend `dist` changes
must be preserved if the frontend source is later rebuilt; export polling
timeout stops UI polling but does not cancel a stuck backend task.

Final asset for the comprehensive remediation:

```text
asset id = 803398018
size     = 67181777
sha256   = 21d5dc93b414400bd1b9f50b80713012f44caee0458ec9a25ee2c01d2dbd7432
digest   = sha256:21d5dc93b414400bd1b9f50b80713012f44caee0458ec9a25ee2c01d2dbd7432
source   = 5e62be1
```

## Real MCP HTTP end-to-end test

The built `dist/trim/wmpf-gui.exe` was actually started as a standalone MCP
HTTP server on `127.0.0.1:18767` with an isolated config file:

```text
wmpf-gui.exe -mcp http -mcp-port 18767 -config <temp-config> -root dist/trim
```

Endpoints and calls:

```text
GET  /console/health        -> HTTP 200 {"ok":true,"service":"wmpf-console","tools":40}
POST /mcp initialize        -> HTTP 200, protocol 2025-06-18, session established
POST /mcp notifications/initialized -> HTTP 202
POST /mcp tools/list        -> HTTP 200, 40 tools
POST /mcp tools/call login_status      -> HTTP 200, no isError
POST /mcp tools/call index action=stats -> HTTP 200, no isError
POST /mcp tools/call runtime_status    -> HTTP 200, no isError
POST /mcp tools/call get_app_detail wx4a4c2f53af93d4d3 -> HTTP 200, no isError
POST /console/shutdown      -> HTTP 200, port closed after exit
```

`get_app_detail` for `wx4a4c2f53af93d4d3` reported `listed=true`,
`launch_mode_hint=direct`, `package_source=local_index`. No raw account
identifiers or credentials are reproduced here.

