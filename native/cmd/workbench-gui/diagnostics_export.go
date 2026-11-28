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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	diagnosticArchiveLimit    = 64 << 20
	diagnosticLogTailLimit    = 4 << 20
	diagnosticAgentLimit      = 12
	diagnosticAgentBefore     = 60 * time.Minute
	diagnosticAgentAfter      = 15 * time.Minute
	diagnosticHashPlaceholder = "0000000000000000000000000000000000000000000000000000000000000000"
)

// ExportDiagnostics is bound to Wails and can be invoked from more than one
// frontend call at once. b.mu is already used by Logs()/logf(), so a dedicated
// lock avoids both overlapping exports and re-entrant locking of b.mu.
var diagnosticExportMu sync.Mutex

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
	var dest string
	var err error
	withDiagnosticExportLock(func() {
		dest, err = b.exportDiagnostics()
	})
	return dest, err
}

func withDiagnosticExportLock(run func()) {
	diagnosticExportMu.Lock()
	defer diagnosticExportMu.Unlock()
	run()
}

// exportDiagnostics must be called with diagnosticExportMu held.
func (b *Backend) exportDiagnostics() (string, error) {
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

type diagnosticSessionRank struct {
	failedAt time.Time
	mod      time.Time
	session  string
}

func diagnosticSessionRankOf(dir string, failure map[string]any, mod time.Time) diagnosticSessionRank {
	session := diagnosticString(failure, "session_id")
	if session == "" {
		session = filepath.Base(dir)
	}
	return diagnosticSessionRank{failedAt: diagnosticFailureTime(failure), mod: mod, session: session}
}

// diagnosticRankNewer prefers the authoritative failed_at timestamp; ties
// within the same second fall back to mtime, then to session ID so selection is
// deterministic even for copied sessions with identical timestamps.
func diagnosticRankNewer(candidate, best diagnosticSessionRank) bool {
	if candidate.failedAt.IsZero() != best.failedAt.IsZero() {
		return !candidate.failedAt.IsZero()
	}
	if !candidate.failedAt.IsZero() &&
		!candidate.failedAt.Truncate(time.Second).Equal(best.failedAt.Truncate(time.Second)) {
		return candidate.failedAt.After(best.failedAt)
	}
	if !candidate.mod.Equal(best.mod) {
		return candidate.mod.After(best.mod)
	}
	return candidate.session > best.session
}

func findLatestFailedSession(root string) (string, map[string]any, error) {
	base := filepath.Join(root, "data", "desktop", "sessions")
	dirs, err := os.ReadDir(base)
	if err != nil {
		return "", nil, fmt.Errorf("未找到会话目录：%w", err)
	}
	bestDir := ""
	var bestFailure map[string]any
	var bestRank diagnosticSessionRank
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
		rank := diagnosticSessionRankOf(filepath.Join(base, d.Name()), failure, st.ModTime())
		if bestDir != "" && !diagnosticRankNewer(rank, bestRank) {
			continue
		}
		bestDir = filepath.Join(base, d.Name())
		bestFailure = failure
		bestRank = rank
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

// diagnosticObservationPath prefers the path written by hostruntime. The
// session-root location is accepted for older or manually copied artifacts.
func diagnosticObservationPath(dir, name string) string {
	preferred := filepath.Join(dir, "observation", name)
	if st, err := os.Stat(preferred); err == nil && st.Mode().IsRegular() {
		return preferred
	}
	return filepath.Join(dir, name)
}

func diagnosticFailurePath(dir string) string {
	return diagnosticObservationPath(dir, "failure.json")
}

var diagnosticFailureTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006/01/02 15:04:05",
	"2006/01/02 15:04",
}

func diagnosticFailureTime(failure map[string]any) time.Time {
	if failure == nil {
		return time.Time{}
	}
	raw, ok := failure["failed_at"]
	if !ok {
		return time.Time{}
	}
	switch value := raw.(type) {
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return time.Time{}
		}
		for _, layout := range diagnosticFailureTimeLayouts {
			if t, err := time.Parse(layout, text); err == nil {
				return t
			}
		}
		if seconds, err := strconv.ParseFloat(text, 64); err == nil {
			return diagnosticUnixFailureTime(seconds)
		}
	case float64:
		return diagnosticUnixFailureTime(value)
	case json.Number:
		if seconds, err := value.Float64(); err == nil {
			return diagnosticUnixFailureTime(seconds)
		}
	}
	return time.Time{}
}

func diagnosticUnixFailureTime(seconds float64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	if seconds > 1e11 { // millisecond timestamps
		seconds /= 1000
	}
	whole := int64(seconds)
	nanos := int64((seconds - float64(whole)) * float64(time.Second))
	return time.Unix(whole, nanos).UTC()
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

	add("events.jsonl", diagnosticObservationPath(dir, "events.jsonl"), diagnosticArchiveLimit)
	add("worker.json", filepath.Join(dir, "worker.json"), diagnosticArchiveLimit)
	add("failure.json", diagnosticFailurePath(dir), diagnosticArchiveLimit)
	add("inputs/metadata.json", filepath.Join(dir, "inputs", "metadata.json"), diagnosticArchiveLimit)
	add("bridge.jsonl", diagnosticObservationPath(dir, "bridge.jsonl"), diagnosticArchiveLimit)
	add("attempt-2/worker.json", filepath.Join(dir, "attempt-2", "worker.json"), diagnosticArchiveLimit)
	add("attempt-2/inputs/metadata.json", filepath.Join(dir, "attempt-2", "inputs", "metadata.json"), diagnosticArchiveLimit)

	failedAt := diagnosticFailedAt(failure, dir)
	start := failedAt.Add(-diagnosticAgentBefore)
	end := failedAt.Add(diagnosticAgentAfter)
	seenPath := map[string]bool{}
	logs := []string{}
	for _, pattern := range []string{"wmpf-agent-*.log", "wmpf-devtools-frontend-*.log"} {
		matches, _ := filepath.Glob(filepath.Join(os.TempDir(), pattern))
		for _, p := range matches {
			key := strings.ToLower(filepath.Clean(p))
			if seenPath[key] {
				continue
			}
			seenPath[key] = true
			logs = append(logs, p)
		}
	}
	type namedTime struct {
		path string
		when time.Time
		info os.FileInfo
	}
	ordered := make([]namedTime, 0, len(logs))
	for _, p := range logs {
		if st, err := os.Stat(p); err == nil {
			ordered = append(ordered, namedTime{path: p, when: st.ModTime(), info: st})
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].when.After(ordered[j].when) })
	count := 0
	kept := make([]os.FileInfo, 0, len(ordered))
	for _, item := range ordered {
		duplicate := false
		for _, previous := range kept {
			if os.SameFile(previous, item.info) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		kept = append(kept, item.info)
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
		budget := diagnosticArchiveLimit - total
		if budget > diagnosticLogTailLimit {
			budget = diagnosticLogTailLimit
		}
		if budget < 0 {
			budget = 0
		}
		truncated := false
		if int64(len(data)) > budget {
			if budget == 0 {
				missing = append(missing, "ui-log.txt：界面日志超过 64 MiB 归档上限，未写入")
				data = nil
			} else {
				data = data[int64(len(data))-budget:]
				truncated = true
				missing = append(missing, fmt.Sprintf("ui-log.txt：界面日志超过 %d MiB 限制，仅保留末尾 %d B",
					diagnosticLogTailLimit/(1<<20), budget))
			}
		}
		if len(data) > 0 {
			entries = append(entries, diagnosticEntry{
				Name: "ui-log.txt", Bytes: int64(len(data)), SHA256: sha256Hex(data),
				Truncated: truncated, data: data, mod: failedAt,
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
	files := make([]diagnosticFileInfo, 0, len(entries)+3)
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
		Redaction:  "uin/openid/unionid/sessionid/token/sign/cookie/本地路径已脱敏",
		Files:      files,
		Missing:    missing,
	}
	now := time.Now()
	readme := fmt.Sprintf(`WMPF 工作台诊断日志

会话：%s
AppID：%s
失败原因：%s
失败时间：%s

文件说明：
- README.txt：本说明。
- manifest.json：文件清单、大小与 SHA256；files 覆盖归档内全部文件（含 manifest.json 自身）。
- missing.txt：缺失/超限文件及原因；存在缺失文件时才写入。
- events.jsonl：原生事件流。重点看 worker.started 后 10 条事件、3 次 host.Reset 的时间戳，以及 script.error / host.spawn_error / ilink.ThirdAppLogin。
- worker.json：worker 启动时使用的 host、inputs、runtime 路径。
- failure.json：工作台记录的失败状态。
- inputs/metadata.json：本次启动的输入元数据。
- bridge.jsonl：二跳调试桥事件流。
- attempt-2/worker.json：二跳目标启动时的 worker 配置（若存在）。
- attempt-2/inputs/metadata.json：二跳目标启动的输入元数据（若存在）。
- agent/*.log：native agent 的 hook 与注入日志；devtools/*.log：DevTools 前端进程日志（若当时存在）。
- ui-log.txt：工作台界面日志；超过 4 MiB 时只保留末尾并记入 missing.txt。

缺失容忍：单个文件缺失或读取失败会写入 missing_files / missing.txt，不影响其余文件导出。
大小限制：整个归档最多 64 MiB；单个 agent / UI 日志最多保留末尾 4 MiB；agent 日志最多 12 个。
`, sessionID, manifest.AppID, manifest.Reason, manifest.FailedAt)
	prefix := []diagnosticEntry{
		{Name: "README.txt", Bytes: int64(len(readme)), SHA256: sha256Hex([]byte(readme)), data: []byte(readme), mod: now},
		{Name: "manifest.json", Bytes: 0, SHA256: diagnosticHashPlaceholder, mod: now},
	}
	if missingText != "" {
		prefix = append(prefix, diagnosticEntry{
			Name: "missing.txt", Bytes: int64(len(missingText)), SHA256: sha256Hex([]byte(missingText)),
			data: []byte(missingText), mod: now,
		})
	}
	for _, e := range prefix {
		manifest.Files = append(manifest.Files, diagnosticFileInfo{
			Name: e.Name, Source: e.Source, Bytes: e.Bytes, SHA256: e.SHA256, Truncated: e.Truncated,
		})
	}
	// The manifest lists its own byte size as well. Re-render until the
	// recorded size equals the rendered length (digit-width boundary case).
	var manifestData []byte
	var err error
	stable := false
	for attempt := 0; attempt < 8; attempt++ {
		manifestData, err = json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return nil, diagnosticManifest{}, err
		}
		manifestData = append(manifestData, '\n')
		stable = true
		for i := range manifest.Files {
			if manifest.Files[i].Name == "manifest.json" && manifest.Files[i].Bytes != int64(len(manifestData)) {
				manifest.Files[i].Bytes = int64(len(manifestData))
				stable = false
			}
		}
		if stable {
			break
		}
	}
	if !stable {
		return nil, diagnosticManifest{}, fmt.Errorf("manifest.json 文件大小无法稳定")
	}
	// The manifest must list its own hash. Canonical serialization uses a
	// fixed-length placeholder; its digest is the self-hash. The final pass
	// patches exactly one field with that digest, so verifiers canonicalize
	// the field back to the placeholder before hashing.
	manifestHash := sha256Hex(manifestData)
	placeholderField := `"sha256": "` + diagnosticHashPlaceholder + `"`
	actualField := `"sha256": "` + manifestHash + `"`
	if strings.Count(string(manifestData), placeholderField) != 1 {
		return nil, diagnosticManifest{}, fmt.Errorf("manifest.json 哈希占位符数量异常")
	}
	manifestData = []byte(strings.Replace(string(manifestData), placeholderField, actualField, 1))
	for i := range prefix {
		if prefix[i].Name == "manifest.json" {
			prefix[i].Bytes = int64(len(manifestData))
			prefix[i].SHA256 = manifestHash
			prefix[i].data = manifestData
		}
	}
	for i := range manifest.Files {
		if manifest.Files[i].Name == "manifest.json" {
			manifest.Files[i].Bytes = int64(len(manifestData))
			manifest.Files[i].SHA256 = manifestHash
		}
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
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wmpf-diagnostics-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()
	zw := zip.NewWriter(tmp)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.Name, Method: zip.Deflate, Modified: e.mod}
		w, createErr := zw.CreateHeader(header)
		if createErr != nil {
			return createErr
		}
		if _, writeErr := w.Write(e.data); writeErr != nil {
			return writeErr
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return err
	}
	committed = true
	return nil
}
