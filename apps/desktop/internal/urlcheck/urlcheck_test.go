package urlcheck

import (
	"strings"
	"testing"
)

func TestValidate_Accepts(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		wantID string
	}{
		{"youtube.com watch", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"youtu.be short", "https://youtu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"youtube-nocookie embed", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"with extra params", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s", "dQw4w9WgXcQ"},
		{"http scheme", "http://youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Validate(tc.input)
			if err != nil {
				t.Fatalf("Validate(%q) unexpected error: %v", tc.input, err)
			}
			if got.VideoID != tc.wantID {
				t.Fatalf("videoID = %q, want %q", got.VideoID, tc.wantID)
			}
			if got.Kind != "single_video" {
				t.Fatalf("kind = %q, want single_video", got.Kind)
			}
		})
	}
}

func TestValidate_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		reason Reason
	}{
		{"empty", "", ReasonEmpty},
		{"not youtube", "https://example.com/watch?v=abcd", ReasonNotYouTube},
		{"playlist", "https://www.youtube.com/playlist?list=PL123", ReasonPlaylist},
		{"watch with list", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL123", ReasonPlaylist},
		{"search", "https://www.youtube.com/results?search_query=hello", ReasonSearch},
		{"channel at", "https://www.youtube.com/@veritasium", ReasonChannel},
		{"shorts", "https://www.youtube.com/shorts/abcdefghijk", ReasonShorts},
		{"live", "https://www.youtube.com/live/abcdefghijk", ReasonLive},
		{"missing id", "https://www.youtube.com/", ReasonMissingVideoID},
		{"ftp scheme", "ftp://youtube.com/watch?v=abc", ReasonInvalidScheme},
		{"malformed", "ht!tp://nope", ReasonMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Validate(tc.input)
			if err == nil {
				t.Fatalf("Validate(%q) returned nil error; want rejection", tc.input)
			}
			if !IsRejected(err) {
				t.Fatalf("Validate(%q) error %v not from validator", tc.input, err)
			}
			if got := ReasonOf(err); got != tc.reason {
				t.Fatalf("Validate(%q) reason = %q, want %q", tc.input, got, tc.reason)
			}
			if strings.Contains(strings.ToLower(err.Error()), "rejected") ||
				strings.Contains(err.Error(), "not_youtube") || strings.Contains(err.Error(), "invalid_scheme") ||
				strings.Contains(err.Error(), "missing_video_id") {
				t.Fatalf("Validate(%q) leaked internal rejection details: %q", tc.input, err.Error())
			}
		})
	}
}
