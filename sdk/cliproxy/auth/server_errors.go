package auth

import (
	"errors"
	"net/http"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// Values for routing.server-errors.
const (
	// ServerErrorsFailover tries the next credential after a server failure (default).
	ServerErrorsFailover = "failover"
	// ServerErrorsReturn returns a server failure to the client on the same credential.
	ServerErrorsReturn = "return"
)

const emptyStreamErrorCode = "empty_stream"

func returnsServerErrors(cfg *internalconfig.Config) bool {
	return cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.Routing.ServerErrors), ServerErrorsReturn)
}

// isUpstreamServerFailure reports failures of the provider's shared service rather than of one
// credential: 408 and 5xx (including 529 overload and the first-byte timeout), empty streams and
// transport errors. Every credential of a provider reaches the same servers, so another one fares no better.
func isUpstreamServerFailure(err error) bool {
	if err == nil {
		return false
	}
	if status := statusCodeFromError(err); status != 0 {
		return status == http.StatusRequestTimeout || status >= http.StatusInternalServerError
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil && (authErr.Code == emptyStreamErrorCode || authErr.Code == transientTransportErrorCode) {
		return true
	}
	return isTransientTransportError(err)
}
