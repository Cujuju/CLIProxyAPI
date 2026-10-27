package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigWatcherReloadsRepeatedAtomicReplacements(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	write := func(retry int, replace bool) {
		t.Helper()
		dest := path
		if replace {
			dest += ".tmp"
		}
		raw := fmt.Sprintf("config-version: 8\noauth:\n  auth-dir: %q\nrouting:\n  retry:\n    request-retry: %d\n", filepath.ToSlash(authDir), retry)
		if err := os.WriteFile(dest, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if replace {
			if err := os.Rename(dest, path); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(1, false)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequestRetry != 1 {
		t.Fatalf("initial request-retry=%d, want 1", cfg.RequestRetry)
	}
	applied := make(chan int, 16)
	w, err := NewWatcher(path, authDir, func(cfg *config.Config) { applied <- cfg.RequestRetry })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer func() {
		if errStop := w.Stop(); errStop != nil {
			t.Error(errStop)
		}
	}()
	w.SetConfig(cfg)
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, retry := range []int{2, 3, 4} {
		write(retry, true)
		deadline := time.NewTimer(5 * time.Second)
		for {
			select {
			case got := <-applied:
				if got == retry {
					deadline.Stop()
					goto next
				}
			case <-deadline.C:
				t.Fatalf("atomic replacement with request-retry=%d was not reloaded", retry)
			}
		}
	next:
	}
}
