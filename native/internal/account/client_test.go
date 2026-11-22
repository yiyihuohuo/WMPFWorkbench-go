package account

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wmpfworkbench/internal/config"
)

func TestManagedSessionUsesCorrectSDKNamespace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("missing auth")
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"login_buffer": "dGlja2V0", "credentials": map[string]any{"openid": "main", "accesstoken": "main+token", "guid": "guid", "expires_at": time.Now().Unix() + 3600, "wxapp_openid": "sdk", "wxapp_logintype": "WX", "wxapp_accesstoken": "sdk+token", "wxapp_scope": "snsapi_runtime_pcsdk", "wxapp_access_token_expire_time": time.Now().Unix() + 3600}}})
	}))
	defer srv.Close()
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	cfg.Update(context.Background(), map[string]any{"login_mode": "legacy", "login_url": srv.URL, "login_token": "fixture", "selected_account": "main"})
	session, err := New(cfg).Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.SDKToken != "sdk+token" || session.Pairs["accesstoken"] != "main+token" || !strings.Contains(session.Cookie, "accesstoken=main%2Btoken") {
		t.Fatal("wrong namespace/encoding")
	}
}

// An unknown SDK absolute expiry (0) is not proof of expiry: the login service
// always serializes the key. Treating 0 as expired forces a refresh on every
// launch, which rotates tokens far more often than the original implementation.
func TestManagedSessionSkipsRefreshWhenSDKExpiryUnknown(t *testing.T) {
	refreshed := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/accounts/refresh") {
			refreshed++
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"status": "alive"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"login_buffer": "dGlja2V0",
			"credentials": map[string]any{"openid": "main", "accesstoken": "main", "guid": "guid", "expires_at": time.Now().Unix() + 3600,
				"wxapp_openid": "sdk", "wxapp_accesstoken": "sdk", "wxapp_access_token_expire_time": 0, "wxapp_expires_in": 7200}}})
	}))
	defer srv.Close()
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	cfg.Update(context.Background(), map[string]any{"login_mode": "legacy", "login_url": srv.URL, "login_token": "fixture", "selected_account": "main"})
	if _, err := New(cfg).Session(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshed != 0 {
		t.Fatal("unknown SDK expiry forced a refresh", refreshed)
	}
}

func TestManagedSessionRefreshesOnlyWhenSDKExpiryIsNear(t *testing.T) {
	refreshed := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/accounts/refresh") {
			refreshed++
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"status": "alive"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"login_buffer": "dGlja2V0",
			"credentials": map[string]any{"openid": "main", "accesstoken": "main", "guid": "guid", "expires_at": time.Now().Unix() + 3600,
				"wxapp_openid": "sdk", "wxapp_accesstoken": "sdk", "wxapp_access_token_expire_time": time.Now().Unix() + 30}}})
	}))
	defer srv.Close()
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	cfg.Update(context.Background(), map[string]any{"login_mode": "legacy", "login_url": srv.URL, "login_token": "fixture", "selected_account": "main"})
	if _, err := New(cfg).Session(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshed != 1 {
		t.Fatal("near-expiry SDK token must refresh once", refreshed)
	}
}
