package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestCDPEndpointForQuery(t *testing.T) {
	if got := cdpEndpointForQuery("ws://127.0.0.1:62000"); got != "127.0.0.1:62000" {
		t.Fatalf("cdpEndpointForQuery=%q", got)
	}
	if got := cdpEndpointForQuery("127.0.0.1:62000"); got != "127.0.0.1:62000" {
		t.Fatalf("cdpEndpointForQuery=%q", got)
	}
}

func TestReadDevtoolsPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DevToolsActivePort")
	if err := os.WriteFile(path, []byte("45678\n/devtools/browser/id\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readDevtoolsPort(path)
	if err != nil || got != "45678" {
		t.Fatalf("readDevtoolsPort=%q err=%v", got, err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDevtoolsPort(path); err == nil {
		t.Fatal("empty port file must fail")
	}
}

func TestVerifyDevtoolsFrontend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"Browser":"test"}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyDevtoolsFrontend(u.Port()); err != nil {
		t.Fatalf("verifyDevtoolsFrontend failed: %v", err)
	}
}

func TestCDPEndpointFromTarget(t *testing.T) {
	cases := map[string]string{
		"ws://127.0.0.1:62000": "ws://127.0.0.1:62000",
		"devtools://devtools/bundled/inspector.html?ws=127.0.0.1:62000":   "127.0.0.1:62000",
		"devtools://devtools/bundled/inspector.html?ws=127.0.0.1%3A62000": "127.0.0.1:62000",
		"http://127.0.0.1:62000/json/list":                                "",
	}
	for in, want := range cases {
		if got := cdpEndpointFromTarget(in); got != want {
			t.Fatalf("cdpEndpointFromTarget(%q)=%q, want %q", in, got, want)
		}
	}
}
