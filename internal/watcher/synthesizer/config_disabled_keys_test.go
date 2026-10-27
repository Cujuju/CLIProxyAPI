package synthesizer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestDisabledAPIKeysSurviveConfigAndKeepTheirIdentity(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("config-version: 8\napi-keys:\n")
	for _, family := range []string{"claude", "codex", "gemini", "interactions", "vertex", "xai", "meta", "openai-compatibility"} {
		fmt.Fprintf(&raw, "  %s:\n    - name: test\n      base-url: https://upstream.example/v1\n      keys:\n        - api-key: test-key\n          disabled: true\n", family)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	identities := map[string]bool{}
	for _, disabled := range []bool{true, false} {
		text := strings.ReplaceAll(raw.String(), "disabled: true", fmt.Sprintf("disabled: %t", disabled))
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		auths, err := NewConfigSynthesizer().Synthesize(&SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
		if err != nil || len(auths) != 8 {
			t.Fatalf("synthesis: %d auths, %v", len(auths), err)
		}
		for _, auth := range auths {
			if auth.Disabled != disabled {
				t.Fatalf("%s disabled = %v, want %v", auth.Provider, auth.Disabled, disabled)
			}
			if disabled {
				identities[auth.ID] = true
			} else if !identities[auth.ID] {
				t.Fatalf("re-enabling %s changed its identity", auth.Provider)
			}
		}
	}
}
