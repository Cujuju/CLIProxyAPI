package codexusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// consumeURL redeems one reset credit; Codex's /usage panel calls the same endpoint.
var consumeURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"

// Consume outcome codes, as ChatGPT reports them.
const (
	// ConsumeReset means the credit was spent and the account's reached limits were cleared.
	ConsumeReset = "reset"
	// ConsumeNothingToReset means no limit was reached, so no credit was spent.
	ConsumeNothingToReset = "nothing_to_reset"
	// ConsumeNoCredit means the account holds no credit.
	ConsumeNoCredit = "no_credit"
	// ConsumeAlreadyRedeemed means this redeem request id was used before.
	ConsumeAlreadyRedeemed = "already_redeemed"
)

// ConsumeResult is ChatGPT's answer to one redeem request.
type ConsumeResult struct {
	Code         string `json:"code"`
	WindowsReset int    `json:"windows_reset"`
}

// ConsumeResetCredit spends one of the account's rate-limit reset credits. Each call carries a fresh
// redeem request id, so a retried HTTP request is never charged twice.
func ConsumeResetCredit(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (ConsumeResult, error) {
	payload, errMarshal := json.Marshal(map[string]string{"redeem_request_id": uuid.NewString()})
	if errMarshal != nil {
		return ConsumeResult{}, errMarshal
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, consumeURL, bytes.NewReader(payload))
	if errReq != nil {
		return ConsumeResult{}, errReq
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+metadataString(auth, "access_token"))
	req.Header.Set("User-Agent", executor.CodexUserAgent)
	if accountID := metadataString(auth, "account_id"); accountID != "" {
		req.Header.Set(accountIDHeader, accountID)
	}
	resp, errDo := helps.NewUtlsHTTPClient(ctx, cfg, auth, requestTimeout).Do(req)
	if errDo != nil {
		return ConsumeResult{}, errDo
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if errRead != nil {
		return ConsumeResult{}, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return ConsumeResult{}, fmt.Errorf("reset credit redeem: HTTP %d", resp.StatusCode)
	}
	var result ConsumeResult
	if errDecode := json.Unmarshal(body, &result); errDecode != nil {
		return ConsumeResult{}, errDecode
	}
	return result, nil
}
