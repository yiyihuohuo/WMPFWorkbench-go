package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	diagnosticArchiveLimit = 64 << 20
	diagnosticLogTailLimit = 4 << 20
	diagnosticAgentLimit   = 12
)

type diagnosticEntry struct {
	Name      string `json:"name"`
	Source    string `json:"source,omitempty"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	data      []byte
	mod       time.Time
}

type diagnosticFileInfo struct {
	Name      string `json:"name"`
	Source    string `json:"source,omitempty"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type diagnosticManifest struct {
	AppVersion string               `json:"app_version"`
	ExportedAt string               `json:"exported_at"`
	Root       string               `json:"root"`
	SessionDir string               `json:"session_dir"`
	SessionID  string               `json:"session_id"`
	AppID      string               `json:"appid"`
	Reason     string               `json:"reason"`
	State      string               `json:"state"`
	FailedAt   string               `json:"failed_at"`
	Redaction  string               `json:"redaction"`
	Files      []diagnosticFileInfo `json:"files"`
	Missing    []string             `json:"missing_files,omitempty"`
}

// ExportDiagnostics packages the latest failed launch into a ZIP chosen by the
// user. It is exposed to the Wails frontend so operators can hand one archive
// to support instead of hunting for session directories and temp logs.
func (b *Backend) ExportDiagnostics() (string, error) {
	if b == nil || b.svc == nil {
		return "", fmt.Errorf("服务尚未初始化，暂时无法导出诊断日志")
	}
	root := b.svc.Paths.Root
	dir, failure, err := findLatestFailedSession(root)
	if err != nil {
		b.logErr("导出诊断日志", err, "")
		return "", err
	}
	sessionID := diagnosticString(failure, "session_id")
	if sessionID == "" {
		sessionID = filepath.Base(dir)
	}
	dest, err := runtime.SaveFileDialog(b.ctx, runtime.SaveDialogOptions{
		Title:                "导出诊断日志",
		DefaultFilename:      "wmpf-diagnostics-" + sessionID + ".zip",
		CanCreateDirectories: true,
		Filters: []runtime.FileFilter{
			{DisplayName: "ZIP 压缩包 (*.zip)", Pattern: "*.zip"},
		},
	})
	if err != nil || dest == "" {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(dest), ".zip") {
		dest += ".zip"
	}
	entries, _, err := collectDiagnosticEntries(root, dir, failure, b.Logs())
	if err != nil {
		b.logErr("导出诊断日志", err, sessionID)
		return "", err
	}
	if err := writeDiagnosticZip(dest, entries); err != nil {
		b.logErr("写入诊断日志", err, dest)
		return "", err
	}
	st, statErr := os.Stat(dest)
	if statErr == nil {
		b.logf("诊断日志已导出：%s（%d B，会话 %s）", dest, st.Size(), sessionID)
	} else {
		b.logf("诊断日志已导出：%s（会话 %s）", dest, sessionID)
	}
	return dest, nil
}

func findLatestFailedSession(root string) (string, map[string]any, error) {
	base := filepath.Join(root, "data", "desktop", "sessions")
	dirs, err := os.ReadDir(base)
	if err != nil {
		return "", nil, fmt.Errorf("未找到会话目录：%w", err)
	}
	bestDir := ""
	bestTime := time.Time{}
	var bestFailure map[string]any
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		path := diagnosticFailurePath(filepath.Join(base, d.Name()))
		st, statErr := os.Stat(path)
		if statErr != nil || !st.Mode().IsRegular() {
			continue
		}
		failure, readErr := readJSONMap(path)
		if readErr != nil {
			continue
		}
		// failed_at is authoritative: file mtime can be changed by copies,
		// antivirus scans or manual inspection and must not pick an older
		// session over the real latest failure.
		failedAt := diagnosticFailureTime(failure)
		rank := failedAt
		if rank.IsZero() {
			rank = st.ModTime()
		}
		if bestDir != "" && !rank.After(bestRank(bestFailure, bestTime)) {
			continue
		}
		bestDir = filepath.Join(base, d.Name())
		bestTime = st.ModTime()
		bestFailure = failure
	}
	if bestDir == "" {
		return "", nil, fmt.Errorf("未找到失败会话；请先复现一次启动失败后再导出")
	}
	return bestDir, bestFailure, nil
}

func readJSONMap(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func diagnosticString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// diagnosticFailurePath prefers the path actually written by hostruntime.
// The session-root location is accepted for older artifacts only.
func diagnosticFailurePath(dir string) string {
	preferred := filepath.Join(dir, "observation", "failure.json")
	if st, err := os.Stat(preferred); err == nil && st.Mode().IsRegular() {
		return preferred
	}
	return filepath.Join(dir, "failure.json")
}

func diagnosticFailureTime(failure map[string]any) time.Time {
	if text := diagnosticString(failure, "failed_at"); text != "" {
		if t, err := time.Parse(time.RFC3339, text); err == nil {
			return t
		}
	}
	return time.Time{}
}

func bestRank(failure map[string]any, mod time.Time) time.Time {
	if t := diagnosticFailureTime(failure); !t.IsZero() {
		return t
	}
	return mod
}

func diagnosticFailedAt(failure map[string]any, dir string) time.Time {
	if t := diagnosticFailureTime(failure); !t.IsZero() {
		return t
	}
	if st, err := os.Stat(diagnosticFailurePath(dir)); err == nil {
		return st.ModTime()
	}
	return time.Now()
}

func collectDiagnosticEntries(root, dir string, failure map[string]any, uiLogs []string) ([]diagnosticEntry, diagnosticManifest, error) {
	total := int64(0)
	entries := []diagnosticEntry{}
	temp := os.TempDir()
	missing := []string{}
	add := func(name, source string, limit int64) {
		data, truncated, err := readDiagnosticFile(source, limit)
		if err != nil {
			reason := redactDiagnosticText(err.Error(), root, temp)
			missing = append(missing, name+" <- "+redactDiagnosticText(source, root, temp)+"：读取失败："+reason)
			return
		}
		data = []byte(redactDiagnosticText(string(data), root, temp))
		if total+int64(len(data)) > diagnosticArchiveLimit {
			missing = append(missing, fmt.Sprintf("%s <- %s：超过 %d MiB 归档上限", name,
				redactDiagnosticText(source, root, temp), diagnosticArchiveLimit/(1<<20)))
			return
		}
		mod := time.Now()
		if st, statErr := os.Stat(source); statErr == nil {
			mod = st.ModTime()
		}
		entries = append(entries, diagnosticEntry{
			Name: name, Source: redactDiagnosticText(source, root, temp), Bytes: int64(len(data)),
			SHA256: sha256Hex(data), Truncated: truncated, data: data, mod: mod,
		})
		total += int64(len(data))
	}

	add("events.jsonl", filepath.Join(dir, "observation", "events.jsonl"), diagnosticArchiveLimit)
	add("worker.json", filepath.Join(dir, "worker.json"), diagnosticArchiveLimit)
	add("failure.json", diagnosticFailurePath(dir), diagnosticArchiveLimit)
	add("inputs/metadata.json", filepath.Join(dir, "inputs", "metadata.json"), diagnosticArchiveLimit)
	add("bridge.jsonl", filepath.Join(dir, "observation", "bridge.jsonl"), diagnosticArchiveLimit)

	failedAt := diagnosticFailedAt(failure, dir)
	start := failedAt.Add(-10 * time.Minute)
	end := failedAt.Add(2 * time.Minute)
	logs := []string{}
	for _, pattern := range []string{"wmpf-agent-*.log", "wmpf-devtools-frontend-*.log"} {
		matches, _ := filepath.Glob(filepath.Join(os.TempDir(), pattern))
		logs = append(logs, matches...)
	}
	type namedTime struct {
		path string
		when time.Time
	}
	ordered := make([]namedTime, 0, len(logs))
	for _, p := range logs {
		if st, err := os.Stat(p); err == nil {
			ordered = append(ordered, namedTime{path: p, when: st.ModTime()})
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].when.After(ordered[j].when) })
	count := 0
	for _, item := range ordered {
		if item.when.Before(start) || item.when.After(end) {
			continue
		}
		base := filepath.Base(item.path)
		folder := "agent"
		if strings.Contains(base, "devtools") {
			folder = "devtools"
		}
		before := len(entries)
		add(folder+"/"+base, item.path, diagnosticLogTailLimit)
		if len(entries) == before {
			continue
		}
		count++
		if count >= diagnosticAgentLimit {
			break
		}
	}

	if len(uiLogs) > 0 {
		data := []byte(redactDiagnosticText(strings.Join(uiLogs, "\n")+"\n", root, temp))
		if total+int64(len(data)) <= diagnosticArchiveLimit {
			entries = append(entries, diagnosticEntry{
				Name: "ui-log.txt", Bytes: int64(len(data)), SHA256: sha256Hex(data),
				data: data, mod: failedAt,
			})
			total += int64(len(data))
		}
	}

	if len(entries) == 0 {
		return nil, diagnosticManifest{}, fmt.Errorf("未找到可导出的诊断文件；该失败会话目录可能已被清理")
	}
	sessionID := diagnosticString(failure, "session_id")
	if sessionID == "" {
		sessionID = filepath.Base(dir)
	}
	missingText := ""
	if len(missing) > 0 {
		missingText = strings.Join(missing, "\n") + "\n"
	}
	files := make([]diagnosticFileInfo, 0, len(entries))
	for _, e := range entries {
		files = append(files, diagnosticFileInfo{
			Name: e.Name, Source: e.Source, Bytes: e.Bytes, SHA256: e.SHA256, Truncated: e.Truncated,
		})
	}
	manifest := diagnosticManifest{
		AppVersion: appVersion,
		ExportedAt: time.Now().Format(time.RFC3339),
		Root:       redactDiagnosticText(root, root, temp),
		SessionDir: redactDiagnosticText(dir, root, temp),
		SessionID:  sessionID,
		AppID:      diagnosticString(failure, "appid"),
		Reason:     redactDiagnosticText(diagnosticString(failure, "reason"), root, temp),
		State:      diagnosticString(failure, "state"),
		FailedAt:   diagnosticString(failure, "failed_at"),
		Redaction:  "uin/openid/token/cookie/本地路径已脱敏",
		Files:      files,
		Missing:    missing,
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, diagnosticManifest{}, err
	}
	manifestData = append(manifestData, '\n')
	readme := fmt.Sprintf(`WMPF 工作台诊断日志

会话：%s
AppID：%s
失败原因：%s
失败时间：%s

文件说明：
- events.jsonl：原生事件流。重点看 worker.started 后 10 条事件、3 次 host.Reset 的时间戳，以及 script.error / host.spawn_error / ilink.ThirdAppLogin。
- worker.json：worker 启动时使用的 host、inputs、runtime 路径。
- failure.json：工作台记录的失败状态。
- agent/*.log：native agent 的 hook 与注入日志。
- devtools/*.log：DevTools 前端进程日志（若当时存在）。
- ui-log.txt：工作台界面日志。
- manifest.json：文件清单、大小与 SHA256；missing_files 记录本次未找到的文件。
- missing.txt：存在缺失文件时列出缺失原因；缺失不影响其余文件导出。

脱敏：导出时已移除 uin、openid、token、cookie 等账户标识/凭据，并把本机路径替换为 <APP_ROOT> / <TEMP> / <USER>；事件名、错误码和时间戳保留。
`, sessionID, manifest.AppID, manifest.Reason, manifest.FailedAt)
	now := time.Now()
	prefix := []diagnosticEntry{
		{Name: "README.txt", Bytes: int64(len(readme)), SHA256: sha256Hex([]byte(readme)), data: []byte(readme), mod: now},
		{Name: "manifest.json", Bytes: int64(len(manifestData)), SHA256: sha256Hex(manifestData), data: manifestData, mod: now},
	}
	if missingText != "" {
		prefix = append(prefix, diagnosticEntry{
			Name: "missing.txt", Bytes: int64(len(missingText)), SHA256: sha256Hex([]byte(missingText)),
			data: []byte(missingText), mod: now,
		})
	}
	entries = append(prefix, entries...)
	return entries, manifest, nil
}

func readDiagnosticFile(path string, limit int64) ([]byte, bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() {
		return nil, false, fmt.Errorf("不是普通文件")
	}
	if limit <= 0 || st.Size() <= limit {
		data, err := os.ReadFile(path)
		return data, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data := make([]byte, limit)
	n, err := f.ReadAt(data, st.Size()-limit)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	return data[:n], true, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeDiagnosticZip(dest string, entries []diagnosticEntry) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.Name, Method: zip.Deflate, Modified: e.mod}
		w, createErr := zw.CreateHeader(header)
		if createErr != nil {
			f.Close()
			return createErr
		}
		if _, writeErr := w.Write(e.data); writeErr != nil {
			f.Close()
			return writeErr
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
