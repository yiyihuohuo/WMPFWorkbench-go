package dumpclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnsureRejectsNonEnvelopeServiceOnPort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`foreign-service`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, AutoStart: false})
	err := c.Ensure(t.Context())
	if err == nil {
		t.Fatal("expected an unreachable-sidecar error")
	}
	if !strings.Contains(err.Error(), "未运行") {
		t.Fatalf("unexpected error: %v", err)
	}
}
