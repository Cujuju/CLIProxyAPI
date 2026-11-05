package helps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Transport observations belong to a usage reporter, never a pooled connection.
// Only immutable snapshots cross the transparent WS -> HTTP fallback boundary.
type transportObservation struct {
	mu                           sync.RWMutex
	downstream, upstream, reason string
}
type transportReporterKey struct{}
type transportFallbackKey struct{}
type transportSnapshot struct{ downstream, upstream, reason string }
type transportFallback struct {
	snapshot transportSnapshot
	claimed  atomic.Bool
}

func downstreamTransport(ctx context.Context) string {
	if core.DownstreamWebsocket(ctx) {
		return "websocket"
	}
	if previous := transportReporter(ctx); previous != nil {
		return previous.transportSnapshot().downstream
	}
	if ctx != nil {
		if c, ok := ctx.Value("gin").(*gin.Context); ok && c != nil && c.Request != nil {
			return "http"
		}
	}
	return ""
}

// WithTransportContext binds network observations to this execution attempt.
// A new reporter always replaces the binding, including on a reused session.
func (r *UsageReporter) WithTransportContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, transportReporterKey{}, r)
}

// TransportFallbackContext transfers an immutable failed-upgrade observation to
// the HTTP execution that owns the final record. Ordinary retries never inherit it.
func (r *UsageReporter) TransportFallbackContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, transportFallbackKey{}, &transportFallback{snapshot: r.transportSnapshot()})
}

// TransportFallbackClaimed reports whether a fallback executor has constructed
// the reporter that now owns this attempt, including failures before any HTTP call.
func TransportFallbackClaimed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	handoff, _ := ctx.Value(transportFallbackKey{}).(*transportFallback)
	return handoff != nil && handoff.claimed.Load()
}

func (r *UsageReporter) transportSnapshot() transportSnapshot {
	if r == nil {
		return transportSnapshot{}
	}
	r.transport.mu.RLock()
	defer r.transport.mu.RUnlock()
	return transportSnapshot{r.transport.downstream, r.transport.upstream, r.transport.reason}
}

// ObserveUpstreamTransport records an actual network attempt or use of an
// established socket. It does not infer transport from stream or executor type.
func (r *UsageReporter) ObserveUpstreamTransport(transport string) {
	if r == nil || (transport != "http" && transport != "websocket") {
		return
	}
	r.transport.mu.Lock()
	defer r.transport.mu.Unlock()
	if r.transport.upstream == "" {
		r.transport.upstream = transport
	} else if r.transport.upstream != transport {
		r.transport.upstream = "websocket+http"
	}
}

func transportReporter(ctx context.Context) *UsageReporter {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(transportReporterKey{}).(*UsageReporter)
	return r
}

// DoHTTP observes the actual HTTP call without changing its client, transport,
// headers, body, response, or cancellation semantics.
func DoHTTP(client *http.Client, req *http.Request) (*http.Response, error) {
	transportReporter(req.Context()).ObserveUpstreamTransport("http")
	return client.Do(req)
}

// ObserveWebsocketDial records an attempted upgrade and a bounded failure code.
// It never retains an error message, response body, URL, headers, or credentials.
func ObserveWebsocketDial(ctx context.Context, response *http.Response, err error) {
	r := transportReporter(ctx)
	if r == nil {
		return
	}
	r.ObserveUpstreamTransport("websocket")
	if err == nil {
		return
	}
	reason := "websocket_dial_failed"
	if response != nil && response.StatusCode > 0 {
		reason = fmt.Sprintf("websocket_upgrade_%d", response.StatusCode)
	} else if errors.Is(err, context.Canceled) {
		reason = "websocket_dial_canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		reason = "websocket_dial_timeout"
	}
	r.transport.mu.Lock()
	r.transport.reason = reason
	r.transport.mu.Unlock()
}
