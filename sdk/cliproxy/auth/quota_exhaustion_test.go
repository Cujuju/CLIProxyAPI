package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const exhaustionTestModel = "claude-test-exhaustion"

func registerExhaustionAuth(t *testing.T, auth *Auth) *Auth {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: exhaustionTestModel}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	return auth
}

func claudeWindowAuth(t *testing.T, id, status string, resetsAt time.Time) *Auth {
	return registerExhaustionAuth(t, &Auth{ID: id, Provider: "claude", Status: StatusActive, Quota: QuotaState{Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Status":      status,
		"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0",
		"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(resetsAt.Unix(), 10),
	}}})
}

func cooldownAuth(t *testing.T, id, body string, recoverAt time.Time) *Auth {
	return registerExhaustionAuth(t, &Auth{ID: id, Provider: "claude", Status: StatusActive,
		Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
		LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: body},
	})
}

func TestPoolQuotaLimit_AllSpentWindows(t *testing.T) {
	now := time.Now()
	soon, later := now.Add(2*time.Hour).Truncate(time.Second), now.Add(5*time.Hour).Truncate(time.Second)
	auths := []*Auth{claudeWindowAuth(t, "ex-a", "rejected", later), claudeWindowAuth(t, "ex-b", "rejected", soon)}
	got, ok := poolQuotaLimit(auths, []string{"claude"}, exhaustionTestModel, now)
	if !ok || !got.ResetsAt.Equal(soon) || got.Window != fiveHourWindow {
		t.Fatalf("got %+v %v, want earliest reset %v in a five-hour window", got, ok, soon)
	}
}

func TestPoolQuotaLimit_ExplicitErrorCode(t *testing.T) {
	now := time.Now()
	recoverAt := now.Add(3 * time.Hour)
	auths := []*Auth{cooldownAuth(t, "ex-code", `429: {"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`, recoverAt)}
	if got, ok := poolQuotaLimit(auths, []string{"claude"}, exhaustionTestModel, now); !ok || !got.ResetsAt.Equal(recoverAt) || got.Window != 0 {
		t.Fatalf("got %v %v, want %v", got, ok, recoverAt)
	}
}

func TestPoolQuotaLimit_MixedPoolStaysRetryable(t *testing.T) {
	now := time.Now()
	auths := []*Auth{
		claudeWindowAuth(t, "ex-spent", "rejected", now.Add(time.Hour)),
		// A plain rate limit: 429 without a quota code.
		cooldownAuth(t, "ex-rate", `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, now.Add(time.Minute)),
	}
	if _, ok := poolQuotaLimit(auths, []string{"claude"}, exhaustionTestModel, now); ok {
		t.Fatal("mixed quota/rate-limit pool classified as a final usage limit")
	}
}

func TestPoolQuotaLimit_FreeTextIsNotEvidence(t *testing.T) {
	now := time.Now()
	auths := []*Auth{cooldownAuth(t, "ex-text", "quota exceeded, usage_limit_reached maybe", now.Add(time.Hour))}
	if _, ok := poolQuotaLimit(auths, []string{"claude"}, exhaustionTestModel, now); ok {
		t.Fatal("free text classified as quota evidence")
	}
}

func TestModelCooldownError_UsageLimitContract(t *testing.T) {
	resetsAt := time.Unix(1_900_000_000, 0)
	err := newModelCooldownError("m", "claude", 5*time.Hour)
	if err.Headers().Get("Retry-After") == "" || err.Headers().Get(HeaderShouldRetry) != "" {
		t.Fatal("ordinary cooldown must keep Retry-After and omit X-Should-Retry")
	}
	err.usageLimit = quotaLimit{ResetsAt: resetsAt}
	headers := err.Headers()
	if headers.Get("Retry-After") != "" || headers.Get(HeaderShouldRetry) != "false" {
		t.Fatalf("usage limit headers = %v, want no Retry-After and X-Should-Retry: false", headers)
	}
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", err.StatusCode())
	}
	var body struct {
		Error struct {
			Type      string `json:"type"`
			Code      string `json:"code"`
			ResetsAt  int64  `json:"resets_at"`
			Retryable *bool  `json:"retryable"`
		} `json:"error"`
	}
	if errJSON := json.Unmarshal([]byte(err.Error()), &body); errJSON != nil {
		t.Fatalf("body: %v", errJSON)
	}
	if body.Error.Type != usageLimitErrorType || body.Error.Code != "model_cooldown" || body.Error.ResetsAt != resetsAt.Unix() || body.Error.Retryable == nil || *body.Error.Retryable {
		t.Fatalf("body = %+v", body.Error)
	}
	dst := http.Header{}
	CopySafeErrorHeaders(dst, err)
	if dst.Get(HeaderShouldRetry) != "false" {
		t.Fatalf("CopySafeErrorHeaders dropped %s: %v", HeaderShouldRetry, dst)
	}
}

// newExhaustingManager serves two codex credentials that answer every request with a quota 429.
func newExhaustingManager(t *testing.T, reset time.Duration) *Manager {
	t.Helper()
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 2)
	registerOverloadAuths(t, m, 2)
	quotaErr := func() error {
		return customStatusError{
			code:       http.StatusTooManyRequests,
			msg:        `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
			retryAfter: &reset,
		}
	}
	m.RegisterExecutor(&issue6865Executor{
		id: "codex",
		executeFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, quotaErr()
		},
		streamFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			return nil, quotaErr()
		},
	})
	return m
}

func assertUsageLimit(t *testing.T, err error, reset time.Duration) {
	t.Helper()
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) || !cooldown.IsUsageLimit() {
		t.Fatalf("err = %v, want a final usage limit", err)
	}
	if until := time.Until(cooldown.UsageLimitResetsAt()); until < reset-time.Minute || until > reset {
		t.Fatalf("resets in %v, want about %v", until, reset)
	}
}

// The request that spends the last credential answers like every later one.
func TestExecute_ExhaustedPoolReturnsUsageLimit(t *testing.T) {
	reset := 3 * time.Hour
	m := newExhaustingManager(t, reset)
	req := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
	for i := 0; i < 2; i++ {
		_, err := m.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
		assertUsageLimit(t, err, reset)
	}
}

func TestExecuteStream_ExhaustingRequestReturnsUsageLimit(t *testing.T) {
	reset := 3 * time.Hour
	m := newExhaustingManager(t, reset)
	result, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if result != nil {
		t.Fatalf("result = %+v, want no stream", result)
	}
	assertUsageLimit(t, err, reset)
}

// credentialLimitAuth is a Claude credential benched by a unified 5h rejection until recoverAt.
func credentialLimitAuth(t *testing.T, id string, signals map[string]string, recoverAt time.Time) *Auth {
	return registerExhaustionAuth(t, &Auth{ID: id, Provider: "claude", Status: StatusActive, Quota: QuotaState{
		Exceeded: true, Reason: credentialQuotaReason, NextRecoverAt: recoverAt, Signals: signals,
	}})
}

// After the window resets, the cooldown's random grace still benches the credential; the pool
// must keep answering as a usage limit that resets when the grace ends (issue #62).
func TestCredentialQuotaLimit_CooldownGraceAfterWindowReset(t *testing.T) {
	now := time.Now()
	recoverAt := now.Add(20 * time.Second)
	cases := []struct {
		name       string
		signals    map[string]string
		wantWindow time.Duration
	}{
		{"spent window already reset", map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Status": "rejected",
			"Anthropic-Ratelimit-Unified-5h-Reset":  strconv.FormatInt(now.Add(-10*time.Second).Unix(), 10),
		}, fiveHourWindow},
		{"lookup already reports the new window", map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
			"Anthropic-Ratelimit-Unified-5h-Utilization": "0",
		}, 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := credentialLimitAuth(t, "grace-"+strconv.Itoa(i), tc.signals, recoverAt)
			got, ok := credentialQuotaLimit(auth, exhaustionTestModel, now)
			if !ok || !got.ResetsAt.Equal(recoverAt) || got.Window != tc.wantWindow {
				t.Fatalf("got %+v %v, want reset %v window %v", got, ok, recoverAt, tc.wantWindow)
			}
		})
	}
}

// A live spent window resets before its cooldown ends; the later instant is the real reset.
func TestCredentialQuotaLimit_ReportsCooldownEndPastWindowReset(t *testing.T) {
	now := time.Now()
	windowReset := now.Add(time.Hour).Truncate(time.Second)
	recoverAt := windowReset.Add(25 * time.Second)
	auth := credentialLimitAuth(t, "grace-live", map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Status": "rejected",
		"Anthropic-Ratelimit-Unified-5h-Reset":  strconv.FormatInt(windowReset.Unix(), 10),
	}, recoverAt)
	if got, ok := credentialQuotaLimit(auth, exhaustionTestModel, now); !ok || !got.ResetsAt.Equal(recoverAt) || got.Window != fiveHourWindow {
		t.Fatalf("got %+v %v, want reset %v in a five-hour window", got, ok, recoverAt)
	}
}

// A model-level rate-limit cooldown is not a usage limit, however long it runs.
func TestCredentialQuotaLimit_ModelCooldownIsNotEvidence(t *testing.T) {
	auth := cooldownAuth(t, "grace-rate", `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, time.Now().Add(time.Minute))
	if _, ok := credentialQuotaLimit(auth, exhaustionTestModel, time.Now()); ok {
		t.Fatal("rate-limit cooldown classified as a usage limit")
	}
}
