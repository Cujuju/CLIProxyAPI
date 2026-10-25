package management

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPreferredAuthMovesNextPick(t *testing.T) {
	h := newDesktopUsageHandler(t, "sr")
	rec := serveDesktop(t, h.PutPreferredAuth, http.MethodPut, `{"provider":"claude","auth_index":"idx-late"}`, "127.0.0.1:1")
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d: %s", rec.Code, rec.Body.String())
	}
	var put struct {
		Preferred map[string]string `json:"preferred"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &put); err != nil || put.Preferred["claude"] != "idx-late" {
		t.Fatalf("put body = %s (%v)", rec.Body.String(), err)
	}
	payload := decodeDesktopUsage(t, serveDesktop(t, h.GetUsageWindows, http.MethodGet, "", "127.0.0.1:1"))
	for _, credential := range payload.Credentials {
		if credential.ID == "late" && !credential.NextPick {
			t.Fatal("preferred credential is not the next pick")
		}
		if credential.ID == "soon" && credential.NextPick {
			t.Fatal("soonest-reset pick still shown while another credential is preferred")
		}
	}

	if rec := serveDesktop(t, h.PutPreferredAuth, http.MethodPut, `{"provider":"claude","auth_index":""}`, "127.0.0.1:1"); rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d", rec.Code)
	}
	if len(h.authManager.PreferredAuths()) != 0 {
		t.Fatalf("preference not cleared: %v", h.authManager.PreferredAuths())
	}
}

func TestPreferredAuthValidation(t *testing.T) {
	h := newDesktopUsageHandler(t, "sr")
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"auth_index":"idx-late"}`, http.StatusBadRequest},
		{`{"provider":"claude","auth_index":"missing"}`, http.StatusNotFound},
		{`{"provider":"claude","auth_index":"idx-codex"}`, http.StatusBadRequest},
	} {
		if rec := serveDesktop(t, h.PutPreferredAuth, http.MethodPut, tc.body, "127.0.0.1:1"); rec.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
}
