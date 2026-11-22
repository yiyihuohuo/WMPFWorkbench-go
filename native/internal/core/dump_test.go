package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/dumpclient"
)

func dumpTestService(t *testing.T, base string) *Service {
	p, _ := app.Resolve(t.TempDir())
	cfg := config.New(t.TempDir() + "/config.json")
	_, _ = cfg.Update(context.Background(), map[string]any{"login_mode": "dump"})
	client := dumpclient.New(dumpclient.Options{BaseURL: base, AutoStart: false})
	return &Service{Paths: p, Config: cfg, Dump: client}
}

func TestLoginQRCreateMapsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "command": "ilink login qr",
			"version": "1.0.0", "protocol": 1, "data": map[string]any{
				"sessionId": "sid-1", "qrContent": "https://example/qr/sid-1",
				"qrImage": "data:image/jpeg;base64,AAAA", "expiresIn": 240}})
	}))
	defer srv.Close()
	s := dumpTestService(t, srv.URL)
	q, err := s.LoginQRCreate(context.Background())
	if err != nil { t.Fatal(err) }
	if q["session_id"] != "sid-1" || q["image_base64"] != "data:image/jpeg;base64,AAAA" {
		t.Fatalf("unmapped fields: %+v", q)
	}
}

func TestLoginQRPollPersistsOpenID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "command": "ilink login check",
			"version": "1.0.0", "protocol": 1, "data": map[string]any{
				"status": "confirmed", "openid": "owTEST", "nickName": "tester"}})
	}))
	defer srv.Close()
	s := dumpTestService(t, srv.URL)
	p, err := s.LoginQRPoll(context.Background(), "sid-1")
	if err != nil { t.Fatal(err) }
	if p["status"] != "confirmed" { t.Fatalf("status=%v", p["status"]) }
	v, _ := s.Config.Read()
	if config.String(v, "selected_account") != "owTEST" {
		t.Fatal("confirmed openid not persisted")
	}
}
