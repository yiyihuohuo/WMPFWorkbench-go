package config

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureTokenConcurrentReturnsSingleValue(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "config.json"))
	const n = 16
	tokens := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = s.EnsureToken(context.Background(), "login_token")
		}(i)
	}
	wg.Wait()
	for i := range tokens {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if tokens[i] == "" {
			t.Fatalf("caller %d got empty token", i)
		}
		if tokens[i] != tokens[0] {
			t.Fatalf("concurrent callers got different tokens: %q vs %q", tokens[i], tokens[0])
		}
	}
	// Persisted value must equal what everyone got.
	cfg, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if String(cfg, "login_token") != tokens[0] {
		t.Fatal("persisted token differs from returned token")
	}
}

func TestEnsureTokenKeepsExistingValue(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "config.json"))
	if _, err := s.Update(context.Background(), map[string]any{"login_token": "existing"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.EnsureToken(context.Background(), "login_token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "existing" {
		t.Fatalf("existing token was replaced: %q", got)
	}
}
