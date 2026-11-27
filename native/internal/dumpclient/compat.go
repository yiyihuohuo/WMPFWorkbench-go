package dumpclient

import (
	"context"
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
	PID  int
	Path string
}

var (
	inspectListener = queryListenerProcess
	killPID         = func(pid int) error {
		p, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		return p.Kill()
	}
)

// supportsKeepAlive distinguishes the current sidecar from older builds where
// the dynamic /account/{openid} DELETE route makes the same path answer 405.
func (c *Client) supportsKeepAlive(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, c.opts.BaseURL+keepAlivePath, nil)
	if err != nil {
		return false
	}
	cli := *c.http
	if cli.Timeout == 0 || cli.Timeout > 3*time.Second {
		cli.Timeout = 3 * time.Second
	}
	resp, err := cli.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true
	}
	for _, method := range strings.Split(resp.Header.Get("Allow"), ",") {
		if strings.EqualFold(strings.TrimSpace(method), http.MethodPost) {
			return true
		}
	}
	return false
}

func (c *Client) incompatibleSidecarError() error {
	port := c.port()
	info, err := inspectListener(port)
	if err != nil {
		return fmt.Errorf("检测到旧版 wxapkg-dump：它不支持账号保活接口 POST %s，且无法读取占用端口 %d 的进程信息（%v）。请更新到最新 1.0.5 并重启工作台。", keepAlivePath, port, err)
	}
	return fmt.Errorf("检测到旧版 wxapkg-dump 正在占用端口 %d（PID %d，路径 %s）。该版本不支持账号保活接口 POST %s。请更新到最新 1.0.5 并重启工作台。", port, info.PID, displayPath(info.Path), keepAlivePath)
}

func (c *Client) replaceIncompatibleSidecar(ctx context.Context) error {
	port := c.port()
	info, err := inspectListener(port)
	if err != nil {
		return fmt.Errorf("检测到旧版 wxapkg-dump，但无法确认占用端口 %d 的进程：%w。为避免误杀，未自动终止；请更新到最新 1.0.5 并重启工作台。", port, err)
	}
	if !isManagedSidecarPath(info.Path, c.opts.ManagedDir) {
		return fmt.Errorf("检测到旧版 wxapkg-dump（PID %d，路径 %s），但该路径不在工作台数据目录 %s 内。为避免误杀，未自动终止；请手动结束该进程后重启工作台。", info.PID, displayPath(info.Path), displayPath(c.opts.ManagedDir))
	}
	if samePath(info.Path, c.opts.ExePath) {
		return fmt.Errorf("检测到旧版 wxapkg-dump（PID %d，路径 %s），但工作台当前要启动的文件就是它：内置新版未成功释放，未自动终止。请重新下载最新 1.0.5 完整包并覆盖安装。", info.PID, displayPath(info.Path))
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
	if !strings.EqualFold(filepath.Base(path), exeName) {
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

func parseListenerPIDs(output string, port int) []int {
	suffix := ":" + strconv.Itoa(port)
	var pids []int
	seen := map[int]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[len(fields)-2], "LISTENING") || !strings.HasSuffix(fields[1], suffix) {
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
