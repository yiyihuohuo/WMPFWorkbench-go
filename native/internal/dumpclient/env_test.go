package dumpclient

import (
	"strings"
	"testing"
)

func TestWithEnvSetsAndReplacesRemoteHome(t *testing.T) {
	env := []string{"A=1", "WXAPKG_REMOTE_HOME=old", "B=2"}
	out := withEnv(env, "WXAPKG_REMOTE_HOME", "new")
	joined := strings.Join(out, "|")
	if !strings.Contains(joined, "WXAPKG_REMOTE_HOME=new") {
		t.Fatalf("missing new value: %s", joined)
	}
	if strings.Contains(joined, "WXAPKG_REMOTE_HOME=old") {
		t.Fatalf("old value remains: %s", joined)
	}
	if !strings.Contains(joined, "A=1") || !strings.Contains(joined, "B=2") {
		t.Fatalf("existing env lost: %s", joined)
	}
}
