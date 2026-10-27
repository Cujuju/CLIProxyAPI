package management

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// CodexBackendPathPrefix is where Codex sends ChatGPT account calls when chatgpt_base_url points here.
	CodexBackendPathPrefix = "/backend-api/"
	codexAccountProvider   = "codex"
	chatGPTAccountIDHeader = "Chatgpt-Account-Id"
)

// chatGPTBackendOrigin is a variable so tests can point it at a local server.
var chatGPTBackendOrigin = "https://chatgpt.com"

// clientKeyHeaders and clientKeyQueryParams carry the proxy's own client key; chatgpt.com must not see it.
var (
	clientKeyHeaders     = []string{"X-Api-Key", "X-Goog-Api-Key"}
	clientKeyQueryParams = []string{"key", "auth_token"}
)

// CodexBackend forwards Codex's ChatGPT account calls (usage, plan, plugins, tasks) to chatgpt.com
// as the pool's routed Codex account, so Codex needs no ChatGPT login of its own.
// The caller authenticates with a client key first; only codexAccountRoutes are forwarded.
func (h *Handler) CodexBackend(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	if !codexAccountCallAllowed(c.Request) {
		// Logged so a new Codex release's account call shows up here rather than failing silently.
		log.WithField("method", c.Request.Method).WithField("path", c.Request.URL.Path).Warn("codex backend: refused a path outside Codex's account calls")
		c.JSON(http.StatusNotFound, gin.H{"error": "not a Codex account call"})
		return
	}
	auth := h.codexAccountAuth(time.Now())
	if auth == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no Codex account in the pool"})
		return
	}
	token, errToken := h.resolveTokenForAuth(c.Request.Context(), auth, "")
	if errToken != nil || token == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the pool's Codex account has no usable token"})
		return
	}
	origin, _ := url.Parse(chatGPTBackendOrigin)
	accountID := codexAccountID(auth)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(origin)
			r.Out.Header.Set("Authorization", "Bearer "+token)
			// Cookies would tie the client to the pool account's chatgpt.com session.
			r.Out.Header.Del("Cookie")
			for _, name := range clientKeyHeaders {
				r.Out.Header.Del(name)
			}
			if query := r.Out.URL.Query(); hasAnyKey(query, clientKeyQueryParams) {
				for _, name := range clientKeyQueryParams {
					query.Del(name)
				}
				r.Out.URL.RawQuery = query.Encode()
			}
			if accountID != "" {
				r.Out.Header.Set(chatGPTAccountIDHeader, accountID)
			} else {
				r.Out.Header.Del(chatGPTAccountIDHeader)
			}
		},
		Transport: helps.NewUtlsHTTPClient(c.Request.Context(), h.configSnapshot(), auth, 0).Transport,
		// A 401 here means the pool's credential failed. Codex would answer a 401 by
		// refreshing its own login, which the proxy holds no refresh token for.
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			if resp.StatusCode == http.StatusUnauthorized {
				resp.StatusCode = http.StatusBadGateway
				resp.Status = http.StatusText(http.StatusBadGateway)
			}
			return nil
		},
		ErrorHandler: codexBackendError,
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

// codexBackendError answers a failed forward. A client that hung up gets the client-closed status, not a gateway error.
func codexBackendError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil && clienterror.IsClientCancellation(0, err) {
		w.WriteHeader(clienterror.StatusClientClosedRequest)
		return
	}
	log.WithError(err).WithField("path", r.URL.Path).Warn("codex backend: chatgpt.com request failed")
	w.WriteHeader(http.StatusBadGateway)
}

// codexAccountAuth picks the ChatGPT-login Codex credential new requests route to,
// or the soonest to recover when the pool is spent.
func (h *Handler) codexAccountAuth(now time.Time) *coreauth.Auth {
	var group []*coreauth.Auth
	for _, auth := range h.authManager.List() {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), codexAccountProvider) || auth.AuthKind() == coreauth.AuthKindAPIKey {
			continue
		}
		group = append(group, auth)
	}
	if len(group) == 0 {
		return nil
	}
	strategy := ""
	if cfg := h.configSnapshot(); cfg != nil {
		if normalized, ok := normalizeRoutingStrategy(strings.TrimSpace(cfg.Routing.Strategy)); ok {
			strategy = normalized
		}
	}
	return coreauth.AccountViewAuth(strategy, h.authManager.PreferredAuths()[codexAccountProvider], group, now)
}

func codexAccountID(auth *coreauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	id, _ := auth.Metadata["account_id"].(string)
	return strings.TrimSpace(id)
}

func hasAnyKey(values url.Values, keys []string) bool {
	for _, key := range keys {
		if values.Has(key) {
			return true
		}
	}
	return false
}
