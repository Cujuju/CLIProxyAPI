package codexlogin

import (
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestConfigureForIsolatesAccessManagers(t *testing.T) {
	m1 := sdkaccess.NewManager()
	m2 := sdkaccess.NewManager()
	defer UnregisterFor(m1)
	defer UnregisterFor(m2)

	ConfigureFor(m1, func(accountID, userID string) (string, bool) {
		if accountID == "acct-1" {
			return "auth-1", true
		}
		return "", false
	})
	ConfigureFor(m2, func(accountID, userID string) (string, bool) {
		if accountID == "acct-2" {
			return "auth-2", true
		}
		return "", false
	})

	cfg := &sdkconfig.SDKConfig{APIKeys: []string{"sk-test"}}
	p1, ok1 := SyncFor(m1, cfg).(*provider)
	p2, ok2 := SyncFor(m2, cfg).(*provider)
	if !ok1 || !ok2 || p1 == p2 {
		t.Fatalf("expected distinct providers per manager, got p1=%v p2=%v", p1, p2)
	}
	if id, ok := p1.accounts("acct-1", "u"); !ok || id != "auth-1" {
		t.Fatalf("p1 lost its own account finder: id=%q ok=%v", id, ok)
	}
	if _, ok := p1.accounts("acct-2", "u"); ok {
		t.Fatal("p1 should not see m2's credential pool")
	}
}
