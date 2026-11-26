package dumpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKeepAliveCallsSidecarEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != keepAlivePath || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(envOK("ilink account keepalive", map[string]any{
			"account": map[string]any{"online": true, "openid": "owTEST"},
			"all":     false,
		}))
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL})
	out, err := c.KeepAlive(context.Background())
	if err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	if out.Account == nil || !out.Account.Online || out.All {
		t.Fatalf("unexpected result: %+v", out)
	}
}
