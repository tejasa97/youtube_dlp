package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRunVideoPasswordRecognizedOnUnsupportedPath exercises CLI flag recognition,
// acceptance, and leakage safety without coupling to any extractor. The
// deterministic unsupported path is "not-a-url", which has historically returned
// exit code 3 with a generic "unsupported" diagnostic. Product propagation is
// covered by pkg/ytdlp/video_password_test.go.
func TestRunVideoPasswordRecognizedOnUnsupportedPath(t *testing.T) {
	const secret = "VideoPwd-ΦοοΒάρ-內部-密碼 with spaces 12!@"
	var stdout, stderr bytes.Buffer
	code := RunContextIO(
		context.Background(),
		[]string{"--video-password", secret, "not-a-url"},
		strings.NewReader(""),
		&stdout, &stderr,
	)
	if code != 3 {
		t.Fatalf("Run() code = %d, want 3 (unsupported); stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported") {
		t.Fatalf("stderr does not describe unsupported: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("video password leaked through CLI surface: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestRunVideoPasswordRejectsOverlongAndNULValue asserts invalid-input
// behavior for two shapes that must never be silently coerced or echoed: an
// overlong value and a value that contains a NUL. The CLI surfaces the
// category through a categorized error, so the exit code is 2 (invalid input)
// per exitCode. The maximum length lives in pkg/ytdlp as an unexported
// constant; the test deliberately exceeds it by a wide margin to remain stable
// across internal changes.
func TestRunVideoPasswordRejectsOverlongAndNULValue(t *testing.T) {
	const marker = "VideoPwdMarker-AAAAA-12345"
	overlong := marker + strings.Repeat("x", 4097)
	withNUL := marker + "\x00suffix"
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "overlong", value: overlong},
		{name: "NUL", value: withNUL},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := RunContextIO(
				context.Background(),
				[]string{"--video-password", test.value, "https://example.invalid/video"},
				strings.NewReader(""),
				&stdout, &stderr,
			)
			if code != 2 {
				t.Fatalf("Run() code = %d, want 2 (invalid input); stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "invalid_input") {
				t.Fatalf("stderr missing invalid_input category: %q", stderr.String())
			}
			if strings.Contains(stdout.String(), marker) || strings.Contains(stderr.String(), marker) {
				t.Fatalf("video password marker leaked: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}
