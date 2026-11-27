package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const (
	// AntigravityCLIDefaultPath is the official Antigravity CLI, looked up on PATH.
	AntigravityCLIDefaultPath = "agy"
	// antigravityCLIQuotaTimeout bounds one /usage run, which takes about 3s.
	// The run starts no model turn, so a hung CLI must not hold the quota loop.
	antigravityCLIQuotaTimeout = 30 * time.Second
	antigravityCLIStatusOK     = "SUCCESS"
	antigravityCLIUsageCommand = "usage"
)

// antigravityCLIQuotaArgs asks the CLI for its slash-command usage report as JSON.
var antigravityCLIQuotaArgs = []string{"-p", "/usage", "--output-format", "json"}

// FetchAntigravityCLIQuota runs the official CLI's /usage command for the
// account the CLI is signed into. It sends no model request. The CLI comes only
// from PATH, so the management config API cannot choose what the proxy runs.
func FetchAntigravityCLIQuota(ctx context.Context) ([]AntigravityQuotaBucket, error) {
	resolved, err := exec.LookPath(AntigravityCLIDefaultPath)
	if err != nil {
		return nil, fmt.Errorf("antigravity cli not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, antigravityCLIQuotaTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, resolved, antigravityCLIQuotaArgs...)
	backgroundAntigravityCLI(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("antigravity cli usage failed: %w", err)
	}
	lowerAntigravityCLIPriority(cmd.Process)
	if err = cmd.Wait(); err != nil {
		return nil, fmt.Errorf("antigravity cli usage failed: %w", err)
	}
	return ParseAntigravityCLIQuota(out.Bytes())
}

// ParseAntigravityCLIQuota reads `agy -p /usage --output-format json` output.
func ParseAntigravityCLIQuota(data []byte) ([]AntigravityQuotaBucket, error) {
	data = bytes.TrimSpace(data)
	if !ojson.ValidBytes(data) {
		return nil, errors.New("antigravity cli usage returned invalid JSON")
	}
	top := ojson.ParseBytes(data)
	if status := top.Get("status").Str; status != antigravityCLIStatusOK {
		return nil, fmt.Errorf("antigravity cli usage status %q", status)
	}
	if name := top.Get("command.name").Str; name != antigravityCLIUsageCommand {
		return nil, fmt.Errorf("antigravity cli returned command %q, want %q", name, antigravityCLIUsageCommand)
	}
	var buckets []AntigravityQuotaBucket
	seen := make(map[string]struct{})
	for _, group := range top.Get("command.data.groups").Array() {
		groupName := strings.Clone(strings.TrimSpace(group.Get("name").Str))
		for _, item := range group.Get("buckets").Array() {
			id := NormalizeAntigravityModelID(item.Get("id").Str)
			window := item.Get("window").Str
			if id == "" || (window != AntigravityQuotaWindowFiveHour && window != AntigravityQuotaWindowWeekly) {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			fraction, reset, ok := parseAntigravityQuotaFields(item, "remaining_fraction", "reset_time")
			if !ok {
				continue
			}
			buckets = append(buckets, AntigravityQuotaBucket{
				ID: id, Window: strings.Clone(window), GroupDisplayName: groupName,
				Description: strings.Clone(item.Get("description").Str), RemainingFraction: fraction, ResetTime: reset,
			})
			seen[id] = struct{}{}
		}
	}
	if len(buckets) == 0 {
		return nil, errors.New("antigravity cli usage reported no quota buckets")
	}
	return buckets, nil
}
