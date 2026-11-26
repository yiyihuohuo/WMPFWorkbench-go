package loginsvc

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/config"
)

// Helper process used as a stand-in for the real login service binary.
func TestLoginServiceHelper(t *testing.T) {
	if os.Getenv("WMPF_TEST_LOGIN_HELPER") != "1" {
		return
	}
	time.Sleep(3 * time.Second)
	os.Exit(0)
}

func fixture(t *testing.T, url string, port int) (*Manager, string) {
	t.Helper()
	p, err := app.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.New(filepath.Join(t.TempDir(), "config.json"))
	if _, err = cfg.Update(context.Background(), map[string]any{"login_url": url, "login_port": float64(port), "login_token": "fixture"}); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(p.Root, "desktop", "bin", "yyb-login-service.exe")
	os.MkdirAll(filepath.Dir(binary), 0700)
	os.WriteFile(binary, []byte("fixture"), 0600)
	return New(p, cfg), binary
}

func TestEnsureReusesHealthyExternalService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && r.Header.Get("Authorization") == "Bearer fixture" {
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	port, _ := strconv.Atoi(server.URL[len("http://127.0.0.1:"):])
	m, _ := fixture(t, server.URL, port)
	started := 0
	m.Command = func(string, []string, []string) *exec.Cmd {
		started++
		return exec.Command(os.Args[0], "-test.run=TestLoginServiceHelper")
	}
	status, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || !status.Healthy || status.Owned || started != 0 {
		t.Fatal("healthy external service must be reused without starting a process", status, started)
	}
}

// TestWaitHealthyToleratesTransientFailure guards the fix for login-service
// restarts: a health check that fails once (service busy or still warming up)
// must not be treated as dead, otherwise the Ensure path stops and restarts the
// service mid-scan and drops the QR session.
func TestWaitHealthyToleratesTransientFailure(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	port, _ := strconv.Atoi(server.URL[len("http://127.0.0.1:"):])
	m, _ := fixture(t, server.URL, port)
	s, err := m.settings()
	if err != nil {
		t.Fatal(err)
	}
	if !m.waitHealthy(context.Background(), s, 3*time.Second) {
		t.Fatal("transient health failures must not be treated as a dead service")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestEnsureStartsAndWaitsForHealth(t *testing.T) {
	port := freePort(t)
	m, _ := fixture(t, "http://127.0.0.1:"+strconv.Itoa(port), port)
	hits := 0
	m.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		code := http.StatusServiceUnavailable
		if hits >= 3 {
			code = http.StatusOK
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: http.Header{}}, nil
	})
	m.Command = func(string, []string, []string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestLoginServiceHelper")
		cmd.Env = append(os.Environ(), "WMPF_TEST_LOGIN_HELPER=1")
		return cmd
	}
	status, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || !status.Healthy || !status.Owned || status.PID == 0 {
		t.Fatal("owned service must be started and become healthy", status)
	}
	if hits < 3 {
		t.Fatal("must poll until healthy", hits)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.Stop(ctx)
}

// The login service runs inside the desktop process now: no helper executable
// and no child PID, while the HTTP surface stays identical.
func TestEnsureStartsEmbeddedService(t *testing.T) {
	port := freePort(t)
	m, _ := fixture(t, "http://127.0.0.1:"+strconv.Itoa(port), port)
	m.HTTP.Timeout = 2 * time.Second
	status, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || !status.Healthy || !status.Owned || status.PID != os.Getpid() {
		t.Fatal("embedded service not reported as owned", status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	after := m.Stop(ctx)
	if after.Running {
		t.Fatal("embedded service still running after stop", after)
	}
}

func TestEnsureRejectsMissingBinary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	port, _ := strconv.Atoi(server.URL[len("http://127.0.0.1:"):])
	m, binary := fixture(t, server.URL, port)
	os.Remove(binary)
	started := 0
	m.Command = func(string, []string, []string) *exec.Cmd {
		started++
		return exec.Command("none")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := m.Ensure(ctx); err == nil {
		t.Fatal("missing binary must fail")
	}
	if started != 0 {
		t.Fatal("must not start a missing binary", started)
	}
}

func TestEnsureRejectsBusyPortWithoutHealthyService(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	m, _ := fixture(t, "http://127.0.0.1:"+strconv.Itoa(port), port)
	m.HTTP.Timeout = 200 * time.Millisecond
	m.Command = func(string, []string, []string) *exec.Cmd { return exec.Command("none") }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err = m.Ensure(ctx); err == nil {
		t.Fatal("busy port without a healthy service must fail instead of starting")
	}
}
