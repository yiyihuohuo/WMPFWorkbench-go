# Stable port acceptance

Branch:

```text
stable/two-hop-release
```

Base:

```text
stable/js-cdp-jump
```

The migration intentionally cherry-picked only product features and required
runtime fixes, not one-off diagnostics, mitigation experiments, or incident
documents.

## Ported feature chain

```text
432152b  event-driven launch readiness
b5c2fa4  readiness failure reasons
a1d69a8  precise reset failure detection
2eef60c  preserve GUI-owned preload processes
cd24efb  do not kill GUI-owned WeChatAppEx processes
106db93  readiness state machine
aa22853  session error signals readiness failure
e6dc80e  forward host stdout diagnostics
6ea1bfb  typed GetUserInfo/Login callbacks
29a2a0a  official AutoLogin branch
f6ddc88  pass empty string to AutoLogin
9f31b7c  log vtable slots and forward WMPF_AUTH_FLOW
267a79e  forward auth vtable log prefix
d9cf74d  recheck GetUserInfo after AutoLogin
1397830  split launch inputs from lazy login buffer
e19444c  host requests manual login buffer lazily
2aee679  manager mints login buffer only on manual request
bf97a17  CDP reconnectable across app switches
5092ad5  classify app listing
5270af4  two-hop session state projection
9d5f769  route unlisted apps through two-hop launch
e937616  hide bridge then unhide after target active
629cbfc  GUI CDP discovery URL
4ad3ec9  MCP tool adaptation for two-hop
4f768b0  wait bridge CDP for two-hop launch
d6675a0  GUI opens DevTools frontend directly
```

## Conflicts resolved

- `hostcpp/main.cpp`: kept `AutoLogin("")` plus pre-call logging.
- `native/worker/main.cpp`: kept native-mode targeted hook and unhide flag path.
- acceptance doc: kept direct `devtools://` frontend URL rather than JSON URL.

## Verification

```text
go test ./...  PASS
hostcpp build  PASS
worker build   PASS
build-release  PASS
```

Real launch on stable port:

```json
{
  "appid": "wxacdc0917b2fdfe6e",
  "seconds": 0,
  "hide_window": false,
  "wait_ready": true,
  "restart_on_failure": true
}
```

Result:

```text
session_id      = f9d3a429b9de04d7
requested_appid = wxacdc0917b2fdfe6e
active_appid    = wxacdc0917b2fdfe6e
appid           = wxacdc0917b2fdfe6e
launch_mode     = two_hop
listing_state   = unlisted
jumped          = true
bridge_hidden   = true
bridge_closed   = true
state           = debug_ready
cdp_websocket   = ws://127.0.0.1:62000
```

Post-jump `cdp_connect` succeeded:

```text
ok = true
target = owned-runtime
version = XWEB/20121
```

## Conclusion

The stable-derived branch now has:

```text
hidden bridge two-hop launch
unlisted app routing
target-only public projection
CDP reconnect after jump
GUI DevTools frontend link
all 19 MCP tools reviewed/adapted
```

It is ready to be merged back into `stable/js-cdp-jump` after review.
