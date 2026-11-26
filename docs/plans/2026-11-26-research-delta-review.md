# Research delta review after stable port

Compared:

```text
stable/two-hop-release..research/signsignin-account
```

## Product code migrated

The following research changes are present in `stable/two-hop-release`:

- event-driven readiness state machine and failure reasons;
- official IILinkAuthManager flow with `GetUserInfo` / `AutoLogin` / lazy buffer;
- lazy login-buffer minting;
- CDP reconnect across app switches;
- app listing classification via `pcyyb_get_app_detail`;
- two-hop session state and target-only projections;
- unlisted app routing through hidden bridge app;
- bridge hide/unhide;
- MCP field adaptation;
- GUI DevTools frontend direct link;
- dump/dumpsource/dumpclient product paths.

The env-gated real app-detail test was migrated as commit `c51f501`.

## Remaining deltas intentionally not migrated

### `tools/native_appid_hook.js`

Research-only diagnostics:

- policy mitigation probe;
- `jsapi_called` stack logging;
- appid rewrite diagnostics;
- route A/B diagnostic event shapes.

Not needed for the product two-hop path. Migrating them would add Frida
diagnostic noise and increase embedded-script size.

### `tools/native_xref_scan.js`

Research-only Route A/B probes:

- `dialog.create`;
- `dialog.runner`;
- `jsapi_owner` parameter replay experiments.

Not product functionality.

### `hostcpp/main.cpp`

Research-only `--no-sandbox --disable-gpu-sandbox` injection into
`WeChatAppEx.exe`. This was used for mitigation experiments and is not part of
the product two-hop launch. Stable keeps normal sandbox behavior.

### `native/internal/hostruntime/manager.go`

Research-only unused `rebindCount` field. No behavioral impact.

### `native/worker/main.cpp`

Research-only forwarding of:

```text
WMPF_JUMP_ARM
WMPF_JUMP_TARGET_APPID
```

These belonged to the native appid rewrite experiment, not the product two-hop
flow.

### Docs and headers

Research-only docs, official WMPF headers, ABI analysis, incident reports, and
offline experiments are not migrated. The stable port has its own acceptance
and migration docs.

## dump project status

The external repository:

```text
<workspace>\wxapkg-dump
```

is clean on `main` with keepalive/expiry commits already present. There are no
uncommitted modifications to migrate.

The WMPFWorkbench dump-related product files have no remaining functional diff
against research.

The baselib patch used during earlier analysis was restored; no persistent
`WAPCAdapterAppIndex.js` modification remains.

## Conclusion

All product-relevant changes are migrated. The remaining research delta is
diagnostic/experimental and intentionally excluded from the stable-derived
branch.
