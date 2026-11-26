package wxapkg

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wmpfworkbench/internal/app"
)

const fixtureID = "wx0123456789abcdef"

func fixtureManager(t *testing.T, mode string) *Manager {
	t.Helper()
	p, _ := app.Resolve(t.TempDir())
	p.Roots = []app.Profile{{Label: "fixture", Path: p.Profile}}
	input := filepath.Join(p.Profile, "users", "0123456789abcdef0123456789abcdef", "applet", "packages", fixtureID, "1", "code.wxapkg")
	os.MkdirAll(filepath.Dir(input), 0700)
	os.WriteFile(input, []byte("V1MMWXfixture"), 0600)
	exe, _ := os.Executable()
	m := New(p, exe)
	m.command = func(ctx context.Context, args []string) *exec.Cmd {
		c := exec.CommandContext(ctx, exe, append([]string{"-test.run=TestWorkerProcess", "--"}, args...)...)
		c.Env = append(os.Environ(), "WXAPKG_FIXTURE="+mode)
		return c
	}
	t.Cleanup(func() { m.Close() })
	return m
}
func writeTaskState(t *testing.T, m *Manager, id, state string, when time.Time) {
	t.Helper()
	dir := filepath.Join(m.stateDir(), id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"task_id": id, "state": state, "created_at": seconds() - 3600, "finished_at": seconds(), "timeout": 30, "done_packages": 1, "total_packages": 2, "pid": nil})
	path := filepath.Join(dir, "status.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionPrunesOldTerminalTasks(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	m := New(p, "tool")
	defer m.Close()
	base := time.Now().Add(-time.Hour)
	ids := []string{}
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("%012x", i+1)
		ids = append(ids, id)
		writeTaskState(t, m, id, "succeeded", base.Add(time.Duration(i)*time.Minute))
	}
	m.prune()
	remaining := map[string]bool{}
	entries, _ := os.ReadDir(m.stateDir())
	for _, e := range entries {
		if e.IsDir() {
			remaining[e.Name()] = true
		}
	}
	if len(remaining) != 20 {
		t.Fatal("retention did not bound terminal tasks", len(remaining), remaining)
	}
	for i := 0; i < 5; i++ {
		if remaining[ids[i]] {
			t.Fatal("oldest terminal task kept", ids[i])
		}
	}
	for i := 5; i < 25; i++ {
		if !remaining[ids[i]] {
			t.Fatal("newest terminal task pruned", ids[i])
		}
	}
}

func TestPruneKeepsNonTerminalTasks(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	m := New(p, "tool")
	defer m.Close()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 25; i++ {
		state := "succeeded"
		if i == 0 || i == 24 {
			state = "running"
		}
		writeTaskState(t, m, fmt.Sprintf("%012x", i+1), state, base.Add(time.Duration(i)*time.Minute))
	}
	m.prune()
	for _, name := range []string{"000000000001", "000000000019"} {
		if _, err := os.Stat(filepath.Join(m.stateDir(), name, "status.json")); err != nil {
			t.Fatal("non-terminal task was pruned", name, err)
		}
	}
}

func TestInterruptedTaskIsFlagged(t *testing.T) {
	p, _ := app.Resolve(t.TempDir())
	m := New(p, "tool")
	defer m.Close()
	id := "abcdef123456"
	dir := filepath.Join(m.stateDir(), id)
	os.MkdirAll(dir, 0700)
	raw, _ := json.Marshal(map[string]any{"task_id": id, "state": "running", "created_at": seconds() - 3600, "started_at": seconds() - 3600, "timeout": 30, "pid": 999999, "done_packages": 1, "total_packages": 2})
	os.WriteFile(filepath.Join(dir, "status.json"), raw, 0600)
	s := m.Status(id)
	if s["interrupted"] != true {
		t.Fatal("crash-left task not flagged", s)
	}
	if s["eta_grade"] != nil {
		t.Fatal("ETA must be suppressed for interrupted tasks", s)
	}
	if s["state_note"] == nil {
		t.Fatal("no restart guidance", s)
	}
}

func waitFinal(t *testing.T, m *Manager, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		s := m.Status(id)
		if terminal(fmt.Sprint(s["state"])) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task did not finish: %+v", m.Status(id))
	return nil
}
func TestAsyncSuccessPersistsProgressAndValidatesOutput(t *testing.T) {
	m := fixtureManager(t, "success")
	r := m.Start(Options{AppID: fixtureID, Timeout: 30})
	if r["ok"] != true {
		t.Fatalf("start: %+v", r)
	}
	id := r["task_id"].(string)
	s := waitFinal(t, m, id)
	if s["state"] != "succeeded" || s["done_packages"] != float64(1) || s["restored_files"] != float64(1) {
		t.Fatalf("status: %+v", s)
	}
	other := New(m.Paths, m.Tool)
	defer other.Close()
	if st := other.Status(id); st["state"] != "succeeded" {
		t.Fatalf("disk status: %+v", st)
	}
	if r := other.Cancel(id); r["cancel_requested"] != false {
		t.Fatalf("terminal cancel: %+v", r)
	}
}
func TestCrossManagerCancelKillsSilentWorker(t *testing.T) {
	m := fixtureManager(t, "silent")
	r := m.Start(Options{AppID: fixtureID, Timeout: 30})
	id, ok := r["task_id"].(string)
	if !ok {
		t.Fatal(r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.Status(id)["pid"] == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	other := New(m.Paths, m.Tool)
	defer other.Close()
	if r := other.Cancel(id); r["ok"] != true {
		t.Fatalf("cancel: %+v", r)
	}
	s := waitFinal(t, m, id)
	if s["state"] != "cancelled" {
		t.Fatalf("final: %+v", s)
	}
}
func TestNonzeroWorkerCannotReportSuccessWithAValidHeader(t *testing.T) {
	m := fixtureManager(t, "failure")
	r := m.Start(Options{AppID: fixtureID, Timeout: 30})
	s := waitFinal(t, m, r["task_id"].(string))
	if s["ok"] == true || s["state"] == "succeeded" {
		t.Fatalf("false success: %+v", s)
	}
}
func TestOutputGuardAndInvalidTaskID(t *testing.T) {
	m := fixtureManager(t, "success")
	for _, out := range []string{m.Paths.Root, filepath.Join(m.Paths.Root, "native", "overwrite"), filepath.Join(m.Paths.Root, "data", "overwrite")} {
		if r := m.Start(Options{AppID: fixtureID, OutDir: out, Timeout: 30}); r["ok"] == true {
			t.Fatalf("guard: %s", out)
		}
	}
	if r := m.Status("../../escape"); r["ok"] == true {
		t.Fatalf("invalid id: %+v", r)
	}
}

func TestSilentWorkerTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("30 second public timeout boundary")
	}
	m := fixtureManager(t, "silent")
	r := m.Start(Options{AppID: fixtureID, Timeout: 30})
	id, ok := r["task_id"].(string)
	if !ok {
		t.Fatal(r)
	}
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		s := m.Status(id)
		if terminal(fmt.Sprint(s["state"])) {
			if s["state"] != "failed" || !strings.Contains(fmt.Sprint(s["reason"]), "超时") {
				t.Fatalf("timeout: %+v", s)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("silent process was not killed by timeout")
}

func TestRealOfflineWorkerWithSyntheticEncryptedPackage(t *testing.T) {
	tool := os.Getenv("WMPF_TEST_WXAPKG_TOOL")
	if tool == "" {
		t.Skip("set WMPF_TEST_WXAPKG_TOOL to built offline worker")
	}
	m := fixtureManager(t, "success")
	m.Tool = tool
	m.command = func(ctx context.Context, args []string) *exec.Cmd { return exec.CommandContext(ctx, tool, args...) }
	files, _ := app.ListPackages(context.Background(), m.Paths, fixtureID)
	name := "app.js"
	body := []byte("var fixture = 'independent offline worker';" + strings.Repeat(" ", 1200))
	var plain bytes.Buffer
	plain.WriteByte(0xbe)
	binary.Write(&plain, binary.BigEndian, uint32(0))
	binary.Write(&plain, binary.BigEndian, uint32(4+12+len(name)))
	binary.Write(&plain, binary.BigEndian, uint32(len(body)))
	plain.WriteByte(0xed)
	binary.Write(&plain, binary.BigEndian, uint32(1))
	binary.Write(&plain, binary.BigEndian, uint32(len(name)))
	plain.WriteString(name)
	binary.Write(&plain, binary.BigEndian, uint32(14+4+12+len(name)))
	binary.Write(&plain, binary.BigEndian, uint32(len(body)))
	plain.Write(body)
	key, err := pbkdf2.Key(sha1.New, fixtureID, []byte("saltiest"), 1000, 32)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	first := make([]byte, 1024)
	copy(first, plain.Bytes()[:1023])
	encrypted := make([]byte, 1024)
	cipher.NewCBCEncrypter(block, []byte("the iv: 16 bytes")).CryptBlocks(encrypted, first)
	raw := append([]byte("V1MMWX"), encrypted...)
	for _, b := range plain.Bytes()[1023:] {
		raw = append(raw, b^fixtureID[len(fixtureID)-2])
	}
	if err = os.WriteFile(files[0].Path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := m.Start(Options{AppID: fixtureID, Timeout: 30})
	id, ok := r["task_id"].(string)
	if !ok {
		t.Fatal(r)
	}
	s := waitFinal(t, m, id)
	if s["state"] != "succeeded" || s["restored_files"] != float64(1) {
		t.Fatalf("real worker: %+v", s)
	}
	sources := s["sources"].([]any)
	out := sources[0].(map[string]any)["out_dir"].(string)
	restored, err := os.ReadFile(filepath.Join(out, name))
	if err != nil || !bytes.Equal(restored, body) {
		t.Fatalf("restored mismatch: %v", err)
	}
}
func TestWorkerProcess(t *testing.T) {
	mode := os.Getenv("WXAPKG_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "silent" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	out := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-out=") {
			out = strings.TrimPrefix(arg, "-out=")
		}
	}
	os.MkdirAll(out, 0700)
	os.WriteFile(filepath.Join(out, "code.wxapkg"), []byte{0xbe, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xed}, 0600)
	os.WriteFile(filepath.Join(out, "app.js"), []byte("fixture source"), 0600)
	fmt.Println(`PROGRESS {"stage":"package_done","done_packages":1}`)
	if mode == "failure" {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestStartRejectsUnsupportedSource(t *testing.T) {
	m := fixtureManager(t, "ok")
	result := m.Start(Options{AppID: fixtureID, Source: "export", Timeout: 30})
	if result["ok"] == true || !strings.Contains(fmt.Sprint(result["reason"]), "source") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestStartAcceptsRuntimeProfileSource(t *testing.T) {
	m := fixtureManager(t, "ok")
	result := m.Start(Options{AppID: fixtureID, Source: "runtime_profile", Timeout: 30})
	if result["ok"] != true || result["source_mode"] != "runtime_profile" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
