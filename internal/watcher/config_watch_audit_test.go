package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestSyncAuthDirWatchPreservesSharedConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	otherDir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("port: 8317\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fsw.Close() }()
	if err := fsw.Add(dir); err != nil {
		t.Fatal(err)
	}

	w := &Watcher{
		configPath:           cfgPath,
		authDir:              otherDir,
		started:              true,
		watchedAuthDir:       dir,
		watchedConfigDirs:    []string{dir},
		watchedConfigTargets: []string{cfgPath},
		watcher:              fsw,
		config:               &config.Config{AuthDir: otherDir},
	}
	w.syncAuthDirWatch()

	stillWatchingConfigDir := false
	for _, watched := range fsw.WatchList() {
		if w.sameNormalizedAuthPath(watched, dir) {
			stillWatchingConfigDir = true
			break
		}
	}
	if !stillWatchingConfigDir {
		t.Fatalf("config directory %s was removed from watch list: %v", dir, fsw.WatchList())
	}
}
