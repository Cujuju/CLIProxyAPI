package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// extractUpstreamID reads only response-level identifiers, never event or output-item IDs.
func extractUpstreamID(payload []byte) string {
	data := jsonPayload(payload)
	if len(data) == 0 {
		return ""
	}
	if !bytes.Contains(data, []byte(`"id"`)) && !bytes.Contains(data, []byte(`"responseId"`)) {
		return ""
	}
	root := gjson.ParseBytes(data)
	if !root.IsObject() || !gjson.ValidBytes(data) {
		return ""
	}
	kind := root.Get("type").String()
	paths := []string{"responseId", "response.responseId"}
	switch kind {
	case "message_start":
		paths = append(paths, "message.id")
	case "response.created", "response.in_progress", "response.completed", "response.done", "response.failed", "response.incomplete":
		paths = append(paths, "response.id")
	case "", "message", "response":
		paths = append(paths, "response.id", "id", "interaction.id")
	}
	for _, path := range paths {
		if id := root.Get(path); id.Type == gjson.String && id.String() != "" {
			return id.String()
		}
	}
	return ""
}
