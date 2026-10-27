package management

import (
	"net/http"
	"path"
	"slices"
	"strings"
)

// codexAccountRoute is one ChatGPT account call Codex makes under chatgpt_base_url.
// The path matches itself and everything below it. No methods means any method.
type codexAccountRoute struct {
	path    string
	methods []string
}

// codexAccountRoutes lists the /backend-api/ paths Codex calls (codex-rs at rust-v0.164.0).
// Everything else the pool token could reach on chatgpt.com (conversations, memories,
// account admin, file downloads) is refused, so a client key cannot act as a full ChatGPT login.
var codexAccountRoutes = []codexAccountRoute{
	// Codex's own namespace: usage, plan, reset credits, cloud tasks, environments, settings.
	{path: "wham"},
	// Codex model-API paths no model route claims (model list, analytics, alpha tools).
	{path: "codex"},
	{path: "ps/plugins"},
	{path: "ps/apps/batch"},
	{path: "plugins/featured"},
	{path: "public/plugins/workspace"},
	{path: "connectors/directory/list"},
	{path: "connectors/directory/list_workspace"},
	{path: "accounts/verified_access", methods: []string{http.MethodGet}},
	// File uploads for MCP tools. Upload only: reading the account's files stays refused.
	{path: "files", methods: []string{http.MethodPost}},
}

// codexAccountCallAllowed reports whether a request is one of Codex's account calls.
// Dot segments, doubled slashes, escapes and backslashes are refused: chatgpt.com
// may normalise them into a path outside the list.
func codexAccountCallAllowed(r *http.Request) bool {
	if r == nil || r.URL == nil || r.URL.RawPath != "" {
		return false
	}
	full := r.URL.Path
	if strings.ContainsRune(full, '\\') || path.Clean(full) != strings.TrimSuffix(full, "/") {
		return false
	}
	rest, ok := strings.CutPrefix(full, CodexBackendPathPrefix)
	if !ok {
		return false
	}
	rest = strings.TrimSuffix(rest, "/")
	for _, route := range codexAccountRoutes {
		if rest != route.path && !strings.HasPrefix(rest, route.path+"/") {
			continue
		}
		if len(route.methods) == 0 || slices.Contains(route.methods, r.Method) {
			return true
		}
	}
	return false
}
