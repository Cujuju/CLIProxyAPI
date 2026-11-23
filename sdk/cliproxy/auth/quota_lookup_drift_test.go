package auth

import (
	"net/http"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestLookupObserversLogSameDriftLine(t *testing.T) {
	hook := setupTestLoggerHook(t)
	log.SetLevel(log.InfoLevel)
	lookedUpAt := time.Now()
	for _, tc := range []struct {
		provider string
		live     http.Header
		lookup   http.Header
		want     string
	}{
		{"claude", http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.57"}},
			http.Header{
				"Anthropic-Ratelimit-Unified-5h-Utilization":        {"0.58"},
				"Anthropic-Ratelimit-Unified-7d_sonnet-Utilization": {"0.2"},
			}, "five_hour=58/57 seven_day_sonnet=20/-"},
		{"codex", http.Header{"X-Codex-Primary-Used-Percent": {"57"}},
			http.Header{
				"X-Codex-Primary-Used-Percent":             {"58"},
				"X-Codex-Code-Review-Primary-Used-Percent": {"20"},
			}, "code-review:primary=20/- primary=58/57"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			a := &Auth{ID: tc.provider + "-drift", Provider: tc.provider, Index: "abc123"}
			a.Quota.ObserveResponseHeadersForProvider(tc.provider, tc.live, lookedUpAt.Add(-30*time.Second))
			manager.auths[a.ID] = a
			hook.Reset()
			if !manager.ObserveQuotaLookup(a.ID, tc.lookup, lookedUpAt) {
				t.Fatal("legacy lookup not recorded")
			}
			windows := signalWindows(tc.provider, collectQuotaSignals(tc.provider, tc.lookup), lookedUpAt)
			if recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), a.ID, windows, lookedUpAt, nil); !recorded || err != nil {
				t.Fatalf("normalized lookup recorded = %v, err = %v", recorded, err)
			}
			want := "quota lookup vs response headers (lookup/header %): auth_index=abc123 provider=" + tc.provider + " header_age=30s " + tc.want
			entries := hook.AllEntries()
			if len(entries) != 2 || entries[0].Message != want || entries[1].Message != want {
				t.Fatalf("drift logs = %+v, want two copies of %q", entries, want)
			}
		})
	}
}
