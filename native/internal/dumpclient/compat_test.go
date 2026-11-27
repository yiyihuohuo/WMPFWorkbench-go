package dumpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportsKeepAlive(t *testing.T) {
	cases := []struct {
		name  string
		code  int
		allow string
		want  bool
	}{
		{name: "options ok", code: http.StatusOK, want: true},
		{name: "allow post", code: http.StatusMethodNotAllowed, allow: "POST", want: true},
		{name: "old delete only", code: http.StatusMethodNotAllowed, allow: "DELETE", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.allow != "" {
					w.Header().Set("Allow", tc.allow)
				}
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()
			c := New(Options{BaseURL: srv.URL})
			if got := c.supportsKeepAlive(context.Background()); got != tc.want {
				t.Fatalf("supportsKeepAlive=%v want %v", got, tc.want)
			}
		})
	}
}

func TestParseListenerPIDs(t *testing.T) {
	out := `
  TCP    127.0.0.1:8788         0.0.0.0:0              LISTENING       1234
  TCP    127.0.0.1:8788         127.0.0.1:50000        ESTABLISHED     9999
  TCP    [::1]:8788             [::]:0                 LISTENING       1234
  TCP    127.0.0.1:8789         0.0.0.0:0              LISTENING       5678
`
	got := parseListenerPIDs(out, 8788)
	if len(got) != 1 || got[0] != 1234 {
		t.Fatalf("pids=%v", got)
	}
}

func TestIsManagedSidecarPath(t *testing.T) {
	root := filepath.Join("C:", "workbench", "data")
	inside := filepath.Join(root, "bin", "wxapkg-dump.exe")
	outside := filepath.Join("C:", "other", "wxapkg-dump.exe")
	if !isManagedSidecarPath(inside, root) {
		t.Fatal("inside path should be managed")
	}
	if isManagedSidecarPath(outside, root) {
		t.Fatal("outside path must be rejected")
	}
	if isManagedSidecarPath(filepath.Join(root, "bin", "other.exe"), root) {
		t.Fatal("wrong image name must be rejected")
	}
	if isManagedSidecarPath(inside, "") {
		t.Fatal("empty managed dir must be rejected")
	}
}

func TestReplaceIncompatibleSidecarSafety(t *testing.T) {
	oldInspect, oldKill := inspectListener, killPID
	defer func() { inspectListener, killPID = oldInspect, oldKill }()

	managed := t.TempDir()
	outside := t.TempDir()
	killed := false
	killPID = func(pid int) error {
		killed = true
		return nil
	}
	c := New(Options{BaseURL: "http://127.0.0.1:1", ManagedDir: managed})

	inspectListener = func(port int) (listenerProcess, error) {
		return listenerProcess{PID: 4321, Path: filepath.Join(outside, "wxapkg-dump.exe")}, nil
	}
	if err := c.replaceIncompatibleSidecar(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "不在工作台数据目录") {
		t.Fatalf("outside process should not be killed: %v", err)
	}
	if killed {
		t.Fatal("outside process was killed")
	}

	oldPath := filepath.Join(managed, "bin", "wxapkg-dump.exe")
	inspectListener = func(port int) (listenerProcess, error) {
		return listenerProcess{PID: 4321, Path: oldPath}, nil
	}
	c.opts.ExePath = oldPath
	if err := c.replaceIncompatibleSidecar(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "内置新版未成功释放") {
		t.Fatalf("same-path old binary must not be killed: %v", err)
	}
	if killed {
		t.Fatal("same-path old binary was killed")
	}

	c.opts.ExePath = filepath.Join(managed, "bin", "versions", "new", "wxapkg-dump.exe")
	if err := c.replaceIncompatibleSidecar(context.Background()); err != nil {
		t.Fatalf("managed process should be replaceable: %v", err)
	}
	if !killed {
		t.Fatal("managed old sidecar was not killed")
	}
}

func TestEnsureIncompatibleReportsOldProcess(t *testing.T) {
	oldInspect := inspectListener
	defer func() { inspectListener = oldInspect }()
	inspectListener = func(port int) (listenerProcess, error) {
		return listenerProcess{PID: 4321, Path: `C:\workbench\data\bin\wxapkg-dump.exe`}, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodOptions {
			w.Header().Set("Allow", "DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		fmt.Fprint(w, `{"ok":true,"command":"ilink status","version":"1.0.0","protocol":1,"data":{}}`)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, AutoStart: false, ManagedDir: `C:\workbench\data`})
	err := c.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "旧版 wxapkg-dump") ||
		!strings.Contains(err.Error(), "PID 4321") {
		t.Fatalf("unexpected error: %v", err)
	}
}
