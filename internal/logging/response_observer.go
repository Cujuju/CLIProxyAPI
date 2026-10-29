package logging

import (
	"context"
	"net/http"
	"time"
)

// WithResponseHeadersObserver observes HTTP headers and later websocket metadata.
// The callback runs outside the header holder lock.
func WithResponseHeadersObserver(ctx context.Context, fn func(http.Header, time.Time)) context.Context {
	return context.WithValue(ctx, responseHeadersObserverKey{}, fn)
}

func notifyResponseHeaders(ctx context.Context, headers http.Header, at time.Time) {
	if fn, ok := ctx.Value(responseHeadersObserverKey{}).(func(http.Header, time.Time)); ok && fn != nil {
		fn(headers, at)
	}
}
