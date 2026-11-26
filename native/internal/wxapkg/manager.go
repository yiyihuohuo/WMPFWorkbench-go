// Package wxapkg manages bounded, persistent offline-only worker jobs.
package wxapkg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"wmpfworkbench/internal/app"
	"wmpfworkbench/internal/config"
	"wmpfworkbench/internal/process"
)

type Options struct {
	AppID, OutDir, Version string
	Source                 string
	Restore, Pretty        bool
	Timeout                int
}
type task struct {
	mu   sync.Mutex
	data map[string]any
	dir  string
}
type Manager struct {
	Paths   app.Paths
	Tool    string
	mu      sync.Mutex
	tasks   map[string]*task
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
	command func(context.Context, []string) *exec.Cmd
}

var taskID = regexp.MustCompile(`^[0-9a-f]{12}$`)
var safeSource = regexp.MustCompile(`[^0-9A-Za-z_.-]`)

// Only terminal tasks beyond this many are pruned; active/interrupted tasks
// are never deleted automatically.
const maxRetainedTasks = 20

func New(p app.Paths, tool string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{Paths: p, Tool: tool, tasks: map[string]*task{}, ctx: ctx, cancel: cancel}
	m.command = func(ctx context.Context, args []string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, m.Tool, args...)
		process.Hide(cmd)
		return cmd
	}
	m.prune()
	return m
}
func (m *Manager) stateDir() string { return filepath.Join(m.Paths.Data, "desktop", "decrypt") }

func (m *Manager) prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
}

// pruneLocked keeps the newest maxRetainedTasks terminal jobs and removes older
// terminal directories. Non-terminal, active or missing tasks are never touched.
func (m *Manager) pruneLocked() {
	ids := m.ids()
	if len(ids) <= maxRetainedTasks {
		return
	}
	for _, id := range ids[maxRetainedTasks:] {
		if _, active := m.tasks[id]; active {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(m.stateDir(), id, "status.json"))
		if err != nil {
			continue
		}
		var d map[string]any
		if json.Unmarshal(raw, &d) != nil || !terminal(fmt.Sprint(d["state"])) {
			continue
		}
		os.RemoveAll(filepath.Join(m.stateDir(), id))
	}
}

// orphanLockFree checks the same cross-process worker lock the runner holds.
// If it is free while a task is still non-terminal, no process can be running
// that task: it was left behind by a restart or crash.
func (m *Manager) orphanLockFree() bool {
	os.MkdirAll(m.stateDir(), 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	release, err := config.AcquireLock(ctx, filepath.Join(m.stateDir(), ".worker.lock"))
	if err != nil {
		return false
	}
	release()
	return true
}
func terminal(s string) bool {
	return slices.Contains([]string{"succeeded", "failed", "cancelled", "blocked"}, s)
}
func blocked(reason string) map[string]any {
	return map[string]any{"ok": false, "grade": "blocked", "reason": reason}
}
func clone(data map[string]any) map[string]any {
	b, _ := json.Marshal(data)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}
func seconds() float64 { return float64(time.Now().UnixMilli()) / 1000 }
func num(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}
func (t *task) change(fn func(map[string]any)) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(t.data)
	return app.WriteJSON(filepath.Join(t.dir, "status.json"), t.data)
}
func (t *task) snapshot() map[string]any { t.mu.Lock(); defer t.mu.Unlock(); return clone(t.data) }
func (t *task) event(ev map[string]any) {
	b, _ := json.Marshal(ev)
	f, err := os.OpenFile(filepath.Join(t.dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		f.Write(append(b, '\n'))
		f.Close()
	}
}
func (m *Manager) Close() { m.mu.Lock(); m.closed = true; m.cancel(); m.mu.Unlock(); m.wg.Wait() }

func resolved(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parts := []string{}
	cur := path
	for {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			for i := len(parts) - 1; i >= 0; i-- {
				real = filepath.Join(real, parts[i])
			}
			return filepath.Clean(real), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", errors.New("output root not found")
		}
		parts = append(parts, filepath.Base(cur))
		cur = parent
	}
}
func within(base, path string) bool {
	rel, err := filepath.Rel(strings.ToLower(base), strings.ToLower(path))
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
func (m *Manager) output(o Options, version string) (string, error) {
	path := o.OutDir
	if path == "" {
		path = filepath.Join(m.Paths.Out, "wxapkg", o.AppID, version)
	}
	if strings.HasPrefix(path, "~"+string(os.PathSeparator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	path, err := resolved(path)
	if err != nil {
		return "", err
	}
	root, err := resolved(m.Paths.Root)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(path, root) {
		return "", errors.New("out_dir 不能是项目根目录")
	}
	protected := []string{"runtime", "data", "vendor", "server", "desktop", "packages", "bridge", "services", "tools", "tests", "docs", "native", "host", "hostcpp", "config", ".git"}
	for _, sub := range protected {
		guard, err := resolved(filepath.Join(root, sub))
		if err != nil {
			return "", err
		}
		if within(guard, path) || within(path, guard) {
			return "", fmt.Errorf("out_dir 不能覆盖受保护目录: %s", sub)
		}
	}
	for _, r := range m.Paths.Roots {
		guard, err := resolved(r.Path)
		if err != nil {
			return "", err
		}
		if within(guard, path) || within(path, guard) {
			return "", errors.New("out_dir 不能覆盖运行时用户目录")
		}
	}
	return path, nil
}
func (m *Manager) Start(o Options) map[string]any {
	if !app.AppID.MatchString(o.AppID) {
		return blocked("appid 必须是 wx+16位小写十六进制（解密密钥由 appid 派生）")
	}
	if o.Timeout < 30 || o.Timeout > 900 {
		return blocked("timeout 应在 30–900 秒")
	}
	if st, err := os.Stat(m.Tool); err != nil || !st.Mode().IsRegular() {
		return blocked("解密工具未编译")
	}
	rows, err := app.ListPackages(m.ctx, m.Paths, o.AppID)
	if err != nil {
		return blocked("读取本地包失败")
	}
	sourceMode := strings.ToLower(strings.TrimSpace(o.Source))
	if sourceMode == "" {
		sourceMode = "auto"
	}
	switch sourceMode {
	case "auto":
	case "runtime_profile":
		// app.ListPackages currently only scans runtime-profile applet
		// directories, so every row is a runtime-profile package.
	default:
		return blocked("source 仅支持 auto 或 runtime_profile")
	}
	versions := []string{}
	for _, r := range rows {
		if r.Encrypted && !slices.Contains(versions, r.Version) {
			versions = append(versions, r.Version)
		}
	}
	if len(versions) == 0 {
		return blocked("本机没有该 appid 的 V1MMWX 加密包")
	}
	sort.Slice(versions, func(i, j int) bool {
		a, e1 := strconv.ParseInt(versions[i], 10, 64)
		b, e2 := strconv.ParseInt(versions[j], 10, 64)
		if e1 != nil {
			a = -1
		}
		if e2 != nil {
			b = -1
		}
		if a != b {
			return a < b
		}
		return versions[i] < versions[j]
	})
	ver := o.Version
	if ver == "" {
		ver = versions[len(versions)-1]
	}
	if !slices.Contains(versions, ver) {
		r := blocked("指定版本不存在")
		r["versions"] = versions
		return r
	}
	out, err := m.output(o, ver)
	if err != nil {
		return blocked(err.Error())
	}
	selected := []app.Package{}
	packageSources := []string{}
	for _, r := range rows {
		if r.Encrypted && r.Version == ver {
			selected = append(selected, r)
			if !slices.Contains(packageSources, r.Source) {
				packageSources = append(packageSources, r.Source)
			}
		}
	}
	notes := []string{"仅处理本机已有的加密包副本；未触碰运行中的进程", "解密产物校验 wxapkg 头且 worker 必须正常退出", "还原产物为运行时编译后代码，不等同官方源码工程"}
	for _, r := range selected {
		if o.Pretty && r.Bytes > 2<<20 {
			o.Pretty = false
			notes = append(notes, "已自动关闭 pretty：存在大于 2MB 的脚本")
		}
	}
	var ident [6]byte
	if _, err = rand.Read(ident[:]); err != nil {
		return blocked("无法创建任务标识")
	}
	id := hex.EncodeToString(ident[:])
	dir := filepath.Join(m.stateDir(), id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return blocked("任务服务已关闭")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return blocked("无法创建任务目录")
	}
	t := &task{dir: dir, data: map[string]any{"task_id": id, "state": "queued", "phase": "preparing", "appid": o.AppID, "version": ver, "source_mode": sourceMode, "package_sources": packageSources, "out_dir": out, "created_at": seconds(), "started_at": nil, "finished_at": nil, "done_packages": 0, "total_packages": len(selected), "current_file": nil, "current_file_bytes": nil, "restored_files": 0, "total_bytes": 0, "decrypted_packages": []any{}, "invalid": []any{}, "sources": []any{}, "grade": "probable", "ok": nil, "reason": nil, "pretty": o.Pretty, "restore": o.Restore, "timeout": o.Timeout, "log_tail": []string{}, "notes": notes, "pid": nil, "cancel": false}}
	if err = t.change(func(map[string]any) {}); err != nil {
		return blocked("无法持久化任务状态")
	}
	m.tasks[id] = t
	m.pruneLocked()
	t.event(map[string]any{"stage": "task_created", "appid": o.AppID})
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.run(t, o, selected, out, id) }()
	return map[string]any{"grade": "confirmed", "ok": true, "task_id": id, "state": "queued", "appid": o.AppID, "version": ver, "source_mode": sourceMode, "package_sources": packageSources, "out_dir": out, "total_packages": len(selected), "pretty": o.Pretty, "notes": notes, "hint": "用 action=status 查询进度"}
}
func (m *Manager) get(id string) (map[string]any, string) {
	if id == "" {
		ids := m.ids()
		if len(ids) > 0 {
			id = ids[0]
		}
	}
	if !taskID.MatchString(id) {
		return nil, id
	}
	m.mu.Lock()
	t := m.tasks[id]
	m.mu.Unlock()
	if t != nil {
		return t.snapshot(), id
	}
	raw, err := os.ReadFile(filepath.Join(m.stateDir(), id, "status.json"))
	if err != nil {
		return nil, id
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return nil, id
	}
	return data, id
}
func (m *Manager) ids() []string {
	entries, _ := os.ReadDir(m.stateDir())
	type row struct {
		id   string
		when time.Time
	}
	rows := []row{}
	for _, e := range entries {
		if e.IsDir() && taskID.MatchString(e.Name()) {
			if st, err := os.Stat(filepath.Join(m.stateDir(), e.Name(), "status.json")); err == nil {
				rows = append(rows, row{e.Name(), st.ModTime()})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].when.After(rows[j].when) })
	out := []string{}
	for _, r := range rows {
		out = append(out, r.id)
	}
	return out
}
func (m *Manager) Status(id string) map[string]any {
	d, resolvedID := m.get(id)
	if d == nil {
		return blocked("任务不存在或还没有解密任务")
	}
	end := seconds()
	if n := num(d["finished_at"]); n > 0 {
		end = n
	}
	m.mu.Lock()
	_, active := m.tasks[resolvedID]
	m.mu.Unlock()
	interrupted := !terminal(fmt.Sprint(d["state"])) && !active && m.orphanLockFree()
	d["interrupted"] = interrupted
	if interrupted {
		d["state_note"] = "任务进程已不存在（服务重启或崩溃遗留），请重新发起"
	}
	d["elapsed_s"] = math.Round((end-num(d["created_at"]))*10) / 10
	d["eta_seconds"], d["eta_grade"], d["eta_basis"] = nil, nil, nil
	if !terminal(fmt.Sprint(d["state"])) && !interrupted {
		done, total, started := num(d["done_packages"]), num(d["total_packages"]), num(d["started_at"])
		basis := "样本不足（尚无包完成），不估算"
		if d["phase"] == "restore" || d["phase"] == "done" {
			basis = "已进入还原阶段，无法按包数外推"
		} else if done > 0 && started > 0 {
			d["eta_seconds"] = math.Round((seconds()-started)/done*math.Max(0, total-done)*10) / 10
			d["eta_grade"] = "candidate"
			basis = "按已完成包的平均耗时外推"
		}
		d["eta_basis"] = basis
	}
	delete(d, "cancel")
	delete(d, "timeout")
	return d
}
func (m *Manager) List(limit int) map[string]any {
	ids := m.ids()
	rows := []any{}
	for _, id := range ids[:min(len(ids), max(1, min(limit, 50)))] {
		d := m.Status(id)
		for _, key := range []string{"log_tail", "decrypted_packages", "invalid", "sources", "notes"} {
			delete(d, key)
		}
		rows = append(rows, d)
	}
	return map[string]any{"grade": "confirmed", "ok": true, "count": len(rows), "tasks": rows}
}
func (m *Manager) Cancel(id string) map[string]any {
	d, id := m.get(id)
	if d == nil {
		return blocked("任务不存在或还没有解密任务")
	}
	if terminal(fmt.Sprint(d["state"])) {
		return map[string]any{"grade": "confirmed", "ok": true, "task_id": id, "state": d["state"], "cancel_requested": false, "note": "任务已经结束，无需取消"}
	}
	if err := os.WriteFile(filepath.Join(m.stateDir(), id, "cancel"), nil, 0600); err != nil {
		return blocked("无法写入取消标记")
	}
	return map[string]any{"grade": "confirmed", "ok": true, "task_id": id, "state": "cancel_requested", "cancel_requested": true}
}
func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 128<<10)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := in.Read(buf)
		if n > 0 {
			if _, e := out.Write(buf[:n]); e != nil {
				return e
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
func (m *Manager) run(t *task, o Options, rows []app.Package, out, id string) {
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(t.dir, "cancel")); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	state, reason := "failed", "离线任务未完成"
	defer func() {
		if ctx.Err() != nil {
			state = "cancelled"
			reason = "已取消"
		}
		t.change(func(d map[string]any) {
			d["state"] = state
			d["phase"] = "done"
			d["finished_at"] = seconds()
			d["pid"] = nil
			d["ok"] = state == "succeeded"
			d["grade"] = "blocked"
			d["reason"] = reason
			if state == "succeeded" {
				d["grade"] = "confirmed"
				d["reason"] = nil
			}
		})
		t.event(map[string]any{"stage": "task_final", "state": state})
	}()
	unlock, err := config.AcquireLock(ctx, filepath.Join(m.stateDir(), ".worker.lock"))
	if err != nil {
		return
	}
	defer unlock()
	runCtx, stop := context.WithTimeout(ctx, time.Duration(o.Timeout)*time.Second)
	defer stop()
	t.change(func(d map[string]any) { d["state"] = "running"; d["started_at"] = seconds() })
	groups := map[string][]app.Package{}
	sources := []string{}
	for _, r := range rows {
		if groups[r.Source] == nil {
			sources = append(sources, r.Source)
		}
		groups[r.Source] = append(groups[r.Source], r)
	}
	slices.Sort(sources)
	for _, source := range sources {
		if runCtx.Err() != nil {
			reason = "任务超时或已取消"
			return
		}
		items := groups[source]
		dest := filepath.Join(out, safeSource.ReplaceAllString(source, "_")+"-"+id)
		if err = os.MkdirAll(dest, 0700); err != nil {
			reason = "无法创建输出目录"
			return
		}
		input, err := os.MkdirTemp(dest, "_inputs-")
		if err != nil {
			reason = "无法创建输入副本目录"
			return
		}
		err = func() error {
			defer os.RemoveAll(input)
			for _, r := range items {
				if err := copyFile(runCtx, r.Path, filepath.Join(input, r.File)); err != nil {
					return err
				}
			}
			args := []string{"-id=" + o.AppID, "-in=" + input, "-out=" + dest, "-save"}
			if o.Restore {
				args = append(args, "-restore")
			}
			if o.Pretty {
				args = append(args, "-pretty")
			}
			cmd := m.command(runCtx, args)
			cmd.Dir = filepath.Dir(m.Tool)
			cmd.Env = append(cmd.Environ(), "KILLWXAPKG_PROGRESS=1")
			cmd.WaitDelay = 2 * time.Second
			hideWorker(cmd)
			baseDone := int(num(t.snapshot()["done_packages"]))
			writer := &progressWriter{t: t, baseDone: baseDone}
			cmd.Stdout = writer
			cmd.Stderr = writer
			if err := cmd.Start(); err != nil {
				return err
			}
			t.change(func(d map[string]any) { d["pid"] = cmd.Process.Pid })
			waitErr := cmd.Wait()
			writer.flush()
			t.change(func(d map[string]any) { d["pid"] = nil })
			decrypted, invalid := []any{}, []any{}
			for _, item := range items {
				path := filepath.Join(dest, item.File)
				head, err := app.Head(path, 14)
				if err != nil {
					invalid = append(invalid, map[string]any{"file": item.File, "reason": "未产出包"})
					continue
				}
				st, _ := os.Stat(path)
				magic := strings.ToUpper(hex.EncodeToString(head[:min(len(head), 8)]))
				if len(head) >= 14 && head[0] == 0xbe && head[13] == 0xed {
					decrypted = append(decrypted, map[string]any{"file": item.File, "bytes": st.Size(), "magic": magic})
				} else {
					invalid = append(invalid, map[string]any{"file": item.File, "magic": magic})
				}
			}
			count, total := 0, int64(0)
			filepath.WalkDir(dest, func(path string, e fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if path == input {
					return filepath.SkipDir
				}
				if e.Type().IsRegular() && !strings.HasSuffix(e.Name(), ".wxapkg") {
					if st, err := e.Info(); err == nil {
						count++
						total += st.Size()
					}
				}
				return nil
			})
			exit := 0
			if waitErr != nil {
				exit = -1
				if cmd.ProcessState != nil {
					exit = cmd.ProcessState.ExitCode()
				}
			}
			entry := map[string]any{"source": source, "out_dir": dest, "packages": len(items), "ok": waitErr == nil && len(decrypted) == len(items) && len(invalid) == 0, "decrypted": decrypted, "invalid": invalid, "exit_code": exit, "restored_files": count, "total_bytes": total}
			t.change(func(d map[string]any) {
				appendRows := func(key string, v []any) { old := d[key].([]any); d[key] = append(old, v...) }
				appendRows("sources", []any{entry})
				appendRows("decrypted_packages", decrypted)
				appendRows("invalid", invalid)
				d["restored_files"] = num(d["restored_files"]) + float64(count)
				d["total_bytes"] = num(d["total_bytes"]) + float64(total)
			})
			if waitErr != nil {
				return waitErr
			}
			if len(invalid) > 0 || len(decrypted) != len(items) {
				return errors.New("解密产物未通过 wxapkg 头校验")
			}
			return nil
		}()
		if err != nil {
			if runCtx.Err() == context.DeadlineExceeded {
				reason = fmt.Sprintf("超时（%ds）", o.Timeout)
			} else {
				reason = "离线 worker 失败或产物未通过校验（appid 可能不正确）"
			}
			return
		}
	}
	state = "succeeded"
	reason = ""
}

type progressWriter struct {
	mu       sync.Mutex
	t        *task
	buffer   []byte
	baseDone int
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	w.buffer = append(w.buffer, p...)
	for {
		idx := strings.IndexByte(string(w.buffer), '\n')
		if idx < 0 {
			break
		}
		w.line(string(w.buffer[:idx]))
		w.buffer = w.buffer[idx+1:]
	}
	if len(w.buffer) > 64<<10 {
		w.buffer = w.buffer[len(w.buffer)-(64<<10):]
	}
	return n, nil
}
func (w *progressWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) > 0 {
		w.line(string(w.buffer))
		w.buffer = nil
	}
}
func (w *progressWriter) line(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if strings.HasPrefix(line, "PROGRESS ") {
		var ev map[string]any
		if json.Unmarshal([]byte(line[9:]), &ev) != nil {
			return
		}
		w.t.event(ev)
		stage, _ := ev["stage"].(string)
		phases := map[string]string{"pipeline_start": "preparing", "decrypt_start": "decrypt", "decrypt_done": "decrypt", "format_start": "format", "format_done": "format", "unpack_done": "unpack", "merge_done": "merge", "restore_start": "restore", "decompile_start": "restore", "decompile_done": "restore", "parse_start": "restore", "parse_done": "restore", "pipeline_done": "done"}
		w.t.change(func(d map[string]any) {
			if phase := phases[stage]; phase != "" {
				d["phase"] = phase
			}
			if stage == "decrypt_start" || stage == "decrypt_done" || stage == "format_start" {
				d["current_file"] = ev["file"]
				d["current_file_bytes"] = ev["bytes"]
			}
			if stage == "package_done" {
				d["done_packages"] = min(num(d["total_packages"]), float64(w.baseDone)+num(ev["done_packages"]))
			}
		})
		return
	}
	w.t.change(func(d map[string]any) {
		r := []rune(line)
		tail := d["log_tail"].([]string)
		tail = append(tail, string(r[:min(len(r), 300)]))
		d["log_tail"] = tail[max(0, len(tail)-8):]
	})
}
