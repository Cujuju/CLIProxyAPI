package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityUsageSummaryWindows(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	oldClock := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = oldClock })
	var mu sync.Mutex
	var paths []string
	probePaths := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(paths)
	}
	summary := `{"groups":[{"displayName":"Gemini group","buckets":[
		{"bucketId":"gemini-5h","window":"5h","remainingFraction":1,"resetTime":"2026-11-22T23:01:00Z"},
		{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":1,"resetTime":"2026-11-29T18:00:00Z"},
		{"bucketId":"gemini-argon-5h","window":"5h","remainingFraction":1,"description":"started","resetTime":"2026-11-22T23:00:00Z"},
		{"bucketId":"gemini-flash-5h","window":"5h","remainingFraction":1,"resetTime":"2026-11-22T22:58:59Z"},
		{"bucketId":"3p-weekly","window":"weekly","resetTime":"2026-11-29T18:00:00Z"},
		{"bucketId":"custom-weekly","window":"weekly","remainingFraction":-0.1}
	]},{"buckets":[
		{"bucketId":"gemini-carbon-5h","window":"5h","remainingFraction":0.8,"resetTime":"2026-11-22T23:00:00Z"},
		{"bucketId":"gemini-argon-weekly","window":"weekly","remainingFraction":0.2},
		{"bucketId":"custom-5h","window":"5h","remainingFraction":1e-20}
	]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		response := summary
		mu.Unlock()
		switch r.URL.Path {
		case sdkauth.AntigravityQuotaSummaryPath:
			_, _ = io.WriteString(w, response)
		case antigravityModelsPath:
			_, _ = io.WriteString(w, `{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.2},"weeklyQuotaInfo":{"resetTime":"2026-11-29T18:00:00Z"}}}}`)
		default:
			t.Errorf("unexpected probe path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("summary", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	lookup := svc.NewAntigravityUsageLookup()
	if err := lookup.Refresh(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	current, _ := manager.GetByID(auth.ID)
	windows := coreauth.QuotaWindows(current, now)
	if paths := probePaths(); !slices.Equal(paths, []string{sdkauth.AntigravityQuotaSummaryPath}) || len(windows) != 9 ||
		windows[0].Label != "5-hour" || windows[1].Label != "Weekly" {
		t.Fatalf("summary did not supply the leading windows, paths=%v windows=%+v", paths, windows)
	}
	byLabel := make(map[string]coreauth.QuotaWindow)
	for _, window := range windows {
		byLabel[window.Label] = window
	}
	for _, tc := range []struct {
		label, status, resetTime string
		seconds                  int
		scoped                   bool
		used                     float64
	}{
		{"5-hour", "allowed", "", 18000, false, 0},
		{"Weekly", "allowed", "2026-11-29T18:00:00Z", 604800, false, 0},
		{"Gemini group · 5-hour", "allowed", "2026-11-22T23:00:00Z", 18000, true, 0},
		{"Gemini · 5-hour", "allowed", "2026-11-22T22:58:59Z", 18000, true, 0},
		{"Claude & GPT · Weekly", "rejected", "2026-11-29T18:00:00Z", 604800, true, 100},
		{"Gemini group · Weekly", "rejected", "", 604800, true, 100},
		{"Gemini Carbon · 5-hour", "allowed", "2026-11-22T23:00:00Z", 18000, true, 20},
		{"Gemini Argon · Weekly", "allowed", "", 604800, true, 80},
		{"custom-5h · 5-hour", "allowed", "", 18000, true, math.Nextafter(100, 0)},
	} {
		wantReset, _ := time.Parse(time.RFC3339, tc.resetTime)
		cadence := coreauth.ResetCadenceOnUse
		if tc.seconds == 604800 {
			cadence = coreauth.ResetCadenceFixed
		}
		window := byLabel[tc.label]
		if window.Status != tc.status || window.Duration != time.Duration(tc.seconds)*time.Second ||
			window.Scoped != tc.scoped || window.Cadence != cadence || !window.ResetsAt.Equal(wantReset) ||
			window.UsedPercent == nil || math.Abs(*window.UsedPercent-tc.used) > 1e-10 ||
			(window.Status == "allowed" && *window.UsedPercent >= 100) {
			t.Errorf("window %q = %+v, want %+v", tc.label, window, tc)
		}
	}

	now = now.Add(time.Second)
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	afterCatalog, _ := manager.GetByID(auth.ID)
	if _, ok := hints.ModelIDs["gemini-pro"]; !ok || !reflect.DeepEqual(current.Quota, afterCatalog.Quota) {
		t.Fatalf("catalog discovery failed or replaced summary: hints=%+v quota=%+v", hints, afterCatalog.Quota)
	}

	now = now.Add(time.Second)
	mu.Lock()
	summary = `{"groups":[{"buckets":[{"bucketId":"gemini-5h","window":"5h","disabled":true,"remainingFraction":0}]}]}`
	mu.Unlock()
	if err := lookup.Refresh(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	fallback, _ := manager.GetByID(auth.ID)
	windows = coreauth.QuotaWindows(fallback, now)
	wantPaths := []string{sdkauth.AntigravityQuotaSummaryPath, antigravityModelsPath, sdkauth.AntigravityQuotaSummaryPath, antigravityModelsPath}
	if paths := probePaths(); !slices.Equal(paths, wantPaths) || !fallback.Quota.LookedUpAt.Equal(now) || len(windows) != 2 {
		t.Fatalf("expected fresh fallback snapshot, paths=%v quota=%+v", paths, fallback.Quota)
	}
	if !windows[0].Scoped || !windows[1].Scoped ||
		windows[0].Key != "ag:gemini" || windows[0].Label != "Gemini" ||
		windows[1].Key != "weekly:ag:gemini" || windows[1].Label != "Gemini · Weekly" ||
		windows[0].Duration != 0 || windows[1].Duration != 604800*time.Second ||
		windows[1].Status != coreauth.QuotaWindowStatusRejected {
		t.Fatalf("unexpected model fallback windows %+v", windows)
	}
}

func TestAntigravityUsageSummaryKeepsQueuedWindowUnstarted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		queuedAt := time.Now()
		now := queuedAt
		oldClock := antigravityNowFunc
		antigravityNowFunc = func() time.Time { return now }
		defer func() { antigravityNowFunc = oldClock }()
		oldSlots := antigravitySummaryProbeSlots
		antigravitySummaryProbeSlots = make(chan struct{}, 1)
		antigravitySummaryProbeSlots <- struct{}{}
		defer func() { antigravitySummaryProbeSlots = oldSlots }()
		oldTransport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != sdkauth.AntigravityQuotaSummaryPath {
				return nil, fmt.Errorf("unexpected probe path %s", req.URL.Path)
			}
			reset := now.Add(antigravityFiveHourWindow).UTC().Format(time.RFC3339)
			body := fmt.Sprintf(`{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":1,"resetTime":%q}]}`, reset)
			return antigravityVersionTestResponse(req, http.StatusOK, body), nil
		})
		defer func() { http.DefaultTransport = oldTransport }()

		manager := coreauth.NewManager(nil, nil, nil)
		svc := &Service{coreManager: manager}
		auth, err := manager.Register(t.Context(), antigravityTestAuth("summary-queued", "https://quota.invalid"))
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- svc.NewAntigravityUsageLookup().Refresh(t.Context(), auth) }()
		synctest.Wait()
		now = queuedAt.Add(antigravityUnstartedTolerance + time.Second)
		<-antigravitySummaryProbeSlots
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		stored, _ := manager.GetByID(auth.ID)
		if !stored.Quota.LookedUpAt.Equal(queuedAt) {
			t.Fatalf("published lookup time = %v, want %v", stored.Quota.LookedUpAt, queuedAt)
		}
		windows := stored.Quota.LookupWindows
		if len(windows) != 1 || !windows[0].ResetsAt.IsZero() {
			t.Fatalf("queued summary should publish one unstarted window, got %+v", windows)
		}
	})
}

func TestAntigravityUsageDoesNotShareCatalogOnlyResult(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	oldClock := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = oldClock })
	catalogStarted, releaseCatalog := make(chan struct{}), make(chan struct{})
	catalogDone := make(chan struct{})
	catalogHints := make(chan antigravityModelCapabilityHints, 1)
	summaryStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case antigravityModelsPath:
			close(catalogStarted)
			<-releaseCatalog
			_, _ = io.WriteString(w, `{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.25}}}}`)
		case sdkauth.AntigravityQuotaSummaryPath:
			close(summaryStarted)
			<-catalogDone
			_, _ = io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.5}]}`)
		default:
			t.Errorf("unexpected probe path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("usage-after-catalog", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(catalogDone)
		catalogHints <- svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	}()
	<-catalogStarted
	usageDone := make(chan error, 1)
	go func() { usageDone <- svc.NewAntigravityUsageLookup().Refresh(t.Context(), auth) }()
	select {
	case <-summaryStarted:
		close(releaseCatalog)
	case <-time.After(2 * time.Second):
		close(releaseCatalog)
		<-usageDone
		t.Fatal("summary could not start while the catalog was in flight")
	}
	if hints := <-catalogHints; len(hints.ModelIDs) != 1 {
		t.Fatalf("overlapping summary superseded catalog: %+v", hints)
	}
	if err := <-usageDone; err != nil {
		t.Fatal(err)
	}
	current, _ := manager.GetByID(auth.ID)
	windows := current.Quota.LookupWindows
	if len(windows) != 1 || !strings.HasPrefix(windows[0].Key, antigravitySummaryWindowPrefix) || windows[0].Scoped {
		t.Fatalf("usage lookup stored %+v, want account-wide summary", windows)
	}
}

func TestAntigravityScheduledRefreshUpdatesUsage(t *testing.T) {
	for _, summaryAvailable := range []bool{true, false} {
		name := "summary"
		if !summaryAvailable {
			name = "catalog fallback"
		}
		t.Run(name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
			oldClock := antigravityNowFunc
			antigravityNowFunc = func() time.Time { return now }
			t.Cleanup(func() { antigravityNowFunc = oldClock })
			var mu sync.Mutex
			var paths []string
			remaining := "0.75"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case sdkauth.AntigravityQuotaSummaryPath:
					if !summaryAvailable {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":`+remaining+`}]}`)
				case antigravityModelsPath:
					_, _ = io.WriteString(w, `{"models":{"gemini-3.1-flash-lite":{"quotaInfo":{"remainingFraction":`+remaining+`}}}}`)
				default:
					t.Errorf("unexpected probe path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			svc := &Service{coreManager: manager}
			auth, err := manager.Register(t.Context(), antigravityTestAuth("scheduled-usage", server.URL))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
			refresh := func() {
				svc.refreshAntigravityModels(t.Context())
				svc.WaitAntigravityProbes()
			}
			check := func(wantUsed float64, wantPaths []string) {
				t.Helper()
				current, _ := manager.GetByID(auth.ID)
				windows := current.Quota.LookupWindows
				if len(windows) != 1 || windows[0].UsedPercent == nil || *windows[0].UsedPercent != wantUsed ||
					hasAntigravitySummaryWindows(windows) != summaryAvailable || !current.Quota.LookedUpAt.Equal(now) {
					t.Fatalf("unexpected scheduled quota: %+v", current.Quota)
				}
				mu.Lock()
				defer mu.Unlock()
				if !slices.Equal(slices.Sorted(slices.Values(paths)), slices.Sorted(slices.Values(wantPaths))) {
					t.Fatalf("requests = %v, want %v", paths, wantPaths)
				}
			}
			if summaryAvailable {
				// A freshly cached catalog must not delay the startup summary.
				svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			}
			svc.asyncProbeAntigravityCapabilities(t.Context(), auth, "antigravity")
			svc.WaitAntigravityProbes()
			initialPaths := []string{antigravityModelsPath, sdkauth.AntigravityQuotaSummaryPath}
			check(25, initialPaths)
			refresh()
			check(25, initialPaths)
			mu.Lock()
			remaining = "0.5"
			mu.Unlock()
			now = now.Add(antigravityCapabilityCacheTTL)
			refresh()
			check(50, append(slices.Clone(initialPaths), antigravityModelsPath, sdkauth.AntigravityQuotaSummaryPath))
			if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") {
				t.Fatal("scheduled refresh did not register the catalog")
			}
		})
	}
}

func TestAntigravityUsageBackgroundProbesRunIndependently(t *testing.T) {
	for _, tc := range []struct {
		name, stalledPath string
		emptyFirstSummary bool
	}{
		{"stalled catalog", antigravityModelsPath, false},
		{"stalled summary", sdkauth.AntigravityQuotaSummaryPath, false},
		{"stalled catalog fallback", antigravityModelsPath, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resetAntigravityCapabilityCache()
				defer resetAntigravityCapabilityCache()
				defer setAntigravityTestProbeSlots()()
				oldTransport := http.DefaultTransport
				var stalledCalls, completedCalls int
				http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
					if req.URL.Path == tc.stalledPath {
						stalledCalls++
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					completedCalls++
					if req.URL.Path == antigravityModelsPath {
						model := "gemini-3.1-flash-lite"
						if completedCalls == 2 {
							model = "gemini-3-flash"
						}
						return antigravityVersionTestResponse(req, http.StatusOK, fmt.Sprintf(`{"models":{%q:{}}}`, model)), nil
					}
					if tc.emptyFirstSummary && completedCalls == 1 {
						return antigravityVersionTestResponse(req, http.StatusNotFound, ""), nil
					}
					return antigravityVersionTestResponse(req, http.StatusOK, `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.5}]}`), nil
				})
				defer func() { http.DefaultTransport = oldTransport }()
				manager := coreauth.NewManager(nil, nil, nil)
				svc := &Service{coreManager: manager}
				auth, err := manager.Register(t.Context(), antigravityTestAuth("background-independence", "https://quota.invalid"))
				if err != nil {
					t.Fatal(err)
				}
				defer GlobalModelRegistry().UnregisterClient(auth.ID)
				ctx, cancel := context.WithCancel(t.Context())
				defer func() { cancel(); svc.WaitAntigravityProbes() }()
				if tc.emptyFirstSummary {
					svc.queueAntigravitySummaryRefresh(ctx, auth)
				} else {
					svc.asyncProbeAntigravityCapabilities(ctx, auth, "antigravity")
				}
				for i, model := range []string{"gemini-3.1-flash-lite", "gemini-3-flash"} {
					synctest.Wait()
					if stalledCalls != 1 || completedCalls != i+1 {
						t.Fatalf("refresh %d: stalled requests=%d completed requests=%d", i, stalledCalls, completedCalls)
					}
					if tc.stalledPath == sdkauth.AntigravityQuotaSummaryPath {
						if !GlobalModelRegistry().ClientSupportsModel(auth.ID, model) {
							t.Fatalf("stalled summary blocked registration of %s", model)
						}
					} else if i != 0 || !tc.emptyFirstSummary {
						current, _ := manager.GetByID(auth.ID)
						if !hasAntigravitySummaryWindows(current.Quota.LookupWindows) || !current.Quota.LookedUpAt.Equal(time.Now()) {
							t.Fatal("stalled catalog blocked quota summary publication")
						}
					}
					if i == 0 {
						time.Sleep(antigravityCapabilityCacheTTL) // Advance the synctest clock.
						svc.refreshAntigravityModels(ctx)
					}
				}
			})
		})
	}
}

func TestAntigravityUsageFallbackPublishesDespiteStalledSummaryRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		defer setAntigravityTestProbeSlots()()
		releaseCatalog := make(chan struct{})
		var summaryCalls, catalogCalls atomic.Int32
		oldTransport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == sdkauth.AntigravityQuotaSummaryPath {
				if summaryCalls.Add(1) == 1 {
					return antigravityVersionTestResponse(req, http.StatusNotFound, ""), nil
				}
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			catalogCalls.Add(1)
			select {
			case <-releaseCatalog:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return antigravityVersionTestResponse(req, http.StatusOK,
				`{"models":{"gemini-3-flash":{"quotaInfo":{"remainingFraction":0.75,"resetTime":"2026-11-01T00:00:00Z"}}}}`), nil
		})
		defer func() { http.DefaultTransport = oldTransport }()
		manager := coreauth.NewManager(nil, nil, nil)
		svc := &Service{coreManager: manager}
		priorUsed := 90.0
		seed := antigravityTestAuth("fallback-during-retry", "https://quota.invalid")
		// Seed a prior summary window so catalogOnly=true cannot mask a dropped fallback.
		seed.Quota.LookedUpAt = time.Now().Add(-time.Hour)
		seed.Quota.LookupWindows = []coreauth.QuotaWindow{{Key: antigravitySummaryWindowPrefix + "gemini-5h", UsedPercent: &priorUsed}}
		auth, err := manager.Register(t.Context(), seed)
		if err != nil {
			t.Fatal(err)
		}
		defer GlobalModelRegistry().UnregisterClient(auth.ID)
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); svc.WaitAntigravityProbes() }()

		// Block the catalog worker, then let an empty summary queue its fallback behind it.
		svc.queueAntigravityModelRefresh(ctx, auth, "antigravity")
		synctest.Wait()
		svc.queueAntigravitySummaryRefresh(ctx, auth)
		synctest.Wait()
		// Start a stalled summary retry while the fallback is waiting on the catalog worker.
		svc.refreshAntigravityModels(ctx)
		synctest.Wait()
		if summaryCalls.Load() != 2 || catalogCalls.Load() != 1 {
			t.Fatalf("summaryCalls=%d catalogCalls=%d, want 2 and 1", summaryCalls.Load(), catalogCalls.Load())
		}
		close(releaseCatalog)
		synctest.Wait()

		current, _ := manager.GetByID(auth.ID)
		if len(current.Quota.LookupWindows) != 1 || current.Quota.LookupWindows[0].UsedPercent == nil ||
			math.Abs(*current.Quota.LookupWindows[0].UsedPercent-25) > 1e-9 {
			t.Fatalf("fallback discarded by stalled summary retry: %+v", current.Quota.LookupWindows)
		}
		antigravityCapabilityMu.RLock()
		expires := antigravityCapabilityCache[svc.antigravityCapabilityKey(auth)].summaryExpiresAt
		antigravityCapabilityMu.RUnlock()
		if !expires.After(time.Now()) {
			t.Fatalf("fallback did not update summaryExpiresAt: %v", expires)
		}
	})
}

func TestAntigravityCanceledOnDemandRefreshDoesNotSuppressBackgroundFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		defer setAntigravityTestProbeSlots()()
		releaseCatalog := make(chan struct{})
		releaseSummary := make(chan struct{})
		oldTransport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == sdkauth.AntigravityQuotaSummaryPath {
				select {
				case <-releaseSummary:
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
				return antigravityVersionTestResponse(req, http.StatusNotFound, ""), nil
			}
			select {
			case <-releaseCatalog:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return antigravityVersionTestResponse(req, http.StatusOK,
				`{"models":{"gemini-3-flash":{"quotaInfo":{"remainingFraction":0.75,"resetTime":"2026-11-01T00:00:00Z"}}}}`), nil
		})
		defer func() { http.DefaultTransport = oldTransport }()
		manager := coreauth.NewManager(nil, nil, nil)
		svc := &Service{coreManager: manager}
		priorUsed := 90.0
		seed := antigravityTestAuth("canceled-on-demand", "https://quota.invalid")
		seed.Quota.LookedUpAt = time.Now().Add(-time.Hour)
		seed.Quota.LookupWindows = []coreauth.QuotaWindow{{Key: antigravitySummaryWindowPrefix + "gemini-5h", UsedPercent: &priorUsed}}
		auth, err := manager.Register(t.Context(), seed)
		if err != nil {
			t.Fatal(err)
		}
		defer GlobalModelRegistry().UnregisterClient(auth.ID)
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); svc.WaitAntigravityProbes() }()

		// Block the catalog worker, then start background and on-demand refreshes sharing the same empty summary revision.
		svc.queueAntigravityModelRefresh(ctx, auth, "antigravity")
		synctest.Wait()
		svc.queueAntigravitySummaryRefresh(ctx, auth)
		refreshCtx, cancelRefresh := context.WithCancel(ctx)
		refreshDone := make(chan error, 1)
		go func() { refreshDone <- svc.NewAntigravityUsageLookup().Refresh(refreshCtx, auth) }()
		synctest.Wait()
		close(releaseSummary)
		synctest.Wait()

		// Hold the per-auth mutation lock so the on-demand refresh fetches catalog fallback and blocks before publishing.
		lockHeld, releaseLock := make(chan struct{}), make(chan struct{})
		var unlockOnce sync.Once
		unlock := func() { unlockOnce.Do(func() { close(releaseLock) }) }
		modifyDone := make(chan struct{})
		go func() {
			defer close(modifyDone)
			_, _, _ = manager.Modify(t.Context(), auth.ID, 0, func(*coreauth.Auth) error {
				close(lockHeld)
				<-releaseLock
				return nil
			})
		}()
		defer func() {
			unlock()
			<-modifyDone
		}()
		<-lockHeld
		close(releaseCatalog)
		synctest.Wait()

		// Cancel the on-demand refresh before it can publish, then release the mutation lock for the background fallback.
		cancelRefresh()
		if errRefresh := <-refreshDone; !errors.Is(errRefresh, context.Canceled) {
			t.Fatalf("on-demand refresh err = %v, want context.Canceled", errRefresh)
		}
		unlock()
		<-modifyDone
		synctest.Wait()

		current, _ := manager.GetByID(auth.ID)
		if len(current.Quota.LookupWindows) != 1 || current.Quota.LookupWindows[0].UsedPercent == nil ||
			math.Abs(*current.Quota.LookupWindows[0].UsedPercent-25) > 1e-9 {
			t.Fatalf("canceled on-demand refresh suppressed background fallback: %+v", current.Quota.LookupWindows)
		}
		antigravityCapabilityMu.RLock()
		expires := antigravityCapabilityCache[svc.antigravityCapabilityKey(auth)].summaryExpiresAt
		antigravityCapabilityMu.RUnlock()
		if !expires.After(time.Now()) {
			t.Fatalf("background fallback did not renew summaryExpiresAt: %v", expires)
		}
	})
}

func TestAntigravityFallbackRenewsSummaryTTLWhenCatalogAlreadyPublishedQuota(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		defer setAntigravityTestProbeSlots()()
		releaseSummary, releaseCatalog := make(chan struct{}), make(chan struct{})
		var summaryOnce, catalogOnce sync.Once
		unblockSummary := func() { summaryOnce.Do(func() { close(releaseSummary) }) }
		unblockCatalog := func() { catalogOnce.Do(func() { close(releaseCatalog) }) }
		var summaryCalls, catalogCalls atomic.Int32
		oldTransport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == sdkauth.AntigravityQuotaSummaryPath {
				summaryCalls.Add(1)
				select {
				case <-releaseSummary:
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
				return antigravityVersionTestResponse(req, http.StatusNotFound, ""), nil
			}
			catalogCalls.Add(1)
			select {
			case <-releaseCatalog:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return antigravityVersionTestResponse(req, http.StatusOK,
				`{"models":{"gemini-3-flash":{"quotaInfo":{"remainingFraction":0.75,"resetTime":"2026-11-01T00:00:00Z"}}}}`), nil
		})
		defer func() { http.DefaultTransport = oldTransport }()
		manager := coreauth.NewManager(nil, nil, nil)
		svc := &Service{coreManager: manager}
		auth, err := manager.Register(t.Context(), antigravityTestAuth("fallback-reuses-catalog", "https://quota.invalid"))
		if err != nil {
			t.Fatal(err)
		}
		defer GlobalModelRegistry().UnregisterClient(auth.ID)
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); unblockSummary(); unblockCatalog(); svc.WaitAntigravityProbes() }()

		// Summary starts first (lower revision), catalog starts second (higher revision) and holds the catalog worker.
		svc.queueAntigravitySummaryRefresh(ctx, auth)
		synctest.Wait()
		svc.queueAntigravityModelRefresh(ctx, auth, "antigravity")
		synctest.Wait()
		// Empty summary queues fallback behind the running higher-revision catalog probe.
		unblockSummary()
		synctest.Wait()
		// Catalog publishes quota at the higher revision; fallback reuses it with recorded=false, err=nil.
		unblockCatalog()
		svc.WaitAntigravityProbes()

		antigravityCapabilityMu.RLock()
		entry := antigravityCapabilityCache[svc.antigravityCapabilityKey(auth)]
		antigravityCapabilityMu.RUnlock()
		if !entry.summaryExpiresAt.After(time.Now()) || entry.summaryFulfilledRevision == 0 {
			t.Fatalf("fallback did not renew summary TTL after catalog publish: expires=%v fulfilled=%d",
				entry.summaryExpiresAt, entry.summaryFulfilledRevision)
		}
		if summaryCalls.Load() != 1 || catalogCalls.Load() != 1 {
			t.Fatalf("initial probes = summary:%d catalog:%d, want 1 and 1", summaryCalls.Load(), catalogCalls.Load())
		}
		svc.refreshAntigravityModels(ctx)
		svc.WaitAntigravityProbes()
		if summaryCalls.Load() != 1 || catalogCalls.Load() != 1 {
			t.Fatalf("immediate rescan repeated probes: summary:%d catalog:%d", summaryCalls.Load(), catalogCalls.Load())
		}
	})
}

func setAntigravityTestProbeSlots() func() {
	catalog, summary := antigravityProbeSlots, antigravitySummaryProbeSlots
	antigravityProbeSlots, antigravitySummaryProbeSlots = make(chan struct{}, 1), make(chan struct{}, 1)
	return func() { antigravityProbeSlots, antigravitySummaryProbeSlots = catalog, summary }
}

func TestAntigravityUsageBackgroundQueuesCancel(t *testing.T) {
	for _, shutdown := range []bool{true, false} {
		t.Run(fmt.Sprintf("shutdown=%t", shutdown), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resetAntigravityCapabilityCache()
				defer resetAntigravityCapabilityCache()
				defer setAntigravityTestProbeSlots()()
				oldTransport := http.DefaultTransport
				var requests, cancellations atomic.Int32
				http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
					requests.Add(1)
					<-req.Context().Done()
					cancellations.Add(1)
					return nil, req.Context().Err()
				})
				defer func() { http.DefaultTransport = oldTransport }()
				lifetime, cancel := context.WithCancel(t.Context())
				manager := coreauth.NewManager(nil, nil, nil)
				svc := &Service{coreManager: manager, antigravityContext: lifetime, runCancel: cancel}
				defer func() { cancel(); svc.WaitAntigravityProbes() }()
				for _, id := range []string{"connected", "waiting-for-slots"} {
					auth, err := manager.Register(t.Context(), antigravityTestAuth(id, "https://quota.invalid"))
					if err != nil {
						t.Fatal(err)
					}
					defer GlobalModelRegistry().UnregisterClient(auth.ID)
					svc.asyncProbeAntigravityCapabilities(t.Context(), auth, "antigravity")
					synctest.Wait()
					svc.asyncProbeAntigravityCapabilities(t.Context(), auth, "antigravity")
				}
				synctest.Wait()
				svc.antigravityProbeMu.Lock()
				catalogQueued := svc.antigravityRefreshPending["waiting-for-slots"] != nil
				summaryQueued := svc.antigravitySummaryPending["waiting-for-slots"] != nil
				svc.antigravityProbeMu.Unlock()
				if connected := requests.Load(); connected != 2 || !catalogQueued || !summaryQueued {
					t.Fatalf("connected=%d catalog queued=%t summary queued=%t", connected, catalogQueued, summaryQueued)
				}
				if shutdown {
					if err := svc.Shutdown(t.Context()); err != nil {
						t.Fatal(err)
					}
				} else {
					manager.Remove(t.Context(), "waiting-for-slots")
					synctest.Wait()
					manager.Remove(t.Context(), "connected")
				}
				svc.WaitAntigravityProbes()
				svc.antigravityProbeMu.Lock()
				remaining := len(svc.antigravityRefreshPending) + len(svc.antigravitySummaryPending) + len(svc.antigravityAccountProbes)
				svc.antigravityProbeMu.Unlock()
				if connected, canceled := requests.Load(), cancellations.Load(); connected != 2 || canceled != connected || remaining != 0 || len(antigravityProbeSlots) != 0 || len(antigravitySummaryProbeSlots) != 0 {
					t.Fatalf("connected=%d canceled=%d remaining=%d catalog slots=%d summary slots=%d",
						connected, canceled, remaining, len(antigravityProbeSlots), len(antigravitySummaryProbeSlots))
				}
			})
		})
	}
}

func TestAntigravityUsageStaleFallbackDoesNotOverwriteNewerSummary(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	var mu sync.Mutex
	now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	oldClock := antigravityNowFunc
	antigravityNowFunc = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	t.Cleanup(func() { antigravityNowFunc = oldClock })
	var summaryCalls int
	catalogStarted, releaseCatalog := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case sdkauth.AntigravityQuotaSummaryPath:
			mu.Lock()
			summaryCalls++
			call := summaryCalls
			mu.Unlock()
			if call == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.8}]}`)
		case antigravityModelsPath:
			close(catalogStarted)
			<-releaseCatalog
			_, _ = io.WriteString(w, `{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.1}}}}`)
		default:
			t.Errorf("unexpected probe path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{coreManager: manager}
	auth, err := manager.Register(t.Context(), antigravityTestAuth("stale-fallback", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	lookup := svc.NewAntigravityUsageLookup()
	staleDone := make(chan error, 1)
	go func() { staleDone <- lookup.Refresh(t.Context(), auth) }()
	<-catalogStarted
	mu.Lock()
	now = now.Add(time.Second)
	mu.Unlock()
	if err := lookup.Refresh(t.Context(), auth); err != nil {
		close(releaseCatalog)
		<-staleDone
		t.Fatal(err)
	}
	mu.Lock()
	now = now.Add(time.Second)
	mu.Unlock()
	close(releaseCatalog)
	if err := <-staleDone; err != nil {
		t.Fatal(err)
	}
	current, _ := manager.GetByID(auth.ID)
	windows := current.Quota.LookupWindows
	if len(windows) != 1 || !hasAntigravitySummaryWindows(windows) || windows[0].UsedPercent == nil || math.Abs(*windows[0].UsedPercent-20) > 1e-9 {
		t.Fatalf("stale fallback overwrote newer summary: %+v", windows)
	}
}

func TestAntigravityBackgroundUsageFetchesCatalogFallbackWhenCacheWarmOrRouteChanged(t *testing.T) {
	t.Run("warm catalog cache", func(t *testing.T) {
		resetAntigravityCapabilityCache()
		t.Cleanup(resetAntigravityCapabilityCache)
		now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
		oldClock := antigravityNowFunc
		antigravityNowFunc = func() time.Time { return now }
		t.Cleanup(func() { antigravityNowFunc = oldClock })
		var catalogCalls int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case sdkauth.AntigravityQuotaSummaryPath:
				w.WriteHeader(http.StatusNotFound)
			case antigravityModelsPath:
				catalogCalls++
				_, _ = io.WriteString(w, `{"models":{"gemini-3.1-flash-lite":{"quotaInfo":{"remainingFraction":0.6}}}}`)
			default:
				t.Errorf("unexpected probe path %s", r.URL.Path)
			}
		}))
		defer server.Close()
		manager := coreauth.NewManager(nil, nil, nil)
		svc := &Service{coreManager: manager}
		auth, err := manager.Register(t.Context(), antigravityTestAuth("warm-cache-fallback", server.URL))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
		key := svc.antigravityCapabilityKey(auth)
		antigravityCapabilityMu.Lock()
		antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
			hints:     antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-3.1-flash-lite": {}}, revision: 1},
			expiresAt: now.Add(antigravityCapabilityCacheTTL),
			lastUsed:  now,
		}
		antigravityCapabilityMu.Unlock()
		svc.asyncProbeAntigravityCapabilities(t.Context(), auth, "antigravity")
		svc.WaitAntigravityProbes()
		current, _ := manager.GetByID(auth.ID)
		if catalogCalls != 1 || len(current.Quota.LookupWindows) != 1 || current.Quota.LookupWindows[0].Key != "ag:gemini" ||
			current.Quota.LookupWindows[0].UsedPercent == nil || math.Abs(*current.Quota.LookupWindows[0].UsedPercent-40) > 1e-9 {
			t.Fatalf("warm cache fallback calls=%d quota=%+v", catalogCalls, current.Quota)
		}
	})

	t.Run("route changed between catalog and summary", func(t *testing.T) {
		resetAntigravityCapabilityCache()
		t.Cleanup(resetAntigravityCapabilityCache)
		now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
		oldClock := antigravityNowFunc
		antigravityNowFunc = func() time.Time { return now }
		t.Cleanup(func() { antigravityNowFunc = oldClock })
		ua := "antigravity/hub/2.33.0 (old)"
		oldUA := antigravityModelUserAgent
		antigravityModelUserAgent = func() string { return ua }
		t.Cleanup(func() { antigravityModelUserAgent = oldUA })
		var catalogCalls int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case sdkauth.AntigravityQuotaSummaryPath:
				w.WriteHeader(http.StatusNotFound)
			case antigravityModelsPath:
				catalogCalls++
				if r.Header.Get("User-Agent") == "antigravity/hub/2.33.0 (old)" {
					_, _ = io.WriteString(w, `{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.1}}}}`)
					return
				}
				_, _ = io.WriteString(w, `{"models":{"gemini-pro":{"quotaInfo":{"remainingFraction":0.8}}}}`)
			default:
				t.Errorf("unexpected probe path %s", r.URL.Path)
			}
		}))
		defer server.Close()
		manager := coreauth.NewManager(nil, nil, nil)
		svc := &Service{coreManager: manager}
		auth, err := manager.Register(t.Context(), antigravityTestAuth("route-change-fallback", server.URL))
		if err != nil {
			t.Fatal(err)
		}
		svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
		ua = "antigravity/hub/2.34.0 (new)"
		now = now.Add(time.Second)
		svc.queueAntigravitySummaryRefresh(t.Context(), auth)
		svc.WaitAntigravityProbes()
		current, _ := manager.GetByID(auth.ID)
		if catalogCalls != 2 || len(current.Quota.LookupWindows) != 1 || current.Quota.LookupWindows[0].UsedPercent == nil ||
			math.Abs(*current.Quota.LookupWindows[0].UsedPercent-20) > 1e-9 {
			t.Fatalf("route change reused old catalog quota: calls=%d quota=%+v", catalogCalls, current.Quota)
		}
	})

	t.Run("catalog failure does not advance summary expiry", func(t *testing.T) {
		resetAntigravityCapabilityCache()
		t.Cleanup(resetAntigravityCapabilityCache)
		now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
		oldClock := antigravityNowFunc
		antigravityNowFunc = func() time.Time { return now }
		t.Cleanup(func() { antigravityNowFunc = oldClock })
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case sdkauth.AntigravityQuotaSummaryPath:
				w.WriteHeader(http.StatusNotFound)
			case antigravityModelsPath:
				w.WriteHeader(http.StatusServiceUnavailable)
			default:
				t.Errorf("unexpected probe path %s", r.URL.Path)
			}
		}))
		defer server.Close()
		manager := coreauth.NewManager(nil, nil, nil)
		svc := &Service{coreManager: manager}
		auth, err := manager.Register(t.Context(), antigravityTestAuth("catalog-503-empty-summary", server.URL))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
		svc.asyncProbeAntigravityCapabilities(t.Context(), auth, "antigravity")
		svc.WaitAntigravityProbes()
		key := svc.antigravityCapabilityKey(auth)
		antigravityCapabilityMu.RLock()
		summaryExpiry := antigravityCapabilityCache[key].summaryExpiresAt
		antigravityCapabilityMu.RUnlock()
		if !summaryExpiry.IsZero() {
			t.Fatalf("failed catalog advanced summaryExpiresAt to %v", summaryExpiry)
		}
	})
}

func TestAntigravityProbeBackoffScopes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failingPath   string
		status        int
		sharedBackoff bool
	}{
		{"catalog transient", antigravityModelsPath, http.StatusServiceUnavailable, false},
		{"summary transient", sdkauth.AntigravityQuotaSummaryPath, http.StatusServiceUnavailable, false},
		{"summary unauthorized", sdkauth.AntigravityQuotaSummaryPath, http.StatusUnauthorized, false},
		{"catalog unauthorized", antigravityModelsPath, http.StatusUnauthorized, false},
		{"catalog forbidden", antigravityModelsPath, http.StatusForbidden, false},
		{"summary forbidden", sdkauth.AntigravityQuotaSummaryPath, http.StatusForbidden, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			now := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
			oldClock := antigravityNowFunc
			antigravityNowFunc = func() time.Time { return now }
			t.Cleanup(func() { antigravityNowFunc = oldClock })
			var mu sync.Mutex
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				switch r.URL.Path {
				case tc.failingPath:
					w.WriteHeader(tc.status)
				case sdkauth.AntigravityQuotaSummaryPath:
					_, _ = io.WriteString(w, `{"buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.5}]}`)
				case antigravityModelsPath:
					_, _ = io.WriteString(w, `{"models":{"gemini-3.1-flash-lite":{}}}`)
				default:
					t.Errorf("unexpected probe path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, nil, nil)
			svc := &Service{coreManager: manager}
			auth, err := manager.Register(t.Context(), antigravityTestAuth("probe-backoff", server.URL))
			if err != nil {
				t.Fatal(err)
			}
			failingUsage := tc.failingPath == sdkauth.AntigravityQuotaSummaryPath
			if _, _, err := svc.fetchAntigravityModelHints(t.Context(), auth, failingUsage); err == nil {
				t.Fatal("expected first probe to fail")
			}
			if _, _, err := svc.fetchAntigravityModelHints(t.Context(), auth, !failingUsage); (err != nil) != tc.sharedBackoff {
				t.Fatalf("other endpoint error = %v, want shared backoff %t", err, tc.sharedBackoff)
			}
			if _, _, err := svc.fetchAntigravityModelHints(t.Context(), auth, failingUsage); err == nil || !strings.Contains(err.Error(), "in backoff") {
				t.Fatalf("failing endpoint retry = %v, want backoff", err)
			}
			want := []string{tc.failingPath}
			if !tc.sharedBackoff {
				otherPath := antigravityModelsPath
				if !failingUsage {
					otherPath = sdkauth.AntigravityQuotaSummaryPath
				}
				want = append(want, otherPath)
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(paths, want) {
				t.Fatalf("requests = %v, want %v", paths, want)
			}
		})
	}
}
