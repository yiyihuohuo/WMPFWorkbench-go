package dumpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type listenerProcess struct {
	PID       int
	Path      string
	IsSidecar bool // image name matches wxapkg-dump.exe; false means unknown listener
}

var (
	inspectListener  = queryListenerProcess
	inspectImagePath = processImagePath
	killPID          = func(pid int) error {
		p, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		return p.Kill()
	}
)

// supportsKeepAlive distinguishes the current sidecar from older builds where
// the dynamic /account/{openid} DELETE route makes the same path answer 405.
// The error return means the probe could not reach a conclusion; callers must
// not replace a process on an inconclusive probe.
func (c *Client) supportsKeepAlive(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, c.opts.BaseURL+keepAlivePath, nil)
	if err != nil {
		return false, err
	}
	if c.opts.AdminKey != "" {
		req.Header.Set("x-admin-key", c.opts.AdminKey)
	}
	cli := *c.http
	if cli.Timeout == 0 || cli.Timeout > 3*time.Second {
		cli.Timeout = 3 * time.Second
	}
	resp, err := cli.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	protocol := strings.TrimSpace(resp.Header.Get("x-api-protocol"))
	allowPost := false
	for _, method := range strings.Split(resp.Header.Get("Allow"), ",") {
		if strings.EqualFold(strings.TrimSpace(method), http.MethodPost) {
			allowPost = true
			break
		}
	}
	// Any CORS preflight can answer 2xx with an empty body. Require the
	// sidecar's protocol header or a real wxapkg-dump Envelope before treating
	// 2xx as proof that the keepalive route exists.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if protocol != "" || (readErr == nil && isSidecarEnvelope(body)) {
			return true, nil
		}
		return false, fmt.Errorf("OPTIONS %s 返回 HTTP %d 但不是 wxapkg-dump 响应（缺少 x-api-protocol 或有效 Envelope），无法确认版本", keepAlivePath, resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusMethodNotAllowed:
		if allowPost {
			return true, nil
		}
		return false, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("探测账号保活接口返回 HTTP %d", resp.StatusCode)
	}
}

func isSidecarEnvelope(body []byte) bool {
	var env Envelope
	return json.Unmarshal(body, &env) == nil && env.OK && env.Command != ""
}

func isSidecarImage(path string) bool {
	return strings.EqualFold(filepath.Base(strings.TrimSpace(path)), exeName)
}

func (c *Client) incompatibleSidecarError() error {
	port := c.port()
	info, err := inspectListener(port)
	if err != nil {
		return fmt.Errorf("账号保活探测不通过，且无法读取占用端口 %d 的进程信息（%v）；未自动终止；请检查端口占用或更新到最新 1.0.5 后重启工作台。", port, err)
	}
	if !info.IsSidecar && !isSidecarImage(info.Path) {
		return fmt.Errorf("端口 %d 被其他进程占用（PID %d，路径 %s），不是 wxapkg-dump；未自动终止；请检查端口占用。", port, info.PID, displayPath(info.Path))
	}
	return fmt.Errorf("检测到旧版 wxapkg-dump 正在占用端口 %d（PID %d，路径 %s）。该版本不支持账号保活接口 POST %s。请更新到最新 1.0.5 并重启工作台。", port, info.PID, displayPath(info.Path), keepAlivePath)
}

func (c *Client) replaceIncompatibleSidecar(ctx context.Context) error {
	port := c.port()
	info, err := inspectListener(port)
	if err != nil {
		return fmt.Errorf("账号保活探测不通过，但无法确认占用端口 %d 的进程：%w。为避免误杀，未自动终止；请更新到最新 1.0.5 并重启工作台。", port, err)
	}
	if !isManagedSidecarPath(info.Path, c.opts.ManagedDir) {
		return fmt.Errorf("检测到疑似旧版 wxapkg-dump（PID %d，路径 %s），但该路径不在工作台数据目录 %s 内。为避免误杀，未自动终止；请手动结束该进程后重启工作台。", info.PID, displayPath(info.Path), displayPath(c.opts.ManagedDir))
	}
	if samePath(info.Path, c.opts.ExePath) {
		return fmt.Errorf("检测到旧版 wxapkg-dump（PID %d，路径 %s），但工作台当前要启动的文件就是它：内置新版未成功释放，未自动终止。请重新下载最新 1.0.5 完整包并覆盖安装。", info.PID, displayPath(info.Path))
	}
	// The netstat snapshot can be stale: re-resolve the image path for this PID
	// immediately before killing so a recycled PID cannot be killed by mistake.
	currentPath, pathErr := inspectImagePath(info.PID)
	if pathErr != nil || !isManagedSidecarPath(currentPath, c.opts.ManagedDir) || !samePath(currentPath, info.Path) {
		return fmt.Errorf("终止前复核旧版 wxapkg-dump（PID %d）失败（复核路径 %s，原路径 %s）：%v。为避免误杀，未自动终止；请手动结束该进程后重启工作台。", info.PID, displayPath(currentPath), displayPath(info.Path), pathErr)
	}
	c.logf("检测到旧版 wxapkg-dump（PID %d，%s），正在终止并加载内置新版", info.PID, info.Path)
	if err := killPID(info.PID); err != nil {
		return fmt.Errorf("终止旧版 wxapkg-dump（PID %d）失败：%w；请手动结束该进程后重启工作台。", info.PID, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !c.Reachable(ctx) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("旧版 wxapkg-dump（PID %d）已终止，但端口 %d 仍未释放；请重启工作台", info.PID, port)
}

func displayPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return "未知"
	}
	return path
}

func isManagedSidecarPath(path, managedDir string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(managedDir) == "" {
		return false
	}
	if !isSidecarImage(path) {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(managedDir)
	if err != nil {
		return false
	}
	absPath = strings.ToLower(filepath.Clean(absPath))
	absRoot = strings.ToLower(filepath.Clean(absRoot))
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(absA), filepath.Clean(absB))
}

// parseListenerPIDs accepts both netstat's TCP and TCPv6 lines and does not
// depend on column positions beyond "local address", "LISTENING" and PID.
func parseListenerPIDs(output string, port int) []int {
	var pids []int
	seen := map[int]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if !strings.EqualFold(fields[0], "TCP") && !strings.EqualFold(fields[0], "TCPv6") {
			continue
		}
		colon := strings.LastIndex(fields[1], ":")
		if colon < 0 {
			continue
		}
		localPort, err := strconv.Atoi(fields[1][colon+1:])
		if err != nil || localPort != port {
			continue
		}
		listening := false
		for i := len(fields) - 2; i >= 2; i-- {
			if strings.EqualFold(fields[i], "LISTENING") {
				listening = true
				break
			}
		}
		if !listening {
			continue
		}
		pid, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids
}
