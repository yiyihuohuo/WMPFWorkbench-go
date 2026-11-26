# Mini Program Jump Automation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a repeatable `jump_mini_program` MCP action that injects a target AppID into an already-launched mini program, automates the confirmation click, and leaves CDP attached to the jumped target.

**Architecture:** Pure Go validation/template helpers plus a Service method that drives the existing CDP session. No runtime/profile files are copied or rebuilt for this feature. The already-restored bridge active-target routing and runtime debug activation fixes are reused.

**Tech Stack:** Go 1.x, Wails MCP service, CDP Runtime/Input domains, JavaScript injected into the mini-program appContext, Node `node:test` for injected JS, `go test`.

---

### Task 1: Add jump validation and JS templates

**Files:**
- Create: `native/internal/core/jump.go`
- Create: `native/internal/core/jump_test.go`

**Step 1: Write failing tests**
- `TestValidateJumpArgs`: rejects invalid appid, empty handler, empty selector; accepts valid values and defaults.
- `TestBuildInjectJumpScript`: output contains target appid, handler name, `navigateToMiniProgram`, `appContext`, evidence key, and escapes quotes safely.
- `TestBuildRectScript`: output contains selector, `getBoundingClientRect`, and `btn_allow` confirmation matching.

**Step 2: Run tests**
Run: `go test ./internal/core -run 'Test(ValidateJumpArgs|BuildInjectJumpScript|BuildRectScript)$' -count=1`
Expected: FAIL because `jump.go` does not exist.

**Step 3: Implement helpers**
- `validateJumpArgs(args map[string]any) (jumpArgs, error)`.
- AppID rule: `^wx[0-9a-f]{16}$`.
- Defaults: source handler `goYY`, button selector `.yy-btn`, confirm selector `button.btn_allow`, timeout 20s, auto confirm true.
- Build JS strings with JSON-quoted values.

**Step 4: Run tests**
Run same command. Expected PASS.

**Step 5: Commit**
```bash
git add native/internal/core/jump.go native/internal/core/jump_test.go
git commit -m "feat(core): add mini-program jump validation and JS templates"
```

### Task 2: Add jump orchestration to Service

**Files:**
- Modify: `native/internal/core/service.go`
- Test: `native/internal/core/jump_service_test.go`

**Step 1: Write failing test**
- `TestJumpArgsValidationThroughCall`: calling `Service.Call("jump_mini_program", invalid appid)` before CDP connection returns validation error, not `not_connected`.

**Step 2: Run test**
Run: `go test ./internal/core -run TestJumpArgsValidationThroughCall -count=1`
Expected: FAIL because dispatcher does not know the tool.

**Step 3: Implement orchestration**
- Add `case "jump_mini_program": return s.jumpMiniProgram(ctx,args)`.
- In `jumpMiniProgram`:
  1. validate args;
  2. require `s.current()` CDP connection;
  3. `Runtime.enable`;
  4. evaluate inject script;
  5. evaluate source rect script;
  6. dispatch `Input.dispatchMouseEvent` pressed/released (pointerType `touch`) at source rect;
  7. poll confirmation rect script until found or timeout;
  8. dispatch confirmation press/release;
  9. poll evidence script and `Runtime.Sessions()` active_appid;
  10. return structured result.

**Step 4: Run tests**
Run: `go test ./internal/core -count=1`
Expected PASS.

**Step 5: Commit**
```bash
git add native/internal/core/service.go native/internal/core/jump_service_test.go
git commit -m "feat(core): orchestrate automated mini-program jump over CDP"
```

### Task 3: Register MCP tool schema

**Files:**
- Modify: `native/contract/data/tools.json`
- Test: `native/internal/contract/tools_test.go` if present; otherwise `go test ./contract/...`

**Step 1: Add schema**
- Tool name `jump_mini_program`.
- Required: `target_appid`.
- Optional: `source_handler`, `button_selector`, `confirm_selector`, `timeout_ms`, `auto_confirm`.

**Step 2: Verify JSON parses**
Run: `go test ./contract/... -count=1` or `python -m json.tool native/contract/data/tools.json > NUL`.

**Step 3: Commit**
```bash
git add native/contract/data/tools.json
git commit -m "feat(mcp): expose jump_mini_program tool"
```

### Task 4: Add injected-JS Node test

**Files:**
- Create: `tools/jump_inject.test.cjs`

**Step 1: Test behavior**
- Mock `document.querySelector('iframe[name=appContext]').contentWindow`.
- Mock `getCurrentPages()` returning a page with `goYY`.
- Evaluate generated script from a fixture or embedded JS.
- Assert original handler saved once, replacement calls `wx.navigateToMiniProgram` with target, and evidence records `navigateToMiniProgram:ok`.

**Step 2: Run**
Run: `node --test tools/jump_inject.test.cjs`
Expected PASS.

**Step 3: Commit**
```bash
git add tools/jump_inject.test.cjs
git commit -m "test(research): verify injected jump handler behavior"
```

### Task 5: Integration verification on research branch

**Files:**
- Modify only generated/research outputs under `out/research-miniapp-jump-20261123/`.
- No source changes in this task.

**Step 1: Build**
Run: `cd native && go test ./... -count=1`

**Step 2: Manual integration**
- Launch Tea Yan Yue Se via MCP.
- `cdp_connect`.
- Call `jump_mini_program` for `wxde8ac0a21135c07d`.
- Require `jumped=true` and `active_appid=wxde8ac0a21135c07d`.
- Repeat for `wxacdc0917b2fdfe6e`.
- After each jump, call `cdp_evaluate` for `Runtime.enable`-ready target and verify the appId belongs to the current target.

**Step 3: Commit evidence report**
```bash
git add out/research-miniapp-jump-20261123/
git commit -m "research: record automated jump verification"
```

---

## Execution Guardrails

- One commit per task; do not mix tasks.
- Do not copy or rebuild runtime/profile files for this feature.
- Do not modify `F:\校友邦\sign-sign-in`.
- If a task needs a design change, stop and update the design doc first.
