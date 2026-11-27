package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// antigravityDirectAPI turns on Antigravity's direct Cloud Code API for tests of that mode.
func antigravityDirectAPI(cfg *config.Config) *config.Config {
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.Antigravity.DirectAPI = true
	return cfg
}

// quotaOnlyTestSetup isolates the CLI schedule and replaces the CLI with fetch.
func quotaOnlyTestSetup(t *testing.T, fetch func(context.Context) ([]sdkauth.AntigravityQuotaBucket, error)) {
	t.Helper()
	resetAntigravityCapabilityCache()
	oldFetcher := antigravityCLIQuotaFetcher
	antigravityCLIQuotaFetcher = fetch
	antigravityCLIQuota = antigravityCLIQuotaSchedule{}
	t.Cleanup(func() {
		antigravityCLIQuotaFetcher = oldFetcher
		antigravityCLIQuota = antigravityCLIQuotaSchedule{}
		resetAntigravityCapabilityCache()
	})
}

// googleTrap fails the test on any request: quota-only mode must never reach Cloud Code.
func googleTrap(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		t.Errorf("quota-only mode called the Cloud Code API: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func cliBuckets() []sdkauth.AntigravityQuotaBucket {
	return []sdkauth.AntigravityQuotaBucket{
		{ID: "gemini-5h", Window: sdkauth.AntigravityQuotaWindowFiveHour, RemainingFraction: 0.75, ResetTime: time.Date(2026, 11, 27, 20, 0, 0, 0, time.UTC), Description: "used"},
		{ID: "3p-weekly", Window: sdkauth.AntigravityQuotaWindowWeekly, RemainingFraction: 0.5, ResetTime: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
	}
}

func TestAntigravityQuotaOnlyIsTheDefault(t *testing.T) {
	if (&config.Config{}).AntigravityDirectAPIEnabled() || (*config.Config)(nil).AntigravityDirectAPIEnabled() {
		t.Fatal("direct API must default to off")
	}
}

func TestAntigravityQuotaOnlyRegistersNoModelsAndNeverProbes(t *testing.T) {
	quotaOnlyTestSetup(t, func(context.Context) ([]sdkauth.AntigravityQuotaBucket, error) {
		return nil, errors.New("not called")
	})
	server, hits := googleTrap(t)
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	t.Cleanup(svc.WaitAntigravityProbes)
	auth, err := svc.coreManager.Register(t.Context(), antigravityTestAuth("quota-only-models", server.URL))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	svc.registerModelsForAuth(t.Context(), auth)
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()

	if models := GlobalModelRegistry().GetModelsForClient(auth.ID); len(models) != 0 {
		t.Fatalf("quota-only credential serves %d models, want 0", len(models))
	}
	if hits.Load() != 0 {
		t.Fatalf("Cloud Code requests = %d, want 0", hits.Load())
	}
}

func TestAntigravityQuotaOnlyRetiresExecutor(t *testing.T) {
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := &coreauth.Auth{ID: "executor", Provider: "antigravity"}

	svc.registerExecutorForAuth(auth, true)
	if _, ok := svc.coreManager.Executor("antigravity"); ok {
		t.Fatal("quota-only mode registered the Antigravity executor")
	}

	svc.cfg = antigravityDirectAPI(nil)
	svc.registerExecutorForAuth(auth, true)
	if _, ok := svc.coreManager.Executor("antigravity"); !ok {
		t.Fatal("direct mode must register the Antigravity executor")
	}

	svc.cfg = &config.Config{}
	svc.registerExecutorForAuth(auth, true)
	if _, ok := svc.coreManager.Executor("antigravity"); ok {
		t.Fatal("switching direct API off must retire the Antigravity executor")
	}
	if _, owned := svc.ownedExecutors["antigravity"]; owned {
		t.Fatal("retired executor still tracked as owned")
	}

	// A reload whose only Antigravity credential is disabled never reaches the per-auth switch.
	svc.cfg = antigravityDirectAPI(nil)
	svc.registerExecutorForAuth(auth, true)
	svc.cfg = &config.Config{}
	disabled := &coreauth.Auth{ID: "executor", Provider: "antigravity", Disabled: true}
	svc.registerAvailableExecutors(t.Context(), executorRegistrationOptions{forceReplaceAuths: true, auths: []*coreauth.Auth{disabled}})
	if _, ok := svc.coreManager.Executor("antigravity"); ok {
		t.Fatal("a reload with only disabled credentials kept the Antigravity executor")
	}
}

func TestAntigravityQuotaOnlyUsageLookupReadsCLI(t *testing.T) {
	var cliCalls atomic.Int32
	quotaOnlyTestSetup(t, func(context.Context) ([]sdkauth.AntigravityQuotaBucket, error) {
		cliCalls.Add(1)
		return cliBuckets(), nil
	})
	server, hits := googleTrap(t)
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth := antigravityTestAuth("quota-only-usage", server.URL)
	// An expired login keeps its refresh token; quota-only mode never refreshes it.
	delete(auth.Metadata, "access_token")
	auth.Metadata["refresh_token"] = "refresh-quota-only-usage"
	auth, err := svc.coreManager.Register(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	lookup := svc.NewAntigravityUsageLookup()
	if !lookup.Eligible(auth) {
		t.Fatal("quota-only mode must not require the credential's access token")
	}
	if lookup.NeedsAccessToken(auth) {
		t.Fatal("quota-only mode must not renew the credential's access token before a lookup")
	}

	if err := lookup.Refresh(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	current, _ := svc.coreManager.GetByID(auth.ID)
	keys := map[string]float64{}
	for _, window := range current.Quota.LookupWindows {
		keys[window.Key] = *window.UsedPercent
	}
	if len(keys) != 2 || keys["ag_summary:gemini-5h"] != 25 || keys["ag_summary:3p-weekly"] != 50 {
		t.Fatalf("recorded windows = %v, want CLI buckets", keys)
	}
	if cliCalls.Load() != 1 || hits.Load() != 0 {
		t.Fatalf("cli calls = %d, Cloud Code requests = %d; want 1 and 0", cliCalls.Load(), hits.Load())
	}
}

func TestAntigravityQuotaOnlyRefusesAmbiguousAccount(t *testing.T) {
	var cliCalls atomic.Int32
	quotaOnlyTestSetup(t, func(context.Context) ([]sdkauth.AntigravityQuotaBucket, error) {
		cliCalls.Add(1)
		return cliBuckets(), nil
	})
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	first, _ := svc.coreManager.Register(t.Context(), antigravityTestAuth("account-a", "https://unused.invalid"))
	second, _ := svc.coreManager.Register(t.Context(), antigravityTestAuth("account-b", "https://unused.invalid"))

	if err := svc.NewAntigravityUsageLookup().Refresh(t.Context(), first); !errors.Is(err, errAntigravityCLIQuotaAmbiguous) {
		t.Fatalf("Refresh error = %v, want ambiguous account", err)
	}
	failures := svc.NewAntigravityUsageLookup().RefreshAll(t.Context())
	if len(failures) != 2 || failures[first.ID] == nil || failures[second.ID] == nil {
		t.Fatalf("RefreshAll failures = %v, want both credentials", failures)
	}
	if cliCalls.Load() != 0 {
		t.Fatalf("cli calls = %d, want 0 when the account is ambiguous", cliCalls.Load())
	}
}

func TestAntigravityQuotaOnlyBackgroundSchedule(t *testing.T) {
	now := time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)
	oldClock := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return now }
	t.Cleanup(func() { antigravityNowFunc = oldClock })
	var cliCalls atomic.Int32
	fail := true
	quotaOnlyTestSetup(t, func(context.Context) ([]sdkauth.AntigravityQuotaBucket, error) {
		cliCalls.Add(1)
		if fail {
			return nil, errors.New("cli failed")
		}
		return cliBuckets(), nil
	})
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}

	// No credential: nothing runs, and the loop waits a full scan instead of spinning.
	svc.refreshAntigravityModels(t.Context())
	if cliCalls.Load() != 0 || svc.nextAntigravityModelRefreshDelay() != antigravityRefreshScanInterval {
		t.Fatalf("cli calls = %d, delay = %s", cliCalls.Load(), svc.nextAntigravityModelRefreshDelay())
	}

	if _, err := svc.coreManager.Register(t.Context(), antigravityTestAuth("background", "https://unused.invalid")); err != nil {
		t.Fatal(err)
	}
	svc.refreshAntigravityModels(t.Context())
	retry := antigravityCLIQuota.next()
	if cliCalls.Load() != 1 || !retry.After(now) || retry.After(now.Add(antigravityCapabilityRetryMax)) {
		t.Fatalf("after failure: cli calls = %d, retry at %s", cliCalls.Load(), retry)
	}
	svc.refreshAntigravityModels(t.Context())
	if cliCalls.Load() != 1 {
		t.Fatal("a failed run must back off before retrying")
	}

	now = retry
	fail = false
	svc.refreshAntigravityModels(t.Context())
	if cliCalls.Load() != 2 || !antigravityCLIQuota.next().Equal(now.Add(antigravityCapabilityCacheTTL)) {
		t.Fatalf("after success: cli calls = %d, next at %s", cliCalls.Load(), antigravityCLIQuota.next())
	}
}

func TestAntigravityQuotaOnlyLookupErrorNamesCLI(t *testing.T) {
	quotaOnlyTestSetup(t, func(context.Context) ([]sdkauth.AntigravityQuotaBucket, error) {
		return nil, errors.New("antigravity cli not found")
	})
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth, _ := svc.coreManager.Register(t.Context(), antigravityTestAuth("missing-cli", "https://unused.invalid"))
	err := svc.NewAntigravityUsageLookup().Refresh(t.Context(), auth)
	if err == nil || !strings.Contains(err.Error(), "antigravity cli") {
		t.Fatalf("Refresh error = %v, want the CLI failure", err)
	}
}
