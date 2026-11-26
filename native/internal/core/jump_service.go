package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"wmpfworkbench/internal/cdp"
)

func cdpValue(raw json.RawMessage) (map[string]any, error) {
	var envelope struct {
		Result struct {
			Value map[string]any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	return envelope.Result.Value, nil
}

func cdpEval(ctx context.Context, conn *cdp.Session, expression string) (map[string]any, error) {
	raw, err := conn.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": false,
	}, "")
	if err != nil {
		return nil, err
	}
	return cdpValue(raw)
}

func cdpTap(ctx context.Context, conn *cdp.Session, x, y float64) error {
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		buttons := 1
		if typ == "mouseReleased" {
			buttons = 0
		}
		_, err := conn.Send(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": typ, "x": x, "y": y, "button": "left", "buttons": buttons,
			"clickCount": 1, "pointerType": "touch",
		}, "")
		if err != nil {
			return err
		}
	}
	return nil
}

func cdpTouchEvents(x, y float64) []map[string]any {
	return []map[string]any{
		{"type": "touchStart", "touchPoints": []map[string]any{{"x": x, "y": y, "id": 1}}},
		{"type": "touchEnd", "touchPoints": []map[string]any{}},
	}
}

func cdpTouchTap(ctx context.Context, conn *cdp.Session, x, y float64) error {
	// XWeb confirmation buttons require a trusted touch chain. mousePressed/
	// mouseReleased often leaves the confirmation dialog in place; touchStart/
	// touchEnd was verified to complete the jump for target wxacdc0917b2fdfe6e.
	for _, params := range cdpTouchEvents(x, y) {
		if _, err := conn.Send(ctx, "Input.dispatchTouchEvent", params, ""); err != nil {
			return err
		}
	}
	return nil
}

func rectOf(v any) (float64, float64, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return 0, 0, false
	}
	x, okX := m["x"].(float64)
	y, okY := m["y"].(float64)
	return x, y, okX && okY
}

func nativeJumpFlagPaths() (targetFlag, armFlag string) {
	dir := os.TempDir()
	return filepath.Join(dir, "wmpf-jump-target-appid.txt"),
		filepath.Join(dir, "wmpf-jump-hook-arm.flag")
}

func armNativeJumpHook(target string) error {
	targetFlag, armFlag := nativeJumpFlagPaths()
	if err := os.WriteFile(targetFlag, []byte(target), 0600); err != nil {
		return err
	}
	return os.WriteFile(armFlag, []byte("1"), 0600)
}

func (s *Service) jumpMiniProgram(ctx context.Context, args jumpArgs) (any, error) {
	if err := armNativeJumpHook(args.TargetAppID); err != nil {
		return map[string]any{"ok": false, "stage": "arm_native_hook", "error": err.Error()}, nil
	}
	conn := s.current()
	if conn == nil {
		return notConnected(), nil
	}
	if _, err := conn.Send(ctx, "Runtime.enable", map[string]any{}, ""); err != nil {
		return map[string]any{"ok": false, "stage": "runtime_enable", "error": err.Error()}, nil
	}
	inject, err := cdpEval(ctx, conn, buildInjectJumpScript(args))
	if err != nil {
		return map[string]any{"ok": false, "stage": "inject", "error": err.Error()}, nil
	}
	if ok, _ := inject["ok"].(bool); !ok {
		return map[string]any{"ok": false, "stage": "inject", "error": inject["error"], "detail": inject}, nil
	}

	rect, err := cdpEval(ctx, conn, buildRectScript(args.ButtonSelector, args.ConfirmSelector))
	if err != nil {
		return map[string]any{"ok": false, "stage": "source_button", "error": err.Error()}, nil
	}
	x, y, ok := rectOf(rect["source"])
	if !ok {
		// Mini-program UI is not exposed as ordinary DOM; fall back to the
		// historically verified Tea Yan Yue Se button point.
		x, y = args.SourceX, args.SourceY
		ok = x > 0 && y > 0
	}
	if !ok {
		return map[string]any{"ok": false, "stage": "source_button", "error": "source button not visible and no fallback coordinates", "detail": rect}, nil
	}
	if err := cdpTap(ctx, conn, x, y); err != nil {
		return map[string]any{"ok": false, "stage": "source_tap", "error": err.Error()}, nil
	}

	confirmTapped := false
	deadline := time.Now().Add(jumpTimeout(args))
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var evidence map[string]any
	for {
		if now := time.Now(); now.After(deadline) {
			break
		}
		rect, err = cdpEval(ctx, conn, buildRectScript(args.ButtonSelector, args.ConfirmSelector))
		if err == nil && args.AutoConfirm && !confirmTapped {
			if cx, cy, found := rectOf(rect["confirm"]); found {
				if err := cdpTouchTap(ctx, conn, cx, cy); err == nil {
					confirmTapped = true
				}
			}
		}
		evidence, _ = cdpEval(ctx, conn, `(() => { const f=document.querySelector('iframe[name=appContext]'); const w=f && f.contentWindow; return w ? (w.__wmpfJumpEvidence || null) : null; })()`)
		active := ""
		for _, sess := range s.Sessions() {
			if sess["active_appid"] == args.TargetAppID || sess["appid"] == args.TargetAppID {
				active = args.TargetAppID
				break
			}
		}
		if active != "" {
			return map[string]any{
				"ok": true, "stage": "done", "target": args.TargetAppID,
				"active_appid": active, "jumped": true,
				"confirm_tapped": confirmTapped, "evidence": evidence,
			}, nil
		}
		if state, _ := evidence["state"].(string); state == "fail" {
			return map[string]any{
				"ok": false, "stage": "jump_api", "target": args.TargetAppID,
				"confirm_tapped": confirmTapped, "evidence": evidence,
			}, nil
		}
		select {
		case <-ctx.Done():
			return map[string]any{"ok": false, "stage": "timeout", "error": ctx.Err().Error(), "evidence": evidence}, nil
		case <-ticker.C:
		}
	}
	return map[string]any{
		"ok": false, "stage": "timeout", "target": args.TargetAppID,
		"confirm_tapped": confirmTapped, "evidence": evidence,
		"error": fmt.Sprintf("target %s did not become active within %dms", args.TargetAppID, args.TimeoutMS),
	}, nil
}
