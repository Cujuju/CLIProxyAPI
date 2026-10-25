package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	// UsageURL reports subscription usage windows; Claude Code's /usage reads it.
	UsageURL = "https://api.anthropic.com/api/oauth/usage"
	// usageBetaHeader is the beta flag the usage endpoint requires.
	usageBetaHeader = "oauth-2025-04-20"
)

// UsageHTTPError reports a non-2xx usage response.
type UsageHTTPError struct {
	StatusCode int
}

func (e *UsageHTTPError) Error() string {
	return fmt.Sprintf("fetch Claude OAuth usage failed with status %d", e.StatusCode)
}

// FetchOAuthUsage returns the raw usage document for an OAuth access token.
func (o *ClaudeAuth) FetchOAuthUsage(ctx context.Context, accessToken string) (json.RawMessage, error) {
	if o == nil || o.httpClient == nil {
		return nil, fmt.Errorf("fetch Claude OAuth usage: HTTP client is nil")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("fetch Claude OAuth usage: access token is empty")
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, UsageURL, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create Claude OAuth usage request: %w", errRequest)
	}
	applyClaudeOAuthAxiosHeaders(req)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Anthropic-Beta", usageBetaHeader)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("fetch Claude OAuth usage: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close Claude OAuth usage response body: %v", errClose)
		}
	}()
	body, errRead := readClaudeOAuthResponseBody(resp)
	if errRead != nil {
		return nil, fmt.Errorf("read Claude OAuth usage response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &UsageHTTPError{StatusCode: resp.StatusCode}
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("parse Claude OAuth usage response: body is not valid JSON")
	}
	return json.RawMessage(body), nil
}
