package logging

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestResponseHeadersObserverSeesUnlockedSnapshot(t *testing.T) {
	ctx := WithFreshResponseHeadersHolder(context.Background())
	calls := 0
	ctx = WithResponseHeadersObserver(ctx, func(headers http.Header, at time.Time) {
		calls++
		if at.IsZero() || GetResponseHeaders(ctx).Get("X-Test") != headers.Get("X-Test") {
			t.Fatal("observer did not receive current snapshot")
		}
		headers.Set("X-Test", "mutated")
	})
	SetResponseHeaders(ctx, http.Header{"X-Test": {"initial"}})
	MergeResponseHeaders(ctx, http.Header{"X-Test": {"later"}})
	if calls != 2 || GetResponseHeaders(ctx).Get("X-Test") != "later" {
		t.Fatal("observer mutated stored headers or missed an update")
	}
}
