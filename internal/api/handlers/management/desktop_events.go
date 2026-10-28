package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Desktop event stream: GET /v8/management/usage/events pushes each upstream attempt as an SSE
// "request" event and each recorded usage lookup as "usage", so the desktop refreshes usage only when it changed.

const (
	desktopEventsPluginName = "desktop-events"
	// desktopEventBuffer absorbs a burst of parallel requests; a slower reader gets "resync".
	desktopEventBuffer = 256
	// desktopEventKeepAlive keeps idle connections open through proxies and detects closed readers.
	desktopEventKeepAlive = 15 * time.Second
)

// desktopRequestEvent is one upstream attempt as the desktop activity view shows it.
type desktopRequestEvent struct {
	At          time.Time `json:"at"`
	RequestID   string    `json:"request_id"`
	SelectionID string    `json:"selection_id,omitempty"`
	TraceID     string    `json:"trace_id,omitempty"`
	Provider    string    `json:"provider"`
	Model       string    `json:"model"`
	Alias       string    `json:"alias,omitempty"`
	AuthID      string    `json:"auth_id,omitempty"`
	AuthIndex   string    `json:"auth_index,omitempty"`
	Stream      bool      `json:"stream"`
	Failed      bool      `json:"failed"`
	StatusCode  int       `json:"status_code,omitempty"`
	Error       string    `json:"error,omitempty"`
	LatencyMs   int64     `json:"latency_ms"`
	TTFTMs      int64     `json:"ttft_ms,omitempty"`
	InputTokens int64     `json:"input_tokens"`
	Output      int64     `json:"output_tokens"`
	// Cached counts cache reads; CacheWrite counts tokens written to the prompt cache.
	Cached     int64 `json:"cached_tokens"`
	CacheWrite int64 `json:"cache_creation_tokens"`
	// Purpose is "window-start" for requests that start a usage window; empty for client traffic.
	Purpose string `json:"purpose,omitempty"`
}

type desktopEventSubscriber struct {
	events chan desktopRequestEvent
	// overflowed marks dropped events; the reader is told to resync from /usage/windows.
	overflowed atomic.Bool
}

type desktopEventHub struct {
	mu          sync.Mutex
	subscribers map[*desktopEventSubscriber]struct{}
}

var desktopEvents = &desktopEventHub{subscribers: map[*desktopEventSubscriber]struct{}{}}

func registerDesktopEvents() { coreusage.RegisterNamedPlugin(desktopEventsPluginName, desktopEvents) }

func (hub *desktopEventHub) subscribe() (*desktopEventSubscriber, func()) {
	sub := &desktopEventSubscriber{events: make(chan desktopRequestEvent, desktopEventBuffer)}
	hub.mu.Lock()
	hub.subscribers[sub] = struct{}{}
	hub.mu.Unlock()
	return sub, func() {
		hub.mu.Lock()
		delete(hub.subscribers, sub)
		hub.mu.Unlock()
	}
}

// HandleUsage implements coreusage.Plugin. It never blocks the usage dispatcher.
func (hub *desktopEventHub) HandleUsage(_ context.Context, record coreusage.Record) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.subscribers) == 0 {
		return
	}
	event := desktopEventFromRecord(record)
	for sub := range hub.subscribers {
		select {
		case sub.events <- event:
		default:
			sub.overflowed.Store(true)
		}
	}
}

func desktopEventFromRecord(record coreusage.Record) desktopRequestEvent {
	event := desktopRequestEvent{
		At: record.RequestedAt.UTC(), RequestID: record.RequestID, SelectionID: record.SelectionID, TraceID: record.TraceID,
		Provider: record.Provider, Model: record.Model, Alias: record.Alias, AuthID: record.AuthID, AuthIndex: record.AuthIndex,
		Stream: record.Stream, Failed: record.Failed, StatusCode: record.Fail.StatusCode,
		LatencyMs: record.Latency.Milliseconds(), TTFTMs: record.TTFT.Milliseconds(),
		InputTokens: record.Detail.InputTokens, Output: record.Detail.OutputTokens, Cached: record.Detail.CachedTokens,
		CacheWrite: record.Detail.CacheCreationTokens,
	}
	if record.AccessProvider == WindowStartAccessProvider {
		event.Purpose = WindowStartAccessProvider
	}
	if record.Failed && record.Fail.Body != "" {
		event.Error = coreauth.ExtractUpstreamErrorSummary(record.Fail.Body)
	}
	return event
}

// DesktopEvents streams request events until the client disconnects.
func (h *Handler) DesktopEvents(c *gin.Context) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}
	sub, unsubscribe := desktopEvents.subscribe()
	defer unsubscribe()
	// Capacity 1 coalesces lookups that land before the reader catches up into one "usage" event.
	lookups := make(chan struct{}, 1)
	if h.authManager != nil {
		stopLookups := h.authManager.SubscribeQuotaLookups(func() {
			select {
			case lookups <- struct{}{}:
			default:
			}
		})
		defer stopLookups()
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	write := func(event string, data any) bool {
		payload, errJSON := json.Marshal(data)
		if errJSON != nil {
			return true
		}
		if _, errWrite := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, payload); errWrite != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !write("ready", gin.H{}) {
		return
	}
	keepAlive := time.NewTicker(desktopEventKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event := <-sub.events:
			if !write("request", event) {
				return
			}
		case <-lookups:
			if !write("usage", gin.H{}) {
				return
			}
		case <-keepAlive.C:
			if _, errWrite := fmt.Fprint(c.Writer, ": keep-alive\n\n"); errWrite != nil {
				return
			}
			flusher.Flush()
		}
		if sub.overflowed.Swap(false) && !write("resync", gin.H{}) {
			return
		}
	}
}
