package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageRecordCarriesSelectionID(t *testing.T) {
	ctx := usage.WithSelectionID(context.Background(), "selection-1")
	reporter := NewUsageReporter(ctx, "claude", "claude-test", nil)
	if got := reporter.buildRecord(usage.Detail{}, false).SelectionID; got != "selection-1" {
		t.Fatalf("SelectionID = %q, want selection-1", got)
	}
}
