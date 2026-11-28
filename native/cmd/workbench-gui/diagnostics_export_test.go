package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readDiagnosticZip(t *testing.T, path string) map[string]string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	out := map[string]string{}
	for _, f := range reader.File {
		rc, openErr := f.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		buf, readErr := io.ReadAll(rc)
		rc.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		out[f.Name] = string(buf)
	}
	return out
}

func TestFindLatestFailedSession(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "data", "desktop", "sessions")
	older := filepath.Join(base, "aaaaaaaaaaaaaaaa")
	newer := filepath.Join(base, "bbbbbbbbbbbbbbbb")
	olderFailure := filepath.Join(older, "observation", "failure.json")
	newerFailure := filepath.Join(newer, "observation", "failure.json")
	writeTestFile(t, olderFailure, `{"session_id":"aaaaaaaaaaaaaaaa","appid":"wxold","state":"error","failed_at":"2026-11-23T13:25:38+08:00"}`)
	writeTestFile(t, newerFailure, `{"session_id":"bbbbbbbbbbbbbbbb","appid":"wxnew","state":"error","failed_at":"2026-11-28T12:52:12+08:00"}`)
	// Deliberately invert mtimes: failed_at must win over file timestamps.
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(olderFailure, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newerFailure, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	dir, failure, err := findLatestFailedSession(root)
	if err != nil {
		t.Fatal(err)
	}
	if dir != newer || diagnosticString(failure, "appid") != "wxnew" {
		t.Fatalf("wrong session selected: dir=%s failure=%#v", dir, failure)
	}
}

func TestFindLatestFailedSessionFallsBackToLegacyPath(t *testing.T) {
	root := t.TempDir()
	sess := filepath.Join(root, "data", "desktop", "sessions", "eeeeeeeeeeeeeeee")
	legacy := filepath.Join(sess, "failure.json")
	writeTestFile(t, legacy, `{"session_id":"eeeeeeeeeeeeeeee","appid":"wxlegacy","state":"error"}`)

	dir, failure, err := findLatestFailedSession(root)
	if err != nil {
		t.Fatal(err)
	}
	if dir != sess || diagnosticString(failure, "appid") != "wxlegacy" {
		t.Fatalf("legacy failure not found: dir=%s failure=%#v", dir, failure)
	}

	preferred := filepath.Join(sess, "observation", "failure.json")
	writeTestFile(t, preferred, `{"session_id":"eeeeeeeeeeeeeeee","appid":"wxlegacy","state":"error"}`)
	if got := diagnosticFailurePath(sess); got != preferred {
		t.Fatalf("observation path should win: %s", got)
	}
}

func TestFindLatestFailedSessionTieBreakers(t *testing.T) {
	const failedAt = "2026-11-28T12:52:12+08:00"
	writeSession := func(root, id string) string {
		path := filepath.Join(root, "data", "desktop", "sessions", id, "observation", "failure.json")
		writeTestFile(t, path, fmt.Sprintf(`{"session_id":%q,"appid":"wx%s","state":"error","failed_at":%q}`, id, id, failedAt))
		return path
	}
	setMtime := func(path string, when time.Time) {
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("mtime breaks same-second tie", func(t *testing.T) {
		root := t.TempDir()
		first := writeSession(root, "aaaaaaaaaaaaaaaa")
		second := writeSession(root, "bbbbbbbbbbbbbbbb")
		base := time.Date(2026, 11, 28, 4, 52, 12, 0, time.UTC)
		setMtime(first, base.Add(-time.Hour)) // newer ID but older mtime
		setMtime(second, base)
		dir, _, err := findLatestFailedSession(root)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(dir) != "bbbbbbbbbbbbbbbb" {
			t.Fatalf("mtime should break the tie, got %s", dir)
		}
	})

	t.Run("session id breaks exact tie", func(t *testing.T) {
		root := t.TempDir()
		first := writeSession(root, "aaaaaaaaaaaaaaaa")
		second := writeSession(root, "bbbbbbbbbbbbbbbb")
		base := time.Date(2026, 11, 28, 4, 52, 12, 0, time.UTC)
		setMtime(first, base)
		setMtime(second, base)
		dir, _, err := findLatestFailedSession(root)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(dir) != "bbbbbbbbbbbbbbbb" {
			t.Fatalf("session id should break the tie, got %s", dir)
		}
	})
}

func TestDiagnosticFailureTimeFormats(t *testing.T) {
	expected := time.Date(2026, 11, 27, 12, 59, 6, 0, time.FixedZone("CST", 8*3600))
	number := float64(expected.Unix())
	mas := float64(expected.UnixMilli())
	cases := []struct {
		name string
		raw  any
		want time.Time
	}{
		{"rfc3339", "2026-11-27T12:59:06+08:00", expected},
		{"rfc3339nano", "2026-11-27T04:59:06.123Z", expected.Add(123 * time.Millisecond).UTC()},
		{"dash", "2026-11-27 12:59:06", time.Date(2026, 11, 27, 12, 59, 6, 0, time.UTC)},
		{"slash", "2026/11/27 12:59:06", time.Date(2026, 11, 27, 12, 59, 6, 0, time.UTC)},
		{"unix string", fmt.Sprintf("%d", expected.Unix()), expected},
		{"unix float", number, expected},
		{"unix millis", mas, expected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diagnosticFailureTime(map[string]any{"failed_at": tc.raw})
			if !got.Equal(tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCollectAndWriteDiagnosticZip(t *testing.T) {
	root := t.TempDir()
	tempLogs := filepath.Join(root, "temp")
	if err := os.MkdirAll(tempLogs, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tempLogs)
	t.Setenv("TMP", tempLogs)

	dir := filepath.Join(root, "data", "desktop", "sessions", "ccdd112233445566")
	writeTestFile(t, filepath.Join(dir, "observation", "events.jsonl"),
		"{\"event\":\"worker.started\",\"pid\":4321}\n{\"event\":\"host.stdout\",\"payload\":{\"line\":\"[authcb] GetUserInfo uin=25985012628405866 status=2\"}}\n{\"event\":\"script\",\"payload\":{\"event\":\"host.Reset\"}}\n")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{"session_id":"ccdd112233445566","appid":"wxde8ac0a21135c07d"}`)
	failure := map[string]any{
		"session_id": "ccdd112233445566",
		"appid":      "wxde8ac0a21135c07d",
		"reason":     "repeated_reset",
		"state":      "error",
		"failed_at":  "2026-11-27T12:59:06+08:00",
	}
	failureJSON, _ := json.Marshal(failure)
	writeTestFile(t, filepath.Join(dir, "observation", "failure.json"), string(failureJSON))
	writeTestFile(t, filepath.Join(dir, "inputs", "metadata.json"), `{"appid":"wxde8ac0a21135c07d"}`)
	agentLog := filepath.Join(tempLogs, "wmpf-agent-4321.log")
	writeTestFile(t, agentLog, "[diag] attach.host.Reset=0 openid=owabcdefghijklmnopqrst token=SUPERSECRET_TOKEN\n")
	failedAt, err := time.Parse(time.RFC3339, "2026-11-27T12:59:06+08:00")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(agentLog, failedAt, failedAt); err != nil {
		t.Fatal(err)
	}

	entries, manifest, err := collectDiagnosticEntries(root, dir, failure, []string{"12:59:06 启动失败"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{
		"README.txt", "manifest.json", "events.jsonl", "worker.json", "failure.json",
		"inputs/metadata.json", "agent/wmpf-agent-4321.log", "ui-log.txt",
	} {
		if !names[want] {
			t.Fatalf("missing %s in %#v", want, names)
		}
	}
	if manifest.SessionID != "ccdd112233445566" || manifest.AppID != "wxde8ac0a21135c07d" {
		t.Fatalf("bad manifest: %#v", manifest)
	}
	manifestBytes, _ := json.Marshal(manifest)
	for _, sensitive := range []string{"25985012628405866", "SUPERSECRET_TOKEN", "owabcdefghijklmnopqrst", "buhuo"} {
		for _, e := range entries {
			if strings.Contains(string(e.data), sensitive) {
				t.Fatalf("entry %s still contains %q: %s", e.Name, sensitive, e.data)
			}
		}
		if strings.Contains(string(manifestBytes), sensitive) {
			t.Fatalf("manifest still contains %q: %s", sensitive, manifestBytes)
		}
	}

	dest := filepath.Join(t.TempDir(), "diagnostics.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	zipNames := readDiagnosticZip(t, dest)
	if !strings.Contains(zipNames["events.jsonl"], "host.Reset") {
		t.Fatalf("events missing from zip: %q", zipNames["events.jsonl"])
	}
	if !strings.Contains(zipNames["README.txt"], "worker.started") {
		t.Fatalf("README missing analysis hint: %q", zipNames["README.txt"])
	}
	var decoded diagnosticManifest
	if err := json.Unmarshal([]byte(zipNames["manifest.json"]), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Reason != "repeated_reset" || len(decoded.Files) < 6 {
		t.Fatalf("bad manifest in zip: %#v", decoded)
	}
}

func TestCollectWithoutEventsFileStillExports(t *testing.T) {
	root := t.TempDir()
	tempLogs := filepath.Join(root, "temp")
	if err := os.MkdirAll(tempLogs, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tempLogs)
	t.Setenv("TMP", tempLogs)

	dir := filepath.Join(root, "data", "desktop", "sessions", "ffffffffffffffff")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{"session_id":"ffffffffffffffff","appid":"wxmissing"}`)
	failure := map[string]any{
		"session_id": "ffffffffffffffff",
		"appid":      "wxmissing",
		"reason":     "repeated_reset",
		"state":      "error",
		"failed_at":  "2026-11-27T20:21:08+08:00",
	}
	failureJSON, _ := json.Marshal(failure)
	writeTestFile(t, filepath.Join(dir, "observation", "failure.json"), string(failureJSON))

	entries, manifest, err := collectDiagnosticEntries(root, dir, failure, nil)
	if err != nil {
		t.Fatalf("missing events.jsonl must not abort export: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"README.txt", "manifest.json", "missing.txt", "failure.json", "worker.json"} {
		if !names[want] {
			t.Fatalf("missing %s in %#v", want, names)
		}
	}
	found := false
	for _, m := range manifest.Missing {
		if strings.HasPrefix(m, "events.jsonl <-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("events.jsonl absence not recorded: %#v", manifest.Missing)
	}

	dest := filepath.Join(t.TempDir(), "diagnostics.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	missingEntry := readDiagnosticZip(t, dest)["missing.txt"]
	if !strings.Contains(missingEntry, "events.jsonl") {
		t.Fatalf("missing.txt lacks events.jsonl: %q", missingEntry)
	}
}

func TestManifestCoversAllArchiveFilesAndSelfHash(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TEMP", filepath.Join(root, "temp"))
	t.Setenv("TMP", filepath.Join(root, "temp"))
	dir := filepath.Join(root, "data", "desktop", "sessions", "1122334455667788")
	writeTestFile(t, filepath.Join(dir, "observation", "failure.json"), `{"session_id":"1122334455667788","state":"error","failed_at":"2026-11-27T12:59:06+08:00"}`)
	failure, err := readJSONMap(filepath.Join(dir, "observation", "failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	entries, manifest, err := collectDiagnosticEntries(root, dir, failure, []string{"ui line"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]diagnosticFileInfo{}
	for _, info := range manifest.Files {
		byName[info.Name] = info
	}
	for _, e := range entries {
		info, ok := byName[e.Name]
		if !ok {
			t.Fatalf("manifest.files misses %s", e.Name)
		}
		if info.Bytes != e.Bytes || info.SHA256 != e.SHA256 {
			t.Fatalf("manifest entry mismatch for %s: %#v vs %#v", e.Name, info, e)
		}
	}
	for _, required := range []string{"README.txt", "manifest.json"} {
		if _, ok := byName[required]; !ok {
			t.Fatalf("manifest.files misses %s", required)
		}
	}
	if _, ok := byName["missing.txt"]; !ok {
		t.Fatal("manifest.files should list missing.txt when it exists")
	}

	dest := filepath.Join(t.TempDir(), "diagnostics.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	archive := readDiagnosticZip(t, dest)
	rawManifest := archive["manifest.json"]
	if strings.Contains(rawManifest, diagnosticHashPlaceholder) {
		t.Fatalf("manifest hash placeholder not replaced: %s", rawManifest)
	}
	var decoded diagnosticManifest
	if err := json.Unmarshal([]byte(rawManifest), &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, info := range decoded.Files {
		if info.Name != "manifest.json" {
			continue
		}
		found = true
		// Self-hash is computed on the first pass, with the sha256 field
		// normalized to the fixed-length placeholder.
		canonical := strings.Replace(rawManifest, `"sha256": "`+info.SHA256+`"`,
			`"sha256": "`+diagnosticHashPlaceholder+`"`, 1)
		if sha256Hex([]byte(canonical)) != info.SHA256 {
			t.Fatalf("manifest self hash mismatch: %s vs canonical %s", info.SHA256, sha256Hex([]byte(canonical)))
		}
		if info.Bytes != int64(len(rawManifest)) {
			t.Fatalf("manifest self size mismatch: %d vs %d", info.Bytes, len(rawManifest))
		}
	}
	if !found {
		t.Fatal("manifest.json does not list itself")
	}
}

func TestUILogTailTruncation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TEMP", filepath.Join(root, "temp"))
	t.Setenv("TMP", filepath.Join(root, "temp"))
	dir := filepath.Join(root, "data", "desktop", "sessions", "1234567812345678")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{"session_id":"1234567812345678"}`)
	failure := map[string]any{"session_id": "1234567812345678", "state": "error", "failed_at": "2026-11-27T12:59:06+08:00"}
	logs := []string{"HEAD_MARKER", strings.Repeat("x", diagnosticLogTailLimit+1024), "TAIL_MARKER"}
	entries, manifest, err := collectDiagnosticEntries(root, dir, failure, logs)
	if err != nil {
		t.Fatal(err)
	}
	uiLog := ""
	for _, e := range entries {
		if e.Name == "ui-log.txt" {
			uiLog = string(e.data)
			if !e.Truncated {
				t.Fatal("oversized UI log must be marked truncated")
			}
			if int64(len(e.data)) > diagnosticLogTailLimit {
				t.Fatalf("UI log size %d exceeds limit", len(e.data))
			}
		}
	}
	if uiLog == "" {
		t.Fatal("ui-log.txt missing")
	}
	if strings.Contains(uiLog, "HEAD_MARKER") || !strings.Contains(uiLog, "TAIL_MARKER") {
		t.Fatal("UI log must keep the tail, not the head")
	}
	recorded := false
	for _, m := range manifest.Missing {
		if strings.HasPrefix(m, "ui-log.txt") {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("UI log truncation not recorded: %#v", manifest.Missing)
	}
	dest := filepath.Join(t.TempDir(), "diagnostics.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	if missing := readDiagnosticZip(t, dest)["missing.txt"]; !strings.Contains(missing, "ui-log.txt") {
		t.Fatalf("missing.txt lacks UI truncation note: %q", missing)
	}
}

func TestAttempt2ArtifactsCollected(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TEMP", filepath.Join(root, "temp"))
	t.Setenv("TMP", filepath.Join(root, "temp"))
	dir := filepath.Join(root, "data", "desktop", "sessions", "a1b2c3d4e5f60718")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{"attempt":1}`)
	writeTestFile(t, filepath.Join(dir, "attempt-2", "worker.json"), `{"attempt":2,"marker":"ATTEMPT2_WORKER"}`)
	writeTestFile(t, filepath.Join(dir, "attempt-2", "inputs", "metadata.json"), `{"marker":"ATTEMPT2_METADATA"}`)
	failure := map[string]any{"session_id": "a1b2c3d4e5f60718", "state": "error", "failed_at": "2026-11-27T12:59:06+08:00"}
	entries, _, err := collectDiagnosticEntries(root, dir, failure, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "diagnostics.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	archive := readDiagnosticZip(t, dest)
	for name, marker := range map[string]string{
		"attempt-2/worker.json":          "ATTEMPT2_WORKER",
		"attempt-2/inputs/metadata.json": "ATTEMPT2_METADATA",
	} {
		if !strings.Contains(archive[name], marker) {
			t.Fatalf("%s missing or wrong content: %q", name, archive[name])
		}
	}
}

func TestObservationFallbackForEventsAndBridge(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TEMP", filepath.Join(root, "temp"))
	t.Setenv("TMP", filepath.Join(root, "temp"))
	dir := filepath.Join(root, "data", "desktop", "sessions", "9988776655443322")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{}`)
	writeTestFile(t, filepath.Join(dir, "events.jsonl"), "EVENT_ROOT\n")
	writeTestFile(t, filepath.Join(dir, "bridge.jsonl"), "BRIDGE_ROOT\n")
	failure := map[string]any{"session_id": "9988776655443322", "state": "error", "failed_at": "2026-11-27T12:59:06+08:00"}
	entries, _, err := collectDiagnosticEntries(root, dir, failure, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "root-fallback.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	archive := readDiagnosticZip(t, dest)
	if !strings.Contains(archive["events.jsonl"], "EVENT_ROOT") || !strings.Contains(archive["bridge.jsonl"], "BRIDGE_ROOT") {
		t.Fatalf("root-level events/bridge not collected: %#v", archive)
	}

	writeTestFile(t, filepath.Join(dir, "observation", "events.jsonl"), "EVENT_OBSERVATION\n")
	writeTestFile(t, filepath.Join(dir, "observation", "bridge.jsonl"), "BRIDGE_OBSERVATION\n")
	entries, _, err = collectDiagnosticEntries(root, dir, failure, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest = filepath.Join(t.TempDir(), "observation-preferred.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	archive = readDiagnosticZip(t, dest)
	if !strings.Contains(archive["events.jsonl"], "EVENT_OBSERVATION") || !strings.Contains(archive["bridge.jsonl"], "BRIDGE_OBSERVATION") {
		t.Fatalf("observation events/bridge should win: %#v", archive)
	}
}

func TestAgentLogDedupAndWiderWindow(t *testing.T) {
	root := t.TempDir()
	tempLogs := filepath.Join(root, "temp")
	if err := os.MkdirAll(tempLogs, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEMP", tempLogs)
	t.Setenv("TMP", tempLogs)
	dir := filepath.Join(root, "data", "desktop", "sessions", "5566778899aabbcc")
	writeTestFile(t, filepath.Join(dir, "worker.json"), `{}`)
	failure := map[string]any{"session_id": "5566778899aabbcc", "state": "error", "failed_at": "2026-11-27T12:59:06+08:00"}
	failedAt := time.Date(2026, 11, 27, 4, 59, 6, 0, time.UTC)
	agent := filepath.Join(tempLogs, "wmpf-agent-1000.log")
	writeTestFile(t, agent, "AGENT_IN_WINDOW\n")
	devtools := filepath.Join(tempLogs, "wmpf-devtools-frontend-2000.log")
	writeTestFile(t, devtools, "DEVTOOLS_IN_WINDOW\n")
	old := filepath.Join(tempLogs, "wmpf-agent-9999.log")
	writeTestFile(t, old, "AGENT_OUT_OF_WINDOW\n")
	for path, when := range map[string]time.Time{
		agent:    failedAt.Add(-25 * time.Minute),
		devtools: failedAt.Add(5 * time.Minute),
		old:      failedAt.Add(-90 * time.Minute),
	} {
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	entries, _, err := collectDiagnosticEntries(root, dir, failure, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "agent-window.zip")
	if err := writeDiagnosticZip(dest, entries); err != nil {
		t.Fatal(err)
	}
	archive := readDiagnosticZip(t, dest)
	if !strings.Contains(archive["agent/wmpf-agent-1000.log"], "AGENT_IN_WINDOW") {
		t.Fatal("agent log 25 minutes before failure should be included")
	}
	if !strings.Contains(archive["devtools/wmpf-devtools-frontend-2000.log"], "DEVTOOLS_IN_WINDOW") {
		t.Fatal("devtools log 5 minutes after failure should be included")
	}
	if _, ok := archive["agent/wmpf-agent-9999.log"]; ok {
		t.Fatal("agent log outside the wider window must be excluded")
	}

	// Two names for the same inode must not produce duplicate archive entries.
	alias := filepath.Join(tempLogs, "wmpf-agent-1001.log")
	if err := os.Link(agent, alias); err != nil {
		t.Logf("hard link unavailable, skipped SameFile dedup check: %v", err)
		return
	}
	entries, _, err = collectDiagnosticEntries(root, dir, failure, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentEntries := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name, "agent/wmpf-agent-100") {
			agentEntries++
		}
	}
	if agentEntries != 1 {
		t.Fatalf("hard-linked agent logs must be deduplicated, got %d entries", agentEntries)
	}
}

func TestWriteDiagnosticZipAtomicReplaceAndCleanup(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "diagnostics.zip")
	writeTestFile(t, dest, "old content")
	if err := writeDiagnosticZip(dest, []diagnosticEntry{{Name: "new.txt", data: []byte("NEW"), mod: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	archive := readDiagnosticZip(t, dest)
	if archive["new.txt"] != "NEW" || len(archive) != 1 {
		t.Fatalf("atomic replace failed: %#v", archive)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			payload := fmt.Sprintf("PAYLOAD_%d", n)
			entries := []diagnosticEntry{{Name: fmt.Sprintf("w-%d.txt", n), data: []byte(payload), mod: time.Now()}}
			if err := writeDiagnosticZip(dest, entries); err != nil {
				t.Errorf("concurrent write %d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()
	archive = readDiagnosticZip(t, dest)
	if len(archive) != 1 {
		t.Fatalf("final archive must come from one complete write: %#v", archive)
	}
	for name, content := range archive {
		if !strings.HasPrefix(content, "PAYLOAD_") || !strings.Contains(name, strings.TrimPrefix(content, "PAYLOAD_")) {
			t.Fatalf("torn archive: %s=%q", name, content)
		}
	}

	blocked := filepath.Join(t.TempDir(), "blocked.zip")
	if err := os.MkdirAll(blocked, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeDiagnosticZip(blocked, []diagnosticEntry{{Name: "x.txt", data: []byte("x"), mod: time.Now()}}); err == nil {
		t.Fatal("writing over a directory should fail")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(blocked), ".wmpf-diagnostics-*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files not cleaned up after failure: %#v", leftovers)
	}
}

func TestDiagnosticExportLockSerializes(t *testing.T) {
	release := make(chan struct{})
	firstAcquired := make(chan struct{})
	firstDone := make(chan struct{})
	go withDiagnosticExportLock(func() {
		close(firstAcquired)
		<-release
		close(firstDone)
	})
	<-firstAcquired
	secondStarted := make(chan struct{})
	secondDone := make(chan struct{})
	go withDiagnosticExportLock(func() {
		close(secondStarted)
		close(secondDone)
	})
	select {
	case <-secondStarted:
		t.Fatal("second export must wait for the first")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-firstDone
	<-secondDone
}

func TestRedactDiagnosticText(t *testing.T) {
	root := `E:\work\app`
	temp := `C:\Users\alice\AppData\Local\Temp`
	input := `uin=12345678901234567 token=abc openid=owabcdefghijklmnopqrst path=` + root + `\data\config.json temp=` + temp + `\x.log`
	out := redactDiagnosticText(input, root, temp)
	for _, sensitive := range []string{"12345678901234567", "abc", "owabcdefghijklmnopqrst", `E:\work\app`, `C:\Users\alice`} {
		if strings.Contains(out, sensitive) {
			t.Fatalf("redaction left %q in %q", sensitive, out)
		}
	}
	for _, marker := range []string{"uin=***", "token=***", "<APP_ROOT>", "<TEMP>"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("redaction lost marker %s in %q", marker, out)
		}
	}
}

func TestRedactDiagnosticTextExtended(t *testing.T) {
	root := `E:\work\app`
	temp := `C:\Users\alice\AppData\Local\Temp`
	input := `{"accessToken":"SECRET_ACCESS","refreshToken":"SECRET_REFRESH","openId":"OPEN_SECRET_123","unionId":"UNION_SECRET_123","sessionId":"SID_SECRET_123","sign":"SIGN_SECRET","signature":"SIG_SECRET","cookie":{"uin":"12345678901234567","token":"COOKIE_TOKEN"}}` + "\n" +
		`Cookie: a=1; b=2; token=COOKIE_HEADER` + "\n" +
		`Set-Cookie: sessionid=SET_SID; uin=12345678901234567` + "\n" +
		`loginBuffer=LOGIN_BUFFER_SECRET appSessionKey=APP_SESSION_SECRET autoAuthKey=AUTO_SECRET` + "\n" +
		`path=` + `e:\WORK\APP\data\config.json` + ` temp=` + `c:\USERS\ALICE\appdata\local\temp\x.log`
	out := redactDiagnosticText(input, root, temp)
	for _, secret := range []string{
		"SECRET_ACCESS", "SECRET_REFRESH", "OPEN_SECRET_123", "UNION_SECRET_123", "SID_SECRET_123",
		"SIGN_SECRET", "SIG_SECRET", "COOKIE_TOKEN", "COOKIE_HEADER", "SET_SID", "LOGIN_BUFFER_SECRET",
		"APP_SESSION_SECRET", "AUTO_SECRET", "12345678901234567", `e:\WORK\APP`, `c:\USERS\ALICE`,
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("redaction left %q in %q", secret, out)
		}
	}
	for _, marker := range []string{
		`"accessToken":"***"`, `"refreshToken":"***"`, `"openId":"***"`,
		`"unionId":"***"`, `"sessionId":"***"`, `"sign":"***"`, `"signature":"***"`,
		`<APP_ROOT>\data\config.json`, `<TEMP>\x.log`,
	} {
		if !strings.Contains(out, marker) {
			t.Fatalf("redaction lost marker %q in %q", marker, out)
		}
	}
}

func TestReadDiagnosticFileTakesTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	writeTestFile(t, path, "0123456789")
	data, truncated, err := readDiagnosticFile(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || string(data) != "6789" {
		t.Fatalf("truncated=%v data=%q", truncated, data)
	}
}
