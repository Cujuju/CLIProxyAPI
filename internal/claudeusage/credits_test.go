package claudeusage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/resetcredits"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// creditUsage is a usage document's reset-credit block in Claude's shape (values changed): two
// usable grants, a paused one, an expired one and a spent one.
const creditUsage = `{"five_hour":{"utilization":2,"resets_at":"2026-10-25T14:00:00Z"},"cedar_ember":{
	"eligible":true,"next_grant_id":"launch",
	"grants":[
		{"id":"tenure","resets_left":1,"ends_at":"2027-03-08T23:59:00-08:00","paused":false,"usable_now":true},
		{"id":"launch","resets_left":1,"ends_at":"2027-01-01T00:00:00Z","paused":false,"usable_now":true},
		{"id":"held","resets_left":2,"ends_at":null,"paused":true,"usable_now":false},
		{"id":"old","resets_left":1,"ends_at":"2026-10-01T00:00:00Z","paused":false,"usable_now":true},
		{"id":"spent","resets_left":0,"ends_at":"2027-01-01T00:00:00Z","paused":false,"usable_now":true}],
	"cooldown_until":null}}`

var creditNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func TestParseCreditsCountsUnexpiredResetsAndPicksTheNextGrant(t *testing.T) {
	state := parseCredits(json.RawMessage(creditUsage), creditNow)
	want := resetcredits.Credits{Available: 4, Applicable: 2, Expiries: []time.Time{
		time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 3, 9, 7, 59, 0, 0, time.UTC)}}
	if state.credits == nil || !state.credits.Same(want) || state.nextGrant != "launch" || !state.coolingUntil.IsZero() {
		t.Fatalf("state = %+v credits = %+v", state, state.credits)
	}
}

func TestParseCreditsDuringACooldownHasNoneUsable(t *testing.T) {
	doc := `{"cedar_ember":{"eligible":true,"next_grant_id":"a","cooldown_until":"2026-10-25T13:00:00Z",
		"grants":[{"id":"a","resets_left":1,"usable_now":true}]}}`
	state := parseCredits(json.RawMessage(doc), creditNow)
	if state.credits == nil || state.credits.Available != 1 || state.credits.Applicable != 0 || state.nextGrant != "" || state.coolingUntil.IsZero() {
		t.Fatalf("state = %+v credits = %+v", state, state.credits)
	}
}

func TestParseCreditsIgnoresIneligibleOrMissingBlocks(t *testing.T) {
	for _, doc := range []string{`{}`, `{"cedar_ember":null}`, `{"cedar_ember":{"eligible":false,"grants":[{"id":"a","resets_left":1,"usable_now":true}]}}`, `not json`} {
		if state := parseCredits(json.RawMessage(doc), creditNow); state.credits != nil {
			t.Fatalf("%s: credits = %+v", doc, state.credits)
		}
	}
}

func creditFixture(t *testing.T, body string, auths ...*coreauth.Auth) *pollerFixture {
	t.Helper()
	f := newPollerFixture(t, auths...)
	f.now = creditNow
	f.poller.fetch = func(_ context.Context, _ *config.Config, auth *coreauth.Auth) (json.RawMessage, error) {
		f.calls[auth.ID]++
		return json.RawMessage(body), nil
	}
	return f
}

func TestRefreshStoresClaudeResetCredits(t *testing.T) {
	f := creditFixture(t, creditUsage, claudeOAuth("a"))
	if errRefresh := f.poller.Refresh(context.Background(), claudeOAuth("a")); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	stored, _ := f.manager.GetByID("a")
	got, ok := resetcredits.Of(stored)
	if !ok || got.Available != 4 || got.Applicable != 2 || !got.CheckedAt.Equal(creditNow) {
		t.Fatalf("credits = %+v, %v", got, ok)
	}
}

func TestRefreshLeavesIneligibleAccountsUntouched(t *testing.T) {
	f := creditFixture(t, `{"five_hour":{"utilization":2,"resets_at":"2026-10-25T14:00:00Z"}}`, claudeOAuth("a"))
	if errRefresh := f.poller.Refresh(context.Background(), claudeOAuth("a")); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	stored, _ := f.manager.GetByID("a")
	if _, has := stored.Metadata["reset_credits_checked_at"]; has {
		t.Fatalf("metadata = %v; want no reset-credit keys", stored.Metadata)
	}
}

type claim struct{ organization, grant, request string }

func redeemFixture(t *testing.T, body, result string, errRedeem error) (*pollerFixture, *coreauth.Auth, *[]claim) {
	t.Helper()
	auth := claudeOAuth("a")
	auth.Metadata["organization_uuid"] = "org-1"
	f := creditFixture(t, body, auth)
	var claims []claim
	f.poller.redeem = func(_ context.Context, _ *config.Config, _ *coreauth.Auth, organization, grant, request string) (string, error) {
		claims = append(claims, claim{organization, grant, request})
		return result, errRedeem
	}
	return f, auth, &claims
}

func TestRedeemClaimsTheNextGrantWithAFreshRequestID(t *testing.T) {
	f, auth, claims := redeemFixture(t, creditUsage, "reset", nil)
	for range 2 {
		code, errRedeem := f.poller.RedeemResetCredit(context.Background(), auth)
		if errRedeem != nil || code != resetcredits.Reset {
			t.Fatalf("code = %q, %v", code, errRedeem)
		}
	}
	got := *claims
	if len(got) != 2 || got[0].organization != "org-1" || got[0].grant != "launch" || got[0].request == "" || got[0].request == got[1].request {
		t.Fatalf("claims = %+v", got)
	}
}

func TestRedeemMapsClaudeResults(t *testing.T) {
	for result, want := range map[string]string{"not_limited": resetcredits.NothingToReset, "already_used": resetcredits.AlreadyRedeemed, "ineligible": resetcredits.NoCredit} {
		f, auth, _ := redeemFixture(t, creditUsage, result, nil)
		if code, errRedeem := f.poller.RedeemResetCredit(context.Background(), auth); errRedeem != nil || code != want {
			t.Fatalf("%s: code = %q, %v", result, code, errRedeem)
		}
	}
	for _, result := range []string{"cooldown", "unavailable", "surprise"} {
		f, auth, _ := redeemFixture(t, creditUsage, result, nil)
		if _, errRedeem := f.poller.RedeemResetCredit(context.Background(), auth); errRedeem == nil {
			t.Fatalf("%s was not an error", result)
		}
	}
	f, auth, _ := redeemFixture(t, creditUsage, "", &claudeauth.UsageHTTPError{StatusCode: 429})
	if _, errRedeem := f.poller.RedeemResetCredit(context.Background(), auth); errRedeem == nil {
		t.Fatal("HTTP 429 was not an error")
	}
}

func TestRedeemWithoutAUsableGrantSpendsNothing(t *testing.T) {
	f, auth, claims := redeemFixture(t, `{"cedar_ember":{"eligible":true,"grants":[]}}`, "reset", nil)
	if code, errRedeem := f.poller.RedeemResetCredit(context.Background(), auth); errRedeem != nil || code != resetcredits.NoCredit || len(*claims) != 0 {
		t.Fatalf("code = %q, %v, claims = %v", code, errRedeem, *claims)
	}
}

func TestRedeemNeedsTheLoginsOrganization(t *testing.T) {
	f, auth, claims := redeemFixture(t, creditUsage, "reset", nil)
	delete(auth.Metadata, "organization_uuid")
	if _, errRedeem := f.poller.RedeemResetCredit(context.Background(), auth); errRedeem == nil || len(*claims) != 0 {
		t.Fatalf("redeemed without an organization: %v", *claims)
	}
}
