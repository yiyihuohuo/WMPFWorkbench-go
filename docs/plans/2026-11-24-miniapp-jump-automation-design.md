# Mini Program Jump Automation Design

> Status: approved by user on 2026-11-24.

**Goal:** Turn the already-proven “ Tea Yan Yue Se → arbitrary target AppID ” in-app jump into a repeatable MCP action with generic confirmation automation and a debuggable target runtime.

**Architecture:** Keep the native WMPF launch path unchanged. After a source mini program is launched and CDP is connected, inject a temporary handler into the source page's `appContext` so any source jump handler targets the requested AppID. The actual jump must still be triggered by trusted CDP input events because the JS runtime enforces a TAP-gate state machine and XWeb dispatches confirmation handlers through an internal trusted-event table. Generic confirmation automation locates the visible allow control by selector/text and injects a trusted CDP press/release at its center. The already-restored active-target bridge and runtime debug activation expose the jumped target as the current CDP target.

**Investigated JS-only alternatives (2026-11-24):**
- `button.click()` does not advance the navigation because XWeb's internal handler table is not driven by untrusted DOM clicks.
- Directly calling `button.l.clickfalse` executes the subscription-dialog handler and resolves its promise, but leaves the navigation flow occupied and does not complete the jump.
- `navigateToMiniProgramDirectly` returns `navigateToMiniProgramDirectly:fail no permission`.
- A trusted tap followed by a direct JS `wx.navigateToMiniProgram` call returns `navigateToMiniProgram:fail another navigation is in progress` when a prior dialog promise was resolved without completing the trusted event chain.
- Therefore JS hooks are used for target injection and generic control discovery; trusted CDP input remains necessary for both source activation and confirmation.

**Tech Stack:** Go MCP service (`native/internal/core`), CDP (`Runtime.evaluate`, `Input.dispatchMouseEvent`), JavaScript injected into the mini-program `appContext`, Node tests for injected JS, Go tests for validation/templating, existing Frida observer scripts (`tools/activate_reused_runtime.js`) and bridge active-target routing.

---

## Components

1. `jump_mini_program` MCP tool
   - Required: `target_appid`.
   - Optional: `source_handler` (default `goYY`), `button_selector` (default `.yy-btn`), `source_x` (default 70), `source_y` (default 260), `confirm_selector` (default `button.btn_allow`), `timeout_ms` (default 20000), `auto_confirm` (default true).
   - Returns: `target`, `evidence`, `active_appid`, `jumped`, `confirm_tapped`.

2. `native/internal/core/jump.go`
   - Validates AppID and selector inputs.
   - Builds injected JS for:
     - finding `iframe[name=appContext]`;
     - saving the original page handler once;
     - replacing it with a handler that calls `wx.navigateToMiniProgram` with the requested target;
     - recording success/fail/complete evidence on `window.__wmpfJumpEvidence`.
   - Builds JS that finds the confirmation button by selector/text and returns viewport coordinates.
   - Source mini-program buttons are rendered by the mini-program runtime, not as ordinary DOM nodes; therefore source activation also accepts configurable `source_x`/`source_y` coordinates, defaulting to the historically verified Tea Yan Yue Se `.yy-btn` point `70,260`. Selector discovery is still attempted first for future pages that expose DOM nodes.
   - Builds CDP input parameters for mouse/touch-like press/release.

3. `native/internal/core/service.go`
   - Adds a `case "jump_mini_program"` dispatcher.
   - Reuses `s.current()` CDP connection.
   - Polls evidence and `Runtime.Sessions()` for `active_appid == target_appid`.

4. `native/contract/data/tools.json`
   - Registers the new tool schema.

5. Existing restored research fixes (already committed on this branch)
   - `native/internal/bridge/server.go`: active target routing.
   - `tools/activate_reused_runtime.js`: any valid non-preload runtime with version can enable debug.
   - `hostcpp/main.cpp`: unlimited observation sessions.
   - `native/internal/hostruntime/manager.go`: `active_appid` / `jumped` tracking.

## Data Flow

1. Caller starts a source mini program with `launch` and connects CDP via `cdp_connect`.
2. Caller invokes `jump_mini_program` with a target AppID.
3. Service validates inputs and injects the temporary page handler.
4. Service queries the source button rectangle and dispatches a real CDP press/release.
5. The injected handler calls `wx.navigateToMiniProgram`.
6. Service polls for the confirmation button using selector/text rules.
7. Service dispatches a real CDP press/release on the confirmation control.
8. Service polls injected evidence and session state until `active_appid` changes to the target.
9. The restored bridge/runtime hooks keep CDP routed to the new target.

## Error Handling

- Invalid target AppID: fail before injecting JS.
- No `appContext` iframe or no current page: return structured `ok=false` with stage `inject`.
- Source button not found: return `ok=false` with stage `source_button`; allow explicit coordinates fallback if selector fails.
- Confirmation not found before timeout: return `ok=false` with stage `confirm`, preserving jump evidence.
- Jump API reports failure: return `ok=false` with stage `jump_api`, including `errMsg`.
- Target runtime starts but debug routing does not switch: return `ok=false` with stage `debug_target`, including `active_appid`.

## Testing

- Go: validation and JS-template tests in `native/internal/core/jump_test.go`.
- Go: service dispatch test for `jump_mini_program` argument validation.
- Node: `tools/jump_inject.test.cjs` verifies injected JS can locate a mocked `appContext`, install the handler, and record evidence.
- Integration: launch Tea Yan Yue Se, connect CDP, jump to `wxde8ac0a21135c07d` and `wxacdc0917b2fdfe6e`; require `jumped=true`, `active_appid` equals target, and a read-only CDP evaluation after `Runtime.enable` returns the target appId.
