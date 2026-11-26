# Phase A incident analysis

## What happened

Phase A added typed `GetUserInfo`/`Login` callbacks to `hostcpp/main.cpp` and rebuilt `dist/trim`. A launch was started and failed at `ilink.ThirdAppLogin.result error_code=-10`, then the desktop session was lost and later recreated.

## Evidence

- OS uptime stayed continuous: last boot remained `2026-11-24 18:02:35`, so this was not a kernel reboot.
- Current `dwm`, `winlogon`, and `explorer` all started around `02:27:11`–`02:27:33`, after the incident.
- System log at `02:25:09` shows many per-user services terminated unexpectedly and restarted:
  - Windows Push Notifications User Service
  - Clipboard User Service
  - Udk User Service
  - DevicesFlow
  - User Data Access/Storage
  - Web threat defense user service
- Security log shows a new interactive type-2 logon for `<user>` at `02:27:31`.
- No Kernel-Power bugcheck or system shutdown record was found.
- Only unrelated EdgeUpdate crash report was found in WER.

Conclusion: this was an interactive user-session teardown/recreate, not an OS reboot.

## My unsafe action

Before rebuilding, I used a blanket `Stop-Process` matching `WeChatAppEx`. That can kill every WMPF/Androws runtime process on the machine, including processes unrelated to this worktree. This is unsafe and must not be repeated.

Future process control must filter by command line containing this worktree path, or avoid process termination entirely.

## Phase A status

The typed callback code is quarantined as suspect. The official ABI analysis remains valid, but the Phase A runtime build is not trusted.

Before any future launch:

1. rebuild from known-good source;
2. do not blanket-kill any process;
3. run only with per-session log capture;
4. stop immediately on any session-level anomaly.
