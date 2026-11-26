package process

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestOwnedProcessCloseTerminatesSilentChild(t *testing.T) {
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=TestSilentChild")
	cmd.Env = append(os.Environ(), "OWNED_PROCESS_FIXTURE=1")
	p, err := Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-ctx.Done():
		t.Fatal("owned process did not stop")
	}
	if err = p.Stop(ctx); err != nil {
		t.Fatal("stop not idempotent", err)
	}
}
func TestSilentChild(t *testing.T) {
	if os.Getenv("OWNED_PROCESS_FIXTURE") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}
