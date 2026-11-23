package logging

import (
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestCaptureSpoolOpenBoundsLockedDatabaseWait(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "captures.db")
	holder, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()

	key := make([]byte, 32)
	spool := &CaptureSpool{dir: dir, key: key}
	start := time.Now()
	if errOpen := spool.open(); errOpen == nil {
		_ = spool.db.Close()
		t.Fatal("expected error when captures.db is locked by another handle")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("open waited %v, want bounded timeout", elapsed)
	}
}
