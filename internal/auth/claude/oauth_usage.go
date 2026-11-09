package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	// UsageURL reports subscription usage windows; Claude Code's /usage reads it.
	UsageURL = "https://api.anthropic.com/api/oauth/usage"
	// usageResetCreditsQuery adds the reset-credit block (cedar_ember) to the usage document.
	usageResetCreditsQuery = "cedar_ember=1"
	// usageBetaHeader is the beta flag the usage and reset endpoints require.
	usageBetaHeader = "oauth-2025-04-20"
	// resetRateLimitsURL claims one reset credit for an organization; Claude Code's /usage calls it.
	resetRateLimitsURL = "https://api.anthropic.com/api/organizations/%s/reset_rate_limits"
	// resetCreditProgram names the reset-credit program in a claim.
	resetCreditProgram = "cedar_ember"
)

// UsageHTTPError reports a non-2xx usage or reset response.
type UsageHTTPError struct {
	StatusCode int
}

func (e *UsageHTTPError) Error() string {
	return fmt.Sprintf("Claude OAuth request failed with status %d", e.StatusCode)
}

// FetchOAuthUsage returns the raw usage document, with its reset-credit block, for an OAuth access token.
// userAgent must be a Claude Code one: Claude reports reset credits only to Claude Code ("surface").
func (o *ClaudeAuth) FetchOAuthUsage(ctx context.Context, accessToken, userAgent string) (json.RawMessage, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, UsageURL+"?"+usageResetCreditsQuery, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create Claude OAuth usage request: %w", errRequest)
	}
	req.Header.Set("Cache-Control", "no-cache")
	body, errSend := o.sendOAuthJSON(req, accessToken, userAgent)
	if errSend != nil {
		return nil, fmt.Errorf("fetch Claude OAuth usage: %w", errSend)
	}
	return json.RawMessage(body), nil
}

// RedeemResetCredit claims one reset from grantID and returns Claude's result ("reset",
// "not_limited", "already_used", "cooldown", "ineligible" or "unavailable"). requestID names the claim;
// userAgent is a Claude Code one, as for FetchOAuthUsage.
func (o *ClaudeAuth) RedeemResetCredit(ctx context.Context, accessToken, userAgent, organizationUUID, grantID, requestID string) (string, error) {
	payload, errMarshal := json.Marshal(map[string]string{"program": resetCreditProgram, "grant_id": grantID, "request_id": requestID})
	if errMarshal != nil {
		return "", errMarshal
	}
	endpoint := fmt.Sprintf(resetRateLimitsURL, url.PathEscape(strings.TrimSpace(organizationUUID)))
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if errRequest != nil {
		return "", fmt.Errorf("create Claude reset request: %w", errRequest)
	}
	body, errSend := o.sendOAuthJSON(req, accessToken, userAgent)
	if errSend != nil {
		return "", fmt.Errorf("redeem Claude reset: %w", errSend)
	}
	var result struct {
		Result string `json:"result"`
	}
	if errDecode := json.Unmarshal(body, &result); errDecode != nil {
		return "", fmt.Errorf("parse Claude reset response: %w", errDecode)
	}
	return result.Result, nil
}

// sendOAuthJSON sends an OAuth control-plane request and returns its JSON body; a non-2xx status is a *UsageHTTPError.
// A non-empty userAgent replaces the Axios one.
func (o *ClaudeAuth) sendOAuthJSON(req *http.Request, accessToken, userAgent string) ([]byte, error) {
	if o == nil || o.httpClient == nil {
		return nil, fmt.Errorf("HTTP client is nil")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("access token is empty")
	}
	applyClaudeOAuthAxiosHeaders(req)
	if userAgent = strings.TrimSpace(userAgent); userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Anthropic-Beta", usageBetaHeader)

	resp, errDo := o.httpClient.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close Claude OAuth response body: %v", errClose)
		}
	}()
	body, errRead := readClaudeOAuthResponseBody(resp)
	if errRead != nil {
		return nil, fmt.Errorf("read response: %w", errRead)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &UsageHTTPError{StatusCode: resp.StatusCode}
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("response body is not valid JSON")
	}
	return body, nil
}
