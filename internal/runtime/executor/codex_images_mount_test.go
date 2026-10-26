package executor

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Codex's own image tool posts to /backend-api/codex/images/* when its OpenAI provider points at the proxy.
const (
	codexMountImagesGenerationsPath = "/backend-api/codex/images/generations"
	codexMountImagesEditsPath       = "/backend-api/codex/images/edits"
)

func TestCodexImagesEndpoint_MatchesEveryMount(t *testing.T) {
	cases := map[string]string{
		codexImagesGenerationsPath:      codexDirectImagesGenerations,
		codexImagesEditsPath:            codexDirectImagesEdit,
		codexMountImagesGenerationsPath: codexDirectImagesGenerations,
		codexMountImagesEditsPath:       codexDirectImagesEdit,
		"/backend-api/codex/responses":  "",
		"/v1/responses":                 "",
	}
	for path, want := range cases {
		if got := codexImagesEndpoint(path); got != want {
			t.Errorf("codexImagesEndpoint(%q) = %q, want %q", path, got, want)
		}
		if got := isCodexOpenAIImageRequest(codexOpenAIImageTestOptions(path, false)); got != (want != "") {
			t.Errorf("isCodexOpenAIImageRequest(%q) = %v", path, got)
		}
	}
}

func TestCodexPrepareOpenAIImageRequest_CodexMount(t *testing.T) {
	req := cliproxyexecutor.Request{Model: "gpt-image-2", Payload: []byte(`{"prompt":"a lighthouse","model":"gpt-image-2"}`)}
	if _, err := codexPrepareOpenAIImageRequest(req, codexOpenAIImageTestOptions(codexMountImagesGenerationsPath, false)); err != nil {
		t.Fatalf("generation: %v", err)
	}
	req.Payload = []byte(`{"prompt":"make it blue","model":"gpt-image-2","images":[{"image_url":"data:image/png;base64,iVBORw0KGgo="}]}`)
	if _, err := codexPrepareOpenAIImageRequest(req, codexOpenAIImageTestOptions(codexMountImagesEditsPath, false)); err != nil {
		t.Fatalf("edit: %v", err)
	}
}
