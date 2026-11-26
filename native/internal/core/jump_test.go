package core

import (
	"strings"
	"testing"
)

func TestValidateJumpArgs(t *testing.T) {
	valid := map[string]any{"target_appid": "wxde8ac0a21135c07d"}
	args, err := validateJumpArgs(valid)
	if err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	if args.SourceHandler != "goYY" || args.ButtonSelector != ".yy-btn" || args.ConfirmSelector != "button.btn_allow" {
		t.Fatalf("unexpected defaults: %+v", args)
	}
	if args.TimeoutMS != 20000 || !args.AutoConfirm {
		t.Fatalf("unexpected timeout/auto: %+v", args)
	}
	if args.SourceX != 70 || args.SourceY != 260 {
		t.Fatalf("unexpected source fallback: %+v", args)
	}
	for _, bad := range []map[string]any{
		{},
		{"target_appid": "not-an-appid"},
		{"target_appid": "wxde8ac0a21135c07d", "source_handler": ""},
		{"target_appid": "wxde8ac0a21135c07d", "button_selector": ""},
	} {
		if _, err := validateJumpArgs(bad); err == nil {
			t.Fatalf("expected rejection for %v", bad)
		}
	}
}

func TestBuildInjectJumpScript(t *testing.T) {
	args := jumpArgs{TargetAppID: "wxde8ac0a21135c07d", SourceHandler: "goYY"}
	js := buildInjectJumpScript(args)
	for _, want := range []string{
		"iframe[name=appContext]", "__wmpfJumpEvidence", "navigateToMiniProgram",
		"goYY", "wxde8ac0a21135c07d",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("inject script missing %q", want)
		}
	}
	if strings.Contains(js, `target_appid"; alert`) {
		t.Fatal("script should JSON-quote injected values")
	}
}

func TestBuildRectScript(t *testing.T) {
	js := buildRectScript(".yy-btn", "button.btn_allow")
	for _, want := range []string{"getBoundingClientRect", ".yy-btn", "btn_allow", "viewport"} {
		if !strings.Contains(js, want) {
			t.Fatalf("rect script missing %q", want)
		}
	}
}
