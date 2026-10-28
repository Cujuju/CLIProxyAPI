package applypatch

import "testing"

// Malformed input strings must produce the same messages on every Go release.
func TestUnwrapInputNamesMalformedStrings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments string
		want      string
	}{
		{"raw newline", "{\"input\":\"a\nb\"}", "invalid control character in apply_patch input"},
		{"bad unicode escape", `{"input": "\uZZZZ"}`, "invalid Unicode escape in apply_patch input"},
		{"short unicode escape", `{"input":"\u12"}`, "invalid Unicode escape in apply_patch input"},
		{"bad escape", `{"input" : "\q"}`, "invalid escape in apply_patch input"},
	} {
		_, err := UnwrapInput(tc.arguments)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: UnwrapInput error = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestUnwrapInputAcceptsValidEscapes(t *testing.T) {
	got, err := UnwrapInput(`{"input":"a\"\\\/\b\f\n\r\té"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a\"\\/\b\f\n\r\té"; got != want {
		t.Fatalf("UnwrapInput = %q, want %q", got, want)
	}
}
