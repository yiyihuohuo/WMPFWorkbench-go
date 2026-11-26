# Native AppID Hook Design

> Status: approved by user on 2026-11-24.

**Goal:** Replace the page-level JS injection experiment with a real Frida hook that rewrites the target AppID inside the native `JsApiNavigateToMiniProgram` call.

**Architecture:** The observer already attaches a minimal script to every `--type=` WeChatAppEx child. That script hooks live `flue.dll` RVA `0x268e190` (`JsApiNavigateToMiniProgram`). On entry it reads the JS arguments object; historical observation proves the AppID fields are inline short strings at `obj+40` (`appId`) and `obj+64` (`toAppId`). When a target is configured, both valid `wx[0-9a-f]{16}` fields are overwritten in place. The page still triggers a real source button event, so TAP-gesture state remains valid; the native hook only changes the requested destination.

**Configuration:** `WMPF_JUMP_TARGET_APPID` environment variable, then `<temp>\wmpf-jump-target-appid.txt`. Empty/invalid values disable rewriting.

**Scope:** This is B route only: in-app jump from an app-store-launched source mini program. A route (independent launch f27 binding) remains separate and is not touched by this hook.

**Verification:** Inject a valid target, launch Tea Yan Yue Se, click the real source jump button, confirm the dialog with CDP trusted input, then require `runtime_status.active_appid` and CDP page URL to match the hook target.
