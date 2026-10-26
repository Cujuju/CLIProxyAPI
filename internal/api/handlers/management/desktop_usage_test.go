package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type desktopUsageResponse struct {
	Strategy    string `json:"strategy"`
	Credentials []struct {
		ID            string     `json:"id"`
		AuthIndex     string     `json:"auth_index"`
		Name          string     `json:"name"`
		Provider      string     `json:"provider"`
		Note          string     `json:"note"`
		Disabled      bool       `json:"disabled"`
		NextPick      bool       `json:"next_pick"`
		ObservedAt    *time.Time `json:"observed_at"`
		CooldownUntil *time.Time `json:"cooldown_until"`
		Windows       []struct {
			Key         string     `json:"key"`
			Label       string     `json:"label"`
			UsedPercent *float64   `json:"used_percent"`
			ResetsAt    *time.Time `json:"resets_at"`
			Status      *string    `json:"status"`
			Duration    *int64     `json:"duration_seconds"`
			Scoped      bool       `json:"scoped"`
		} `json:"windows"`
	} `json:"credentials"`
	Errors map[string]string `json:"errors"`
}

func claudeUsageSignals(weekly, fiveHour time.Time) map[string]string {
	return map[string]string{
		"anthropic-ratelimit-unified-7d-utilization": "0.4",
		"anthropic-ratelimit-unified-7d-reset":       strconv.FormatInt(weekly.Unix(), 10),
		"anthropic-ratelimit-unified-7d-status":      "allowed",
		"anthropic-ratelimit-unified-5h-utilization": "0.1",
		"anthropic-ratelimit-unified-5h-reset":       strconv.FormatInt(fiveHour.Unix(), 10),
	}
}

func newDesktopUsageHandler(t *testing.T, strategy string) *Handler {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	now := time.Now()
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	cfg.Routing.Strategy = strategy
	manager.SetConfig(cfg)
	for _, auth := range []*coreauth.Auth{
		{ID: "late", Index: "idx-late", Provider: "claude", Attributes: map[string]string{"runtime_only": "true", "note": "Work"},
			Quota: coreauth.QuotaState{ObservedAt: now, Signals: claudeUsageSignals(now.Add(72*time.Hour), now.Add(3*time.Hour))}},
		{ID: "soon", Index: "idx-soon", Provider: "claude", Attributes: map[string]string{"runtime_only": "true"},
			Quota: coreauth.QuotaState{ObservedAt: now, Signals: claudeUsageSignals(now.Add(24*time.Hour), now.Add(2*time.Hour))}},
		{ID: "codex-a", Index: "idx-codex", Provider: "codex", Attributes: map[string]string{"runtime_only": "true"}},
	} {
		registerAuthForLookupTest(t, manager, auth)
	}
	return NewHandlerWithoutConfigFilePath(cfg, manager)
}

func serveDesktop(t *testing.T, handler gin.HandlerFunc, method, body, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, "/v8/management/usage", strings.NewReader(body))
	ctx.Request.RemoteAddr = remoteAddr
	handler(ctx)
	return rec
}

func decodeDesktopUsage(t *testing.T, rec *httptest.ResponseRecorder) desktopUsageResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload desktopUsageResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatal(errDecode)
	}
	return payload
}

func TestGetUsageWindowsShapeAndNextPick(t *testing.T) {
	h := newDesktopUsageHandler(t, "sr")
	payload := decodeDesktopUsage(t, serveDesktop(t, h.GetUsageWindows, http.MethodGet, "", "127.0.0.1:1"))
	if payload.Strategy != coreauth.SoonestResetStrategy {
		t.Fatalf("strategy = %q", payload.Strategy)
	}
	if len(payload.Credentials) != 3 {
		t.Fatalf("credentials = %+v", payload.Credentials)
	}
	picks := map[string]bool{}
	for _, credential := range payload.Credentials {
		picks[credential.ID] = credential.NextPick
		if credential.Name != credential.ID || credential.CooldownUntil != nil {
			t.Fatalf("unexpected identity/cooldown: %+v", credential)
		}
		switch credential.ID {
		case "late":
			if credential.Note != "Work" {
				t.Fatalf("note = %q", credential.Note)
			}
		case "soon":
			if len(credential.Windows) != 2 || credential.ObservedAt == nil {
				t.Fatalf("windows = %+v", credential.Windows)
			}
			fiveHour, weekly := credential.Windows[0], credential.Windows[1]
			if fiveHour.Key != "five_hour" || weekly.Key != "seven_day" || weekly.Label != "Weekly" {
				t.Fatalf("window order/keys = %+v", credential.Windows)
			}
			if weekly.UsedPercent == nil || *weekly.UsedPercent != 40 || weekly.ResetsAt == nil || weekly.Status == nil || *weekly.Status != "allowed" {
				t.Fatalf("weekly = %+v", weekly)
			}
			if fiveHour.Duration == nil || *fiveHour.Duration != 5*3600 || weekly.Duration == nil || *weekly.Duration != 7*24*3600 || fiveHour.Scoped || weekly.Scoped {
				t.Fatalf("duration/scope = %+v", credential.Windows)
			}
			if fiveHour.Status != nil {
				t.Fatalf("missing status should be null: %+v", fiveHour)
			}
		case "codex-a":
			if credential.ObservedAt != nil || len(credential.Windows) != 0 {
				t.Fatalf("codex = %+v", credential)
			}
		}
	}
	if !picks["soon"] || picks["late"] {
		t.Fatalf("next_pick = %v", picks)
	}
	if !picks["codex-a"] {
		t.Fatalf("sole available codex credential should be next pick: %v", picks)
	}
}

func TestGetUsageWindowsRotatingStrategyHasNoNextPick(t *testing.T) {
	h := newDesktopUsageHandler(t, "round-robin")
	payload := decodeDesktopUsage(t, serveDesktop(t, h.GetUsageWindows, http.MethodGet, "", "127.0.0.1:1"))
	for _, credential := range payload.Credentials {
		if credential.NextPick {
			t.Fatalf("round-robin has no deterministic pick: %+v", credential)
		}
	}
}

func TestRefreshUsageWindowsValidation(t *testing.T) {
	h := newDesktopUsageHandler(t, "soonest-reset")
	if rec := serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{"auth_index":"missing"}`, "127.0.0.1:1"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown index status = %d", rec.Code)
	}
	if rec := serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{"auth_index":"idx-codex"}`, "127.0.0.1:1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-Claude status = %d", rec.Code)
	}
	if rec := serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, `{`, "127.0.0.1:1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", rec.Code)
	}
	// No credential carries an access token, so refresh-all makes no lookups.
	payload := decodeDesktopUsage(t, serveDesktop(t, h.RefreshUsageWindows, http.MethodPost, "", "127.0.0.1:1"))
	if len(payload.Credentials) != 3 || len(payload.Errors) != 0 {
		t.Fatalf("refresh-all payload = %+v", payload)
	}
}

func TestShutdownServerLoopbackOnly(t *testing.T) {
	calls := 0
	original := requestServerShutdown
	requestServerShutdown = func() { calls++ }
	t.Cleanup(func() { requestServerShutdown = original })
	h := newDesktopUsageHandler(t, "")

	if rec := serveDesktop(t, h.ShutdownServer, http.MethodPost, "", "203.0.113.7:4000"); rec.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("remote: status=%d calls=%d", rec.Code, calls)
	}
	for _, addr := range []string{"127.0.0.1:4000", "[::1]:4000"} {
		if rec := serveDesktop(t, h.ShutdownServer, http.MethodPost, "", addr); rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status=%d", addr, rec.Code)
		}
	}
	if calls != 2 {
		t.Fatalf("shutdown calls = %d", calls)
	}
}
