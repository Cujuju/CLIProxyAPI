package auth

import (
	"context"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// selectionTicket is the ID of one credential selection (upstream router-for-me#6988). Usage
// records of that selection carry it, so a scheduler plugin can link a pick to its usage.
type selectionTicket struct {
	id string
}

type selectionTicketKey struct{}

// beginSelection gives the next pick a fresh selection ID.
func beginSelection(ctx context.Context) (context.Context, *selectionTicket) {
	ticket := &selectionTicket{id: uuid.NewString()}
	return context.WithValue(ctx, selectionTicketKey{}, ticket), ticket
}

func selectionTicketFrom(ctx context.Context) *selectionTicket {
	if ctx == nil {
		return nil
	}
	ticket, _ := ctx.Value(selectionTicketKey{}).(*selectionTicket)
	return ticket
}

// reissue replaces the ID after a plugin left the pick to the built-in scheduler, so the
// built-in choice is never attributed to the plugin's decision.
func (t *selectionTicket) reissue() {
	if t != nil {
		t.id = uuid.NewString()
	}
}

// attach freezes the selection ID into the execution context; executors copy it into usage records.
func (t *selectionTicket) attach(ctx context.Context) context.Context {
	if t == nil {
		return ctx
	}
	return usage.WithSelectionID(ctx, t.id)
}

// withSelectionIDMetadata adds the current selection ID to scheduler plugin metadata.
func withSelectionIDMetadata(ctx context.Context, metadata map[string]any) map[string]any {
	ticket := selectionTicketFrom(ctx)
	if ticket == nil {
		delete(metadata, cliproxyexecutor.SelectionIDMetadataKey)
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[cliproxyexecutor.SelectionIDMetadataKey] = ticket.id
	return metadata
}
