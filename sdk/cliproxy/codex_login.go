package cliproxy

import (
	"strings"

	codexlogin "github.com/router-for-me/CLIProxyAPI/v8/internal/access/codex_login"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// codexCredentialFinder matches a Codex login to a Codex credential the proxy holds.
// Disabled credentials still match: they prove the login is the user's own.
func codexCredentialFinder(manager *coreauth.Manager) codexlogin.Accounts {
	return func(accountID, userID string) (string, bool) {
		for _, auth := range manager.List() {
			if !strings.EqualFold(auth.Provider, "codex") {
				continue
			}
			login, ok := codexlogin.CredentialLogin(auth.Metadata)
			if ok && login.AccountID == accountID && login.UserID == userID {
				return auth.ID, true
			}
		}
		return "", false
	}
}
