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
