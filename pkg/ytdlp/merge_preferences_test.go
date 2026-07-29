package ytdlp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseMergeOutputFormatValid(t *testing.T) {
	if got, err := ParseMergeOutputFormat(""); err != nil || got != nil {
		t.Fatalf("empty = %#v, err = %v", got, err)
	}
	if got, err := ParseMergeOutputFormat("mp4"); err != nil || len(got) != 1 || got[0] != "mp4" {
		t.Fatalf("single = %#v, err = %v", got, err)
	}
	if got, err := ParseMergeOutputFormat("mp4/mkv/webm"); err != nil || len(got) != 3 {
		t.Fatalf("multi = %#v, err = %v", got, err)
	}
	for _, extension := range MergeOutputFormatSupported {
		if got, err := ParseMergeOutputFormat(extension); err != nil || len(got) != 1 || got[0] != extension {
			t.Fatalf("supported %q = %#v, err = %v", extension, got, err)
		}
	}
}

func TestParseMergeOutputFormatInvalid(t *testing.T) {
	invalid := []string{
		"/",
		"/mp4",
		"mp4/",
		"mp4//mkv",
		"mp4/ /mkv",
		"unknown",
		"MP4",
		" mp4",
		"mp4 ",
		"mp4/\nmkv",
		"mp4\x00mkv",
		strings.Repeat("mp4/", 20),
		strings.Repeat("a", maxMergeOutputFormatBytes+1),
	}
	for _, input := range invalid {
		if _, err := ParseMergeOutputFormat(input); !errors.Is(err, errInvalidMergeOutputFormat) {
			t.Fatalf("input %q = %v, want errInvalidMergeOutputFormat", input, err)
		}
	}
}

func TestMergeOutputFormatValidationPrecedesNetwork(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	_, err := NewClient().Run(t.Context(), Request{
		URL: server.URL, SkipDownload: true, MergeOutputFormat: "unknown",
	})
	if !IsCategory(err, ErrorInvalidInput) || requests != 0 {
		t.Fatalf("error=%v requests=%d", err, requests)
	}
}

func TestValidateRequestOptionsRejectsInvalidMergeOutputFormat(t *testing.T) {
	err := validateRequestOptions(Request{MergeOutputFormat: "mp4/"})
	if !errors.Is(err, errInvalidRequestOptions) {
		t.Fatalf("error = %v", err)
	}
}

func TestMergeOutputFormatPreferencesUsesValidatedParser(t *testing.T) {
	if got := mergeOutputFormatPreferences("mp4/mkv"); len(got) != 2 || got[0] != "mp4" || got[1] != "mkv" {
		t.Fatalf("explicit = %#v", got)
	}
	if got := mergeOutputFormatPreferences(""); got != nil {
		t.Fatalf("empty = %#v, want nil", got)
	}
	if got := mergeOutputFormatPreferences("mp4/"); got != nil {
		t.Fatalf("invalid = %#v, want nil", got)
	}
}
