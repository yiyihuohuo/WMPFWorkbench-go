package process

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Uses only the observer's self-contained fixture, never the official client.
func TestObserverNativeFixture(t *testing.T) {
	exe := os.Getenv("WMPF_TEST_OBSERVER")
	if exe == "" {
		t.Skip("set WMPF_TEST_OBSERVER to the built native worker")
	}
	dir := t.TempDir()
	cfg := map[string]any{"host": exe, "appid": "wx0123456789abcdef", "runtime": dir, "data_root": dir, "input_dir": dir, "out": dir, "stop_file": filepath.Join(dir, "stop"), "gate_file": filepath.Join(dir, "go"), "seconds": 30, "fixture": true, "hide_window": true}
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "--config", path)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	p, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.Stop(ctx)
	}()
	if err = os.WriteFile(filepath.Join(dir, "go"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	rows := make(chan map[string]any, 128)
	go func() {
		defer close(rows)
		s := bufio.NewScanner(pipe)
		for s.Scan() {
			var row map[string]any
			if json.Unmarshal(s.Bytes(), &row) == nil {
				rows <- row
			}
		}
	}()
	attached := 0
	stopped := false
	verified := false
	deadline := time.After(40 * time.Second)
	for {
		select {
		case row, ok := <-rows:
			if !ok {
				if !verified {
					t.Fatal("no verified cleanup")
				}
				if err := p.Err(); err != nil {
					t.Fatal(err)
				}
				return
			}
			t.Log(row)
			if row["event"] == "session.attached" {
				attached++
				if attached == 2 && !stopped {
					os.WriteFile(filepath.Join(dir, "stop"), nil, 0600)
					stopped = true
				}
			}
			if row["event"] == "cleanup.verified" {
				v, _ := row["payload"].(map[string]any)
				count, _ := v["checked_pids"].(float64)
				left, _ := v["remaining_pids"].([]any)
				active, exists := v["job_active_processes"].(float64)
				if count < 2 || len(left) != 0 || !exists || active != 0 {
					t.Fatalf("unproven cleanup: %v", v)
				}
				verified = true
			}
		case <-deadline:
			t.Fatal("worker exceeded fixture deadline")
		}
	}
}
