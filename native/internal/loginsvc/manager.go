// Package loginsvc owns the lifecycle of the separate Go login service process.
// It keeps credentials in the service and never logs the desktop token.
package loginsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/process"
	"yyb_go/embedded"
)

type Status struct {
	Running bool   `json:"running"`
	Healthy bool   `json:"healthy"`
	Owned   bool   `json:"owned"`
	PID     int    `json:"pid,omitempty"`
	URL     string `json:"url"`
	Binary  string `json:"binary"`
	Port    int    `json:"port"`
	Error   string `json:"error,omitempty"`
}

type Manager struct {
	Paths   app.Paths
	Config  *config.Store
	HTTP    *http.Client
	Command func(name string, args []string, env []string) *exec.Cmd
	// Logf, when set, receives human-readable diagnostics about every request
	// and lifecycle decision. The GUI wires it to its log panel.
	Logf func(format string, args ...any)

	mu   sync.Mutex
	proc *exec.Cmd
	done chan struct{}

	embeddedService *embedded.Service
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

func New(p app.Paths, cfg *config.Store) *Manager {
	return &Manager{
		Paths:  p,
		Config: cfg,
		HTTP:   &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second},
		Command: func(name string, args []string, env []string) *exec.Cmd {
			cmd := exec.Command(name, args...)
			cmd.Env = env
			cmd.Dir = p.Root
			process.Hide(cmd)
			return cmd
		},
	}
}

// Binary is the login service executable. A bundled copy injected through
// WMPF_LOGIN_BIN wins over the original project's desktop/bin layout.
func (m *Manager) Binary() string {
	if value := os.Getenv("WMPF_LOGIN_BIN"); value != "" {
		return value
	}
	return filepath.Join(m.Paths.Root, "desktop", "bin", "yyb-login-service.exe")
}

type settings struct {
	URL         string
	Port        int
	Token       string
	ResourceDir string
}

func (m *Manager) settings() (settings, error) {
	values, err := m.Config.Read()
	if err != nil {
		return settings{}, err
	}
	port, _ := values["login_port"].(float64)
	if port == 0 {
		port = 18766
	}
	s := settings{Port: int(port), ResourceDir: filepath.Join(m.Paths.Data, "desktop", "login")}
	s.URL = config.String(values, "login_url")
	if s.URL == "" {
		s.URL = fmt.Sprintf("http://127.0.0.1:%d", s.Port)
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		return s, errors.New("登录服务地址必须是本机 http://127.0.0.1")
	}
	// Use the shared, lock-protected generator so account.Client and this
	// service cannot mint different tokens on a fresh config.
	s.Token, err = m.Config.EnsureToken(context.Background(), "login_token")
	if err != nil {
		return s, err
	}
	return s, nil
}

// Request performs an authenticated desktop request and unwraps the service
// envelope. Callers never see the desktop token.
func (m *Manager) Request(ctx context.Context, path, method string, body any) (any, error) {
	s, err := m.settings()
	if err != nil {
		m.logf("登录服务请求 %s %s 失败：读取配置出错：%v", method, path, err)
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			m.logf("登录服务请求 %s %s 失败：序列化请求体出错：%v", method, path, err)
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.URL+path, reader)
	if err != nil {
		m.logf("登录服务请求 %s %s 失败：构造请求出错：%v", method, path, err)
		return nil, errors.New("登录服务请求无效")
	}
	request.Header.Set("Authorization", "Bearer "+s.Token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 65 * time.Second}
	start := time.Now()
	response, err := client.Do(request)
	if err != nil {
		m.logf("登录服务请求 %s %s 连接失败（耗时 %s）：%v", method, path, time.Since(start).Round(time.Millisecond), err)
		return nil, fmt.Errorf("登录服务连接失败或请求超时（%s %s）：%w", method, path, err)
	}
	defer response.Body.Close()
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&envelope)
	if decodeErr != nil || envelope.Code != 0 || envelope.Data == nil {
		msg := envelope.Msg
		if msg == "" {
			msg = "无错误信息"
		}
		m.logf("登录服务请求 %s %s 返回异常（HTTP %d，code=%d，msg=%s，耗时 %s）decodeErr=%v",
			method, path, response.StatusCode, envelope.Code, msg, time.Since(start).Round(time.Millisecond), decodeErr)
		if response.StatusCode == http.StatusUnauthorized {
			return nil, errors.New("登录服务鉴权失败（HTTP 401）：login_token 不一致，请重启程序")
		}
		return nil, fmt.Errorf("登录服务返回无效数据（HTTP %d，code=%d，msg=%s）", response.StatusCode, envelope.Code, msg)
	}
	var result any
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		m.logf("登录服务请求 %s %s 返回数据无法解析：%v", method, path, err)
		return nil, errors.New("登录服务返回无效数据")
	}
	m.logf("登录服务请求 %s %s 成功（HTTP %d，耗时 %s）", method, path, response.StatusCode, time.Since(start).Round(time.Millisecond))
	return result, nil
}

func (m *Manager) QRCreate(ctx context.Context) (any, error) {
	return m.Request(ctx, "/qr?as_base64=true", "POST", map[string]any{})
}

func (m *Manager) QRPoll(ctx context.Context, session string) (any, error) {
	return m.Request(ctx, "/qr/"+url.PathEscape(session)+"/poll", "GET", nil)
}

func (m *Manager) QRConfirm(ctx context.Context, session string) (any, error) {
	return m.Request(ctx, "/qr/"+url.PathEscape(session)+"/confirm", "POST", map[string]any{})
}

func (m *Manager) healthy(ctx context.Context, s settings) bool {
	request, err := http.NewRequestWithContext(ctx, "GET", s.URL+"/health", nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+s.Token)
	response, err := m.HTTP.Do(request)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == 200
}

// Ensure returns a healthy service, starting the owned process when the port is
// free. An already running external service is reused but never killed by us.
func (m *Manager) Ensure(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.settings()
	if err != nil {
		return Status{URL: s.URL, Binary: m.Binary(), Port: s.Port, Error: err.Error()}, err
	}
	status := Status{URL: s.URL, Binary: m.Binary(), Port: s.Port}
	if m.healthy(ctx, s) {
		status.Running, status.Healthy = true, true
		if m.proc != nil && m.proc.Process != nil {
			status.Owned, status.PID = true, m.proc.Process.Pid
		}
		m.logf("登录服务已就绪（复用现有实例 %s）", s.URL)
		return status, nil
	}
	// The login service runs inside this process; no helper binary is released.
	if m.embeddedService != nil {
		// A single failed /health is not proof the service is dead: it may still
		// be warming up or briefly busy. Restarting on the first miss is what
		// dropped QR sessions mid-scan, so give it a short grace window first.
		m.logf("登录服务健康检查未通过，重试中（不会立即重启）…")
		if m.waitHealthy(ctx, s, 6*time.Second) {
			status.Running, status.Healthy, status.Owned, status.PID = true, true, true, os.Getpid()
			m.logf("登录服务恢复响应，继续复用")
			return status, nil
		}
		m.logf("登录服务持续无响应，重启服务")
		m.embeddedService.Stop(context.Background())
		m.embeddedService = nil
	}
	os.MkdirAll(s.ResourceDir, 0700)
	m.logf("正在启动登录服务（%s，数据目录 %s）", s.URL, s.ResourceDir)
	service, err := embedded.Start(s.ResourceDir, s.Port, s.Token)
	if err != nil {
		status.Error = "登录服务启动失败：" + err.Error()
		m.logf("登录服务启动失败：%v", err)
		return status, errors.New(status.Error)
	}
	m.embeddedService = service
	if m.waitHealthy(ctx, s, 25*time.Second) {
		status.Running, status.Healthy, status.Owned, status.PID = true, true, true, os.Getpid()
		m.logf("登录服务已就绪（%s）", s.URL)
		return status, nil
	}
	m.embeddedService.Stop(context.Background())
	m.embeddedService = nil
	status.Error = fmt.Sprintf("登录服务健康检查超时：端口 %d 无响应（可能被其他程序占用，或服务启动后立即退出）", s.Port)
	m.logf("%s", status.Error)
	return status, errors.New(status.Error)
}

// waitHealthy polls /health until it succeeds or the window elapses.
func (m *Manager) waitHealthy(ctx context.Context, s settings, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if m.healthy(ctx, s) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
	return m.healthy(ctx, s)
}

// Stop requests the graceful shutdown endpoint, then terminates only a process
// this manager started.
func (m *Manager) Stop(ctx context.Context) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.settings()
	status := Status{URL: s.URL, Binary: m.Binary(), Port: s.Port}
	if err != nil {
		status.Error = err.Error()
		return status
	}
	if m.embeddedService != nil {
		m.embeddedService.Stop(ctx)
		m.embeddedService = nil
		status.Running, status.Healthy = false, false
		return status
	}
	if m.healthy(ctx, s) {
		request, _ := http.NewRequestWithContext(ctx, "POST", s.URL+"/internal/shutdown", strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer "+s.Token)
		request.Header.Set("Content-Type", "application/json")
		if response, err := m.HTTP.Do(request); err == nil {
			response.Body.Close()
		}
	}
	if m.proc == nil {
		status.Running = m.healthy(ctx, s)
		status.Healthy = status.Running
		return status
	}
	status.Running, status.Owned, status.PID = true, true, m.proc.Process.Pid
	select {
	case <-m.done:
	case <-ctx.Done():
	case <-time.After(25 * time.Second):
	}
	if m.proc != nil && m.proc.Process != nil {
		select {
		case <-m.done:
		default:
			m.proc.Process.Kill()
			select {
			case <-m.done:
			case <-time.After(5 * time.Second):
			}
		}
	}
	m.proc, m.done = nil, nil
	status.Running = false
	return status
}

// Status reports the current state without starting or stopping anything.
func (m *Manager) Status(ctx context.Context) Status {
	s, err := m.settings()
	status := Status{URL: s.URL, Binary: m.Binary(), Port: s.Port}
	if err != nil {
		status.Error = err.Error()
		return status
	}
	if m.healthy(ctx, s) {
		status.Running, status.Healthy = true, true
		m.mu.Lock()
		if m.embeddedService != nil {
			status.Owned, status.PID = true, os.Getpid()
		} else if m.proc != nil && m.proc.Process != nil {
			status.Owned, status.PID = true, m.proc.Process.Pid
		}
		m.mu.Unlock()
	}
	return status
}
