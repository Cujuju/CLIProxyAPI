package resetcredits

import (
	"encoding/json"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func roundTrip(t *testing.T, metadata map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(metadata)
	var out map[string]any
	if errUnmarshal := json.Unmarshal(raw, &out); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return out
}

func TestOfReadsAStoredLookupAfterAJSONRoundTrip(t *testing.T) {
	checkedAt := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	expiry := time.Date(2026, 11, 15, 8, 30, 0, 0, time.UTC)
	metadata := map[string]any{}
	Store(metadata, checkedAt, &Credits{Available: 2, Applicable: 1, Expiries: []time.Time{expiry}})
	got, ok := Of(&coreauth.Auth{Metadata: roundTrip(t, metadata)})
	want := Credits{Available: 2, Applicable: 1, Expiries: []time.Time{expiry}}
	if !ok || !got.Same(want) || !got.CheckedAt.Equal(checkedAt) {
		t.Fatalf("credits = %+v, %v", got, ok)
	}
}

func TestStoreWithoutCreditsClearsCountsButRecordsTheCheck(t *testing.T) {
	metadata := map[string]any{}
	Store(metadata, time.Now(), &Credits{Available: 1, Expiries: []time.Time{time.Now()}})
	Store(metadata, time.Now(), nil)
	if _, ok := Of(&coreauth.Auth{Metadata: metadata}); ok {
		t.Fatal("credits kept after a lookup reported none")
	}
	if len(metadata) != 1 || metadata[checkedAtKey] == nil {
		t.Fatalf("metadata = %v; want only the check time", metadata)
	}
}
