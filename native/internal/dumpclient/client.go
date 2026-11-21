// Package dumpclient is the Go-side client for the wxapkg-dump HTTP API.
// wxapkg-dump is a Rust sidecar; all login and package-export work is done
// through its local HTTP endpoints. Credentials never leave the sidecar.
package dumpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultBase   = "http://127.0.0.1:8788"
	healthPath    = "/api/health"
	statusPath    = "/api/ilink/status"
	loginQRPath   = "/api/ilink/login/qr"
	loginCheck    = "/api/ilink/login/check"
	downloadPath  = "/api/ilink/download"
	recentPath    = "/api/ilink/recent"
	defaultExeDir = `E:\wxxcx\wxapkg-dump-bin\wxapkg-dump.exe`
)

// Options configures the sidecar connection and process.
type Options struct {
	BaseURL   string // HTTP base, default http://127.0.0.1:8788
	ExePath   string // wxapkg-dump binary; default from WXAPKG_DUMP_EXE or known path
	Workspace string // server --workspace; empty means the binary's default
	AdminKey  string // optional x-admin-key
	AutoStart bool // start the sidecar if not reachable
	Logf      func(string, ...any)
}

// Client talks to one wxapkg-dump sidecar.
type Client struct {
	opts Options
	http *http.Client
	mu   sync.Mutex
	cmd  *exec.Cmd
}

// Envelope is the common response shape of wxapkg-dump's HTTP API.
type Envelope struct {
	OK      bool            `json:"ok"`
	Command string          `json:"command"`
	Version string          `json:"version"`
	Protocol int            `json:"protocol"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
}

// Status is the public account state returned by /api/ilink/status.
type Status struct {
	Implementation string `json:"implementation"`
	Account        *struct {
		NickName     string `json:"nickName"`
		Avatar       string `json:"avatar"`
		Status       string `json:"status"`
		StatusEn     string `json:"statusEn"`
		Online       bool   `json:"online"`
		OpenID       string `json:"openid"`
		LastRefresh  int64  `json:"lastRefreshAt"`
	} `json:"account"`
	Warnings []string `json:"warnings"`
}

// LoginQR is the response of /api/ilink/login/qr.
type LoginQR struct {
	SessionID string `json:"sessionId"`
	QRContent string `json:"qrContent"`
	QRImage   string `json:"qrImage"`
	ExpiresIn int    `json:"expiresIn"`
}

// LoginCheck is the response of /api/ilink/login/check.
type LoginCheck struct {
	Status   string `json:"status"`
	NickName string `json:"nickName"`
	Avatar   string `json:"avatar"`
	OpenID   string `json:"openid"`
	Message  string `json:"message"`
}

// DownloadOptions maps to /api/ilink/download's request body.
type DownloadOptions struct {
	AppID        string `json:"appid,omitempty"`
	Username     string `json:"username,omitempty"`
	Out          string `json:"out"`
	VersionType  int    `json:"versionType,omitempty"`
	Version      string `json:"version,omitempty"`
	AllPackages  bool   `json:"allPackages,omitempty"`
	Concurrency  int    `json:"concurrency,omitempty"`
	Unpack       bool   `json:"unpack,omitempty"`
	Beautify     bool   `json:"beautify,omitempty"`
}

// DownloadResult is the selected subset of /api/ilink/download's data.
type DownloadResult struct {
	AppID       string `json:"appid"`
	Username    string `json:"username"`
	OutDir      string `json:"outDir"`
	UnpackedDir string `json:"unpackedDir"`
	Packages    []struct {
		Module string `json:"module"`
		Type   int    `json:"type"`
		Size   int64  `json:"size"`
		Path   string `json:"path"`
		Error  string `json:"error"`
	} `json:"packages"`
}

func New(o Options) *Client {
	if o.BaseURL == "" { o.BaseURL = defaultBase }
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.ExePath == "" {
		if v := os.Getenv("WXAPKG_DUMP_EXE"); v != "" { o.ExePath = v } else { o.ExePath = defaultExeDir }
	}
	return &Client{opts: o, http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) logf(format string, args ...any) {
	if c.opts.Logf != nil { c.opts.Logf(format, args...) }
}

func (c *Client) baseHost() string {
	u := strings.TrimPrefix(c.opts.BaseURL, "http://")
	if i := strings.IndexAny(u, "/:"); i > 0 { return u[:i] }
	return u
}
func (c *Client) port() int {
	u := strings.TrimPrefix(c.opts.BaseURL, "http://")
	if i := strings.LastIndex(u, ":"); i > 0 {
		n, _ := strconv.Atoi(strings.TrimPrefix(u[i+1:], "/"))
		return n
	}
	return 80
}

// Reachable reports whether the sidecar's HTTP API is up.
func (c *Client) Reachable(ctx context.Context) bool {
	d := net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.baseHost(), strconv.Itoa(c.port())))
	if err != nil { return false }
	conn.Close()
	return true
}

// Ensure starts the sidecar process when AutoStart is set and it is not up.
func (c *Client) Ensure(ctx context.Context) error {
	if c.Reachable(ctx) { return nil }
	if !c.opts.AutoStart { return errors.New("wxapkg-dump server 未运行") }
	if _, err := os.Stat(c.opts.ExePath); err != nil {
		return fmt.Errorf("wxapkg-dump 不存在: %s", c.opts.ExePath)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Reachable(ctx) { return nil }
	args := []string{"serve", "--bind", net.JoinHostPort(c.baseHost(), strconv.Itoa(c.port()))}
	if c.opts.Workspace != "" { args = append(args, "--workspace", c.opts.Workspace) }
	cmd := exec.CommandContext(ctx, c.opts.ExePath, args...)
	// Detached from GUI console on Windows: CREATE_NO_WINDOW (0x08000000).
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil { return err }
	c.cmd = cmd
	c.logf("wxapkg-dump sidecar 已启动：%s", c.opts.BaseURL)
	// Wait for the port to come up, bounded and event-driven (no fixed sleeps).
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if c.Reachable(ctx) { return nil }
		select {
		case <-ticker.C:
		case <-deadline.C:
			return errors.New("wxapkg-dump server 启动超时")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Stop terminates a sidecar started by this client.
func (c *Client) Stop() {
	c.mu.Lock(); defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		c.cmd = nil
		c.logf("wxapkg-dump sidecar 已停止")
	}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var rdr io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil { return err }
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.opts.BaseURL+path, rdr)
	if err != nil { return err }
	if in != nil { req.Header.Set("Content-Type", "application/json") }
	if c.opts.AdminKey != "" { req.Header.Set("x-admin-key", c.opts.AdminKey) }
	resp, err := c.http.Do(req)
	if err != nil { return err }
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil { return err }
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("wxapkg-dump 响应无效 (HTTP %d): %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}
	if !env.OK {
		if env.Error != nil {
			return fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return fmt.Errorf("wxapkg-dump 返回失败 (HTTP %d)", resp.StatusCode)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil { return err }
	}
	return nil
}

func min(a, b int) int { if a < b { return a }; return b }

// Status returns the sidecar's public account state.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.do(ctx, http.MethodGet, statusPath, nil, &s)
	return s, err
}

// LoginQR creates a QR login session.
func (c *Client) LoginQR(ctx context.Context) (LoginQR, error) {
	var q LoginQR
	err := c.do(ctx, http.MethodPost, loginQRPath, map[string]any{}, &q)
	return q, err
}

// CheckQR polls a QR login session.
func (c *Client) CheckQR(ctx context.Context, sessionID string) (LoginCheck, error) {
	var s LoginCheck
	err := c.do(ctx, http.MethodPost, loginCheck, map[string]any{"sessionId": sessionID}, &s)
	return s, err
}

// Download exports a mini program's packages from the remote service.
func (c *Client) Download(ctx context.Context, o DownloadOptions) (DownloadResult, error) {
	var d DownloadResult
	if o.AppID == "" && o.Username == "" { return d, errors.New("appid 或 username 必填") }
	if o.Out == "" { return d, errors.New("导出目录不能为空") }
	if o.Concurrency < 1 || o.Concurrency > 10 { o.Concurrency = 3 }
	// The sidecar's HTTP call is synchronously blocking; use a generous timeout.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err := c.do(ctx, http.MethodPost, downloadPath, o, &d)
	return d, err
}

// ResolveExe returns the configured binary path (for diagnostics).
func (c *Client) ResolveExe() string { return c.opts.ExePath }
func (c *Client) BaseURL() string { return c.opts.BaseURL }
func (c *Client) Workspace() string { return c.opts.Workspace }

// Ensure the import of filepath is used on all platforms for path hygiene.
var _ = filepath.Clean
