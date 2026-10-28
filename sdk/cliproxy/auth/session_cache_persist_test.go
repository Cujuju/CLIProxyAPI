package auth

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSessionCacheSnapshotRestoreKeepsBindingsAndExpiry(t *testing.T) {
	source := NewSessionCache(time.Hour)
	defer source.Stop()
	source.setSessionAliases("auth-a", "claude", "claude:one", "claude::claude:one::opus")
	source.setSessionAliases("auth-b", "claude", "claude:two", "claude::claude:two::opus")
	records := source.Snapshot()
	if len(records) != 2 || records[0].AuthID != "auth-a" || records[1].AuthID != "auth-b" {
		t.Fatalf("snapshot = %+v, want auth-a then auth-b", records)
	}

	target := NewSessionCache(time.Hour)
	defer target.Stop()
	if restored := target.Restore(records); restored != 2 {
		t.Fatalf("restored %d, want 2", restored)
	}
	if got, ok := target.Get("claude::claude:two::opus"); !ok || got != "auth-b" {
		t.Fatalf("Get = %q %v, want auth-b", got, ok)
	}
	if got := target.latestSessionAuth("claude:one", map[string]struct{}{"claude": {}}); got != "auth-a" {
		t.Fatalf("latestSessionAuth = %q, want auth-a", got)
	}
	restoredRecords := target.Snapshot()
	if !restoredRecords[0].ExpiresAt.Equal(records[0].ExpiresAt) {
		t.Fatalf("expiry changed: %v, want %v", restoredRecords[0].ExpiresAt, records[0].ExpiresAt)
	}
}

func TestSessionCacheRestoreSkipsExpiredAndAlreadyBound(t *testing.T) {
	cache := NewSessionCache(time.Hour)
	defer cache.Stop()
	cache.Set("live", "auth-new")
	restored := cache.Restore([]SessionBindingRecord{
		{AuthID: "auth-old", ExpiresAt: time.Now().Add(time.Hour), Aliases: []string{"live"}},
		{AuthID: "auth-old", ExpiresAt: time.Now().Add(-time.Second), Aliases: []string{"expired"}},
	})
	if restored != 0 {
		t.Fatalf("restored %d, want 0", restored)
	}
	if got, _ := cache.Get("live"); got != "auth-new" {
		t.Fatalf("live binding = %q, want auth-new (a request that arrived first wins)", got)
	}
	if _, ok := cache.Get("expired"); ok {
		t.Fatal("expired record was restored")
	}
}

func TestFileSessionAffinityStoreRoundTrip(t *testing.T) {
	store := NewFileSessionAffinityStore(filepath.Join(t.TempDir(), SessionAffinityStateFile))
	if records, err := store.Load(); err != nil || records != nil {
		t.Fatalf("missing file: records=%v err=%v, want empty", records, err)
	}
	want := []SessionBindingRecord{{AuthID: "auth-a", ExpiresAt: time.Now().Add(time.Hour).UTC(), Aliases: []string{"x"}, SessionID: "s", Provider: "claude"}}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil || len(got) != 1 || got[0].AuthID != "auth-a" || !got[0].ExpiresAt.Equal(want[0].ExpiresAt) || got[0].Aliases[0] != "x" {
		t.Fatalf("Load = %+v %v, want %+v", got, err, want)
	}
}
