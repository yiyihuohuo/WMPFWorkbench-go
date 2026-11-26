package protocol

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in server integration check. It sends only a genuine supplied ticket,
// launches no official executable, and never prints credential/session values.
func TestLiveLoginBufferManualAuth(t *testing.T) {
	path := os.Getenv("YYB_LOGIN_CHECK_STATE")
	if path == "" {
		t.Skip("set YYB_LOGIN_CHECK_STATE to a private yyb-login-check checkpoint")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read private checkpoint")
	}
	var input struct {
		LoginBuffer string `json:"login_buffer"`
	}
	if json.Unmarshal(data, &input) != nil || input.LoginBuffer == "" {
		t.Fatal("private checkpoint contains no ticket")
	}
	cfg := DefaultConfig()
	cfg.LoginTimeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	session, err := NewPool(cfg, nil).loginAndSession(ctx, input.LoginBuffer, "")
	if err != nil {
		// Report a bounded stage label; protocol errors can include server data.
		stage := "unknown"
		for _, name := range []string{"HTTPDNS", "LongLink", "MMTLS", "HybridEcdhResponse", "ManualAuthResponse", "PSK", "deadline"} {
			if strings.Contains(err.Error(), name) {
				stage = name
			}
		}
		t.Fatalf("real manual authentication failed; stage=%s", stage)
	}
	if session.Session.UIN == 0 || len(session.Session.SendKey) == 0 || len(session.Session.RecvKey) == 0 {
		t.Fatal("real manual authentication did not establish a usable session")
	}
	t.Log("real manual authentication succeeded; session keys and identity withheld")
}
