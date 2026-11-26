# Release hardening plan

## Scope

Fix every non-image issue found in the release audit. Screenshots are explicitly
out of scope per user instruction.

## Tasks

### 1. Remove real account identifiers from product code

- Remove the hardcoded real UIN and related research comment from
  `hostcpp/main.cpp`.
- Do not replace it with another real identifier.
- Read optional runtime overrides from environment/config when needed.
- Verify launch still works after removal.

### 2. Remove research-only account mutation code

- Delete `native/internal/launch/research_mint_test.go`.
- Delete tracked research JSON under
  `out/research-miniapp-jump-20261123/`.
- These files read or modify real account state and expose local paths.

### 3. Sanitize docs

- Replace local Windows username in the incident document with `<user>`.
- Replace local worktree paths with `<workbench>`, `<repo>`, or `<workspace>`.
- Correct portability claims that no longer match the code.

### 4. Remove hardcoded temp paths

- Replace `<temp>\...` in Go jump service with `os.TempDir()`.
- Keep worker's `GetTempPathW` behavior and remove only unsafe fallbacks where
  practical.
- Add tests for temp path selection and flag cleanup.

### 5. Align README with actual product

- Tool count must match contract (currently 40).
- Document two-hop support for unlisted targets.
- Document runtime version and sidecar requirements without implying they are
  bundled.
- Explain `bridge_appid` configuration and default.
- Remove stale “16/18 tools” claims.

### 6. Improve user-facing failure messages

- When Chrome/Edge is unavailable for the DevTools frontend, return a clear
  message and fallback URL.
- When a sidecar port is occupied by an incompatible service, report it as a
  port/version conflict rather than a generic JSON error.
- Validate sidecar version where possible and avoid silently reusing an
  incompatible service.

### 7. Portability tests

- Tests must use `t.TempDir()` and never write outside the repository.
- Add tests for temporary flag paths and local path resolution.
- Ensure no test requires a developer-specific worktree path or APPDATA account.

### 8. Final release safety

- Verify tracked tree contains no real UIN/openid/token.
- Verify untracked runtime data remains ignored.
- Because current branch history may still contain the old UIN, create a new
  sanitized orphan release branch after all commits.
- Keep the working branch for local history; publish only the sanitized branch.

## Acceptance

```text
go test ./...
build-release
real launch
real CDP connect/evaluate
git grep for UIN/openid/token in tracked files
```
