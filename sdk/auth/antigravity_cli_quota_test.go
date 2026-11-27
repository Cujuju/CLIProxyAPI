package auth

import (
	"strings"
	"testing"
	"time"
)

// agyUsageOutput is `agy -p /usage --output-format json` output captured 2026-11-27, trimmed.
const agyUsageOutput = `{"conversation_id":"","status":"SUCCESS","response":"Gemini Models\tWeekly Limit Remaining\t91%\n","duration_seconds":0,"num_turns":0,"usage":{"input_tokens":0,"output_tokens":0,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":0},"command":{"name":"usage","data":{"description":"Within each group, models share a weekly limit and a 5-hour limit.","groups":[{"name":"Gemini Models","description":"Models within this group: Gemini 4 Neon","buckets":[{"id":"gemini-weekly","name":"Weekly Limit Remaining","description":"You have used some of your weekly limit, it will fully refresh in 5 days, 11 hours.","window":"weekly","remaining_fraction":0.9063769578933716,"reset_time":"2026-12-03T07:01:58Z"},{"id":"gemini-5h","name":"Five Hour Limit Remaining","description":"You have used some of your 5-hour limit, it will fully refresh in 19 minutes.","window":"5h","remaining_fraction":0.6870487332344055,"reset_time":"2026-11-27T19:44:57Z"}]},{"name":"Claude and GPT models","description":"Models within this group: Claude Opus 5.5","buckets":[{"id":"3p-weekly","name":"Weekly Limit Remaining","description":"You have used some of your weekly limit, it will fully refresh in 4 days, 1 hour.","window":"weekly","remaining_fraction":0.6695356965065002,"reset_time":"2026-12-01T21:22:46Z"},{"id":"3p-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":1,"reset_time":"1970-01-01T00:00:00Z"}]}]}}}`

func TestParseAntigravityCLIQuota(t *testing.T) {
	buckets, err := ParseAntigravityCLIQuota([]byte(agyUsageOutput))
	if err != nil {
		t.Fatal(err)
	}
	want := []AntigravityQuotaBucket{
		{ID: "gemini-weekly", Window: "weekly", GroupDisplayName: "Gemini Models", RemainingFraction: 0.9063769578933716, ResetTime: time.Date(2026, 12, 3, 7, 1, 58, 0, time.UTC)},
		{ID: "gemini-5h", Window: "5h", GroupDisplayName: "Gemini Models", RemainingFraction: 0.6870487332344055, ResetTime: time.Date(2026, 11, 27, 19, 44, 57, 0, time.UTC)},
		{ID: "3p-weekly", Window: "weekly", GroupDisplayName: "Claude and GPT models", RemainingFraction: 0.6695356965065002, ResetTime: time.Date(2026, 12, 1, 21, 22, 46, 0, time.UTC)},
		// The CLI's epoch reset marks an unstarted window.
		{ID: "3p-5h", Window: "5h", GroupDisplayName: "Claude and GPT models", RemainingFraction: 1},
	}
	if len(buckets) != len(want) {
		t.Fatalf("buckets = %d, want %d", len(buckets), len(want))
	}
	for i := range want {
		got := buckets[i]
		got.Description = ""
		if got != want[i] {
			t.Errorf("bucket %d = %+v, want %+v", i, got, want[i])
		}
	}
	if !strings.Contains(buckets[0].Description, "weekly limit") {
		t.Errorf("description dropped: %q", buckets[0].Description)
	}
}

func TestParseAntigravityCLIQuotaFractions(t *testing.T) {
	const output = `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"G","buckets":[
		{"id":"gemini-5h","window":"5h","reset_time":"2026-11-27T19:44:57Z"},
		{"id":"gemini-weekly","window":"weekly","reset_time":"1970-01-01T00:00:00Z"},
		{"id":"gemini-daily","window":"daily","remaining_fraction":0.5,"reset_time":"2026-11-27T19:44:57Z"}]}]}}}`
	buckets, err := ParseAntigravityCLIQuota([]byte(output))
	if err != nil {
		t.Fatal(err)
	}
	// A missing fraction with a real reset is exhausted; with no reset it is unknown; unknown windows are skipped.
	if len(buckets) != 1 || buckets[0].ID != "gemini-5h" || buckets[0].RemainingFraction != 0 {
		t.Fatalf("buckets = %+v, want only exhausted gemini-5h", buckets)
	}
}

func TestParseAntigravityCLIQuotaRejects(t *testing.T) {
	for name, output := range map[string]string{
		"invalid JSON":  `not json`,
		"failed status": `{"status":"ERROR","command":{"name":"usage","data":{"groups":[]}}}`,
		"other command": `{"status":"SUCCESS","command":{"name":"help","data":{}}}`,
		"no buckets":    `{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if buckets, err := ParseAntigravityCLIQuota([]byte(output)); err == nil {
				t.Fatalf("parsed %+v, want error", buckets)
			}
		})
	}
}
