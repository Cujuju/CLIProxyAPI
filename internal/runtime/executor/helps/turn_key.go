package helps

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type turnKeyContext struct{}

func turnKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	key, _ := ctx.Value(turnKeyContext{}).(string)
	return key
}

func withTurnKey(ctx context.Context, opts cliproxyexecutor.Options, payload []byte) context.Context {
	if len(opts.OriginalRequest) > 0 {
		payload = opts.OriginalRequest
	}
	key := requestTurnKey(opts, payload)
	return context.WithValue(ctx, turnKeyContext{}, key)
}

func requestTurnKey(opts cliproxyexecutor.Options, payload []byte) string {
	for _, metadata := range []string{opts.Headers.Get("X-Codex-Turn-Metadata"), ojson.GetBytes(payload, "client_metadata.x-codex-turn-metadata").String()} {
		if id := strings.TrimSpace(ojson.Get(metadata, "turn_id").String()); id != "" {
			return "codex:" + id
		}
	}
	if opts.SourceFormat.String() != "claude" {
		return ""
	}
	count := 0
	last := ""
	for _, message := range ojson.GetBytes(payload, "messages").Array() {
		if message.Get("role").String() != "user" {
			continue
		}
		content := message.Get("content")
		tool := false
		for _, block := range content.Array() {
			if block.Get("type").String() == "tool_result" {
				tool = true
				break
			}
		}
		if !tool {
			count++
			last = content.Raw
		}
	}
	if count == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(last))
	const turnHashBytes = 6
	return fmt.Sprintf("claude:%d:%x", count, sum[:turnHashBytes])
}
