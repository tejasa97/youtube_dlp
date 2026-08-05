package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tejasa97/youtube_dlp/pkg/ytdlp"
)

func TestFriendlyAnalyzeErrorDeadline(t *testing.T) {
	if got := friendlyAnalyzeError(context.DeadlineExceeded); got != "Video analysis timed out — retry" {
		t.Fatalf("friendlyAnalyzeError() = %q; want analysis-timeout message", got)
	}
}

func TestFriendlyAnalyzeErrorYouTubeChallengeTimeout(t *testing.T) {
	typed := &ytdlp.Error{
		Category: ytdlp.ErrorUnsupported,
		Op:       "youtube extraction",
		Err: errors.New(
			"JavaScript challenge solver unavailable: EJS helper timeout: JavaScript execution timed out",
		),
	}
	err := fmt.Errorf("analyze video: %w", typed)

	if got := friendlyAnalyzeError(err); got != "YouTube challenge timed out — retry" {
		t.Fatalf("friendlyAnalyzeError() = %q; want challenge-timeout message", got)
	}
}

func TestFriendlyAnalyzeErrorOtherUnsupportedIsUnchanged(t *testing.T) {
	err := &ytdlp.Error{
		Category: ytdlp.ErrorUnsupported,
		Op:       "youtube extraction",
		Err:      errors.New("video unavailable"),
	}

	if got := friendlyAnalyzeError(err); got != "That link is not a supported single YouTube video." {
		t.Fatalf("friendlyAnalyzeError() = %q; want ordinary unsupported message", got)
	}
}
