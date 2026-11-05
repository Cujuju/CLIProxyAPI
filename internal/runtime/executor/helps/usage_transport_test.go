package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestTransportObservationsUnknownAndActualDownstream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		r := NewUsageReporter(usage.WithStream(context.Background(), stream), "codex", "gpt-test", nil)
		r.executorType = "CodexWebsocketsExecutor"
		if got := r.buildRecord(usage.Detail{}, false); got.DownstreamTransport != "" || got.UpstreamTransport != "" {
			t.Fatalf("inferred transport from stream/executor: %+v", got)
		}
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ctx := context.WithValue(context.Background(), "gin", c)
	for _, ws := range []bool{false, true} {
		requestCtx := ctx
		want := "http"
		if ws {
			requestCtx = core.WithDownstreamWebsocket(ctx)
			want = "websocket"
		}
		r := NewUsageReporter(requestCtx, "codex", "test", nil)
		if r.transportSnapshot().downstream != want {
			t.Fatalf("downstream = %+v", r.transportSnapshot())
		}
	}
}

type transportTestRoundTripper func(*http.Request) (*http.Response, error)

func (f transportTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportHTTPIsPayloadNeutralAndFallbackIsIsolated(t *testing.T) {
	root := context.Background()
	ws := NewUsageReporter(root, "codex", "test", nil)
	ctx := ws.WithTransportContext(root)
	ObserveWebsocketDial(ctx, &http.Response{StatusCode: 426}, errors.New("sensitive upstream detail"))
	fallback := NewUsageReporter(ws.TransportFallbackContext(ctx), "codex", "test", nil)
	fallbackCtx := fallback.WithTransportContext(ws.TransportFallbackContext(ctx))
	request, _ := http.NewRequestWithContext(fallbackCtx, "POST", "https://upstream.invalid/responses", strings.NewReader(`{"input":"keep unchanged"}`))
	request.Header.Set("X-Test", "preserved")
	client := &http.Client{Transport: transportTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"input":"keep unchanged"}` || r.Header.Get("X-Test") != "preserved" || r != request {
			t.Fatal("transport observation changed the request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("response")), Header: make(http.Header)}, nil
	})}
	resp, err := DoHTTP(client, request)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	got := fallback.buildRecord(usage.Detail{}, false)
	if got.UpstreamTransport != "websocket+http" || got.TransportFallbackReason != "websocket_upgrade_426" {
		t.Fatalf("fallback = %+v", got)
	}
	if ws.transportSnapshot().upstream != "websocket" {
		t.Fatal("fallback mutated the failed upgrade reporter")
	}
	// An ordinary retry/new turn on the same original request cannot inherit observations.
	next := NewUsageReporter(ctx, "codex", "test", nil)
	if next.transportSnapshot().upstream != "" || next.transportSnapshot().reason != "" {
		t.Fatal("stale transport on next attempt")
	}
	ObserveWebsocketDial(next.WithTransportContext(root), nil, context.Canceled)
	if next.transportSnapshot().reason != "websocket_dial_canceled" {
		t.Fatal("canceled handshake lost")
	}
}

func TestTransportObservationsConcurrentAndSnapshots(t *testing.T) {
	r := NewUsageReporter(context.Background(), "codex", "test", nil)
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			r.ObserveUpstreamTransport("http")
			r.ObserveUpstreamTransport("websocket")
			_ = r.buildRecord(usage.Detail{}, true)
		}()
	}
	group.Wait()
	got := r.buildRecord(usage.Detail{}, true)
	r.ObserveUpstreamTransport("unsupported")
	if got.UpstreamTransport != "websocket+http" || got.TransportFallbackReason != "" {
		t.Fatalf("snapshot = %+v", got)
	}
}
