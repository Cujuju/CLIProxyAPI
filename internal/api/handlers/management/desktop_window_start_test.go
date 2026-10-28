package management

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// windowStartExecutor records the request it executes, or fails with err.
type windowStartExecutor struct {
	refreshRecordExecutor
	err      error
	authID   string
	model    string
	format   sdktranslator.Format
	payload  []byte
	provider string
}

func (e *windowStartExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.authID, e.model, e.format, e.payload = auth.ID, req.Model, opts.SourceFormat, req.Payload
	e.provider = coreusage.AccessProviderFromContext(ctx)
	return cliproxyexecutor.Response{Payload: []byte(`{}`)}, e.err
}

// windowStartTestModel is registered for the idle login only.
const windowStartTestModel = "window-start-test-model"

type statusError struct{ code int }

func (e statusError) Error() string   { return "upstream refused" }
func (e statusError) StatusCode() int { return e.code }

func newWindowStartHandler(t *testing.T, execErr error) (*Handler, *windowStartExecutor) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(nil, nil, nil)
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager.SetConfig(cfg)
	exec := &windowStartExecutor{refreshRecordExecutor: refreshRecordExecutor{provider: "claude"}, err: execErr}
	manager.RegisterExecutor(exec)
	now := time.Now()
	for _, auth := range []*coreauth.Auth{
		{ID: "idle", Index: "idx-idle", Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"access_token": "token"}},
		{ID: "busy", Index: "idx-busy", Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"access_token": "token"},
			Quota: coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{
				"anthropic-ratelimit-unified-5h-utilization": "0.1",
				"anthropic-ratelimit-unified-5h-reset":       strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
			}}},
		{ID: "key", Index: "idx-key", Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{"runtime_only": "true", "api_key": "sk-ant-test"}},
	} {
		registerAuthForLookupTest(t, manager, auth)
	}
	registry.GetGlobalRegistry().RegisterClient("idle", "claude", []*registry.ModelInfo{{ID: windowStartTestModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("idle") })
	return NewHandlerWithoutConfigFilePath(cfg, manager), exec
}

func TestStartUsageWindowValidation(t *testing.T) {
	h, exec := newWindowStartHandler(t, nil)
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"no body":        {``, http.StatusBadRequest},
		"no model":       {`{"auth_index":"idx-idle"}`, http.StatusBadRequest},
		"unknown index":  {`{"auth_index":"nope","model":"m"}`, http.StatusNotFound},
		"api key":        {`{"auth_index":"idx-key","model":"m"}`, http.StatusBadRequest},
		"window running": {`{"auth_index":"idx-busy","model":"m"}`, http.StatusConflict},
	} {
		rec := serveDesktop(t, h.StartUsageWindow, http.MethodPost, tc.body, "127.0.0.1:1")
		if rec.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d: %s", name, rec.Code, tc.want, rec.Body.String())
		}
	}
	if exec.authID != "" {
		t.Fatalf("a rejected request reached the executor (%q)", exec.authID)
	}
}

func TestStartUsageWindowSendsPinnedMinimalRequest(t *testing.T) {
	h, exec := newWindowStartHandler(t, nil)
	rec := serveDesktop(t, h.StartUsageWindow, http.MethodPost, `{"auth_index":"idx-idle","model":"`+windowStartTestModel+`"}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if exec.authID != "idle" || exec.model != windowStartTestModel || exec.format != sdktranslator.FormatClaude {
		t.Fatalf("executed %q/%q/%q, want idle/test model/claude", exec.authID, exec.model, exec.format)
	}
	var sent struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	if errDecode := json.Unmarshal(exec.payload, &sent); errDecode != nil || sent.Model != windowStartTestModel || sent.MaxTokens != 1 {
		t.Fatalf("payload = %s (%v)", exec.payload, errDecode)
	}
	if exec.provider != WindowStartAccessProvider {
		t.Fatalf("access provider = %q, want %q", exec.provider, WindowStartAccessProvider)
	}
	var body struct {
		Usage struct {
			Credentials []json.RawMessage `json:"credentials"`
		} `json:"usage"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil || len(body.Usage.Credentials) == 0 {
		t.Fatalf("response lacks usage payload: %s", rec.Body.String())
	}
}

func TestStartUsageWindowReportsUpstreamFailure(t *testing.T) {
	h, _ := newWindowStartHandler(t, statusError{code: http.StatusTooManyRequests})
	rec := serveDesktop(t, h.StartUsageWindow, http.MethodPost, `{"auth_index":"idx-idle","model":"`+windowStartTestModel+`"}`, "127.0.0.1:1")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		StatusCode int `json:"status_code"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil || body.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestDesktopEventMarksWindowStart(t *testing.T) {
	if got := desktopEventFromRecord(coreusage.Record{AccessProvider: WindowStartAccessProvider}).Purpose; got != WindowStartAccessProvider {
		t.Fatalf("purpose = %q", got)
	}
	if got := desktopEventFromRecord(coreusage.Record{AccessProvider: "config-inline"}).Purpose; got != "" {
		t.Fatalf("client traffic purpose = %q, want empty", got)
	}
}
