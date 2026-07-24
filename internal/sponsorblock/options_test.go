package sponsorblock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestHashPrefixMatchesSHA256(t *testing.T) {
	const videoID = "dQw4w9WgXcQ"
	sum := sha256.Sum256([]byte(videoID))
	want := hex.EncodeToString(sum[:])[:4]
	got, err := hashPrefix(videoID)
	if err != nil {
		t.Fatalf("hashPrefix returned error: %v", err)
	}
	if got != want {
		t.Fatalf("hashPrefix = %q, want %q", got, want)
	}
	if _, err := hashPrefix(""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("hashPrefix(\"\") = %v, want ErrInvalidInput", err)
	}
	for _, invalid := range []string{"café", "line\nbreak", strings.Repeat("a", maxVideoIDBytes+1)} {
		if _, err := hashPrefix(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("hashPrefix(%q) = %v, want ErrInvalidInput", invalid, err)
		}
	}
}

func TestBuildEndpointURLCanonical(t *testing.T) {
	apiBase := "https://sponsor.ajay.app"
	endpoint, err := buildEndpointURL(apiBase, "abcd", []Category{CategorySponsor, CategoryIntro}, AllActions())
	if err != nil {
		t.Fatalf("buildEndpointURL returned error: %v", err)
	}
	if endpoint.Scheme != "https" {
		t.Fatalf("scheme = %q, want https", endpoint.Scheme)
	}
	if endpoint.Host != "sponsor.ajay.app" {
		t.Fatalf("host = %q, want sponsor.ajay.app", endpoint.Host)
	}
	if endpoint.Path != "/api/skipSegments/abcd" {
		t.Fatalf("path = %q, want /api/skipSegments/abcd", endpoint.Path)
	}
	if got := endpoint.Query().Get("service"); got != "YouTube" {
		t.Fatalf("service = %q, want YouTube", got)
	}
	categories := endpoint.Query().Get("categories")
	if categories != `["sponsor","intro"]` {
		t.Fatalf("categories = %q, want [\"sponsor\",\"intro\"]", categories)
	}
	actions := endpoint.Query().Get("actionTypes")
	if actions != `["skip","poi","chapter"]` {
		t.Fatalf("actions = %q, want [\"skip\",\"poi\",\"chapter\"]", actions)
	}
}

func TestBuildEndpointURLRejectsBadSchemes(t *testing.T) {
	for _, bad := range []string{"", "ftp://x", "sponsor.ajay.app", "javascript:alert(1)", "https://user:pass@example.test", "https://example.test?secret=x", "https://example.test/#fragment", "https://example.test/a%2fb"} {
		if _, err := buildEndpointURL(bad, "abcd", AllCategories(), AllActions()); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("buildEndpointURL(%q) err = %v, want ErrInvalidInput", bad, err)
		}
	}
}

func TestBuildEndpointURLPreservesSelfHostedPathPrefix(t *testing.T) {
	endpoint, err := buildEndpointURL("https://example.test/sponsorblock/v1/", "abcd", []Category{CategorySponsor}, AllActions())
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Path != "/sponsorblock/v1/api/skipSegments/abcd" {
		t.Fatalf("path = %q", endpoint.Path)
	}
}

func TestEncodeJSONArrayEscapesControlCharacters(t *testing.T) {
	got, err := encodeJSONArray([]string{"a\"b", "c\nd", "\t"})
	if err != nil {
		t.Fatal(err)
	}
	want := `["a\"b","c\nd","\t"]`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestOptionsValidateDeDuplicates(t *testing.T) {
	options := Options{Enabled: true, Categories: []string{"sponsor", "intro", "sponsor"}}
	if err := options.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(options.Categories) != 2 {
		t.Fatalf("after de-dup len = %d, want 2", len(options.Categories))
	}
	if options.Categories[0] != "sponsor" || options.Categories[1] != "intro" {
		t.Fatalf("order = %v, want [sponsor intro]", options.Categories)
	}
}

func TestOptionsRejectsUnknownAndEmpty(t *testing.T) {
	for _, tc := range []struct {
		name     string
		options  Options
		wantKind string
	}{
		{"unknown", Options{Enabled: true, Categories: []string{"unknown"}}, "unknown category"},
		{"empty", Options{Enabled: true, Categories: []string{""}}, "empty category"},
		{"whitespace", Options{Enabled: true, Categories: []string{"   "}}, "empty category"},
		{"long", Options{Enabled: true, Categories: []string{strings.Repeat("x", 65)}}, "category too long"},
		{"bad-api-base", Options{Enabled: true, Categories: []string{"sponsor"}, APIBase: "javascript:alert(1)"}, "invalid API base"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.options.validate()
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("validate err = %v, want ErrInvalidInput", err)
			}
			if !strings.Contains(err.Error(), tc.wantKind) {
				t.Fatalf("validate err = %q, want contains %q", err.Error(), tc.wantKind)
			}
		})
	}
}

func TestOptionsAllDuplicateNonEmpty(t *testing.T) {
	options := Options{Enabled: true, Categories: []string{"sponsor", "sponsor"}}
	if err := options.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(options.Categories) != 1 || options.Categories[0] != "sponsor" {
		t.Fatalf("dedup = %v, want [sponsor]", options.Categories)
	}
}

func TestOptionsResolvesDefaults(t *testing.T) {
	options := Options{Enabled: true}
	if err := options.validate(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty enabled options = %v, want ErrInvalidInput", err)
	}
	if got := options.resolvedAPIBase(); got != DefaultAPIBase {
		t.Fatalf("api base = %q, want %q", got, DefaultAPIBase)
	}
}

func TestOptionsRespectsCustomAPI(t *testing.T) {
	options := Options{Enabled: true, Categories: []string{"sponsor"}, APIBase: "http://localhost:9999"}
	if err := options.validate(); err != nil {
		t.Fatal(err)
	}
	if got := options.resolvedAPIBase(); got != "http://localhost:9999" {
		t.Fatalf("api base = %q, want localhost", got)
	}
}

func TestOptionsDisabledNoValidation(t *testing.T) {
	// Disabled must not validate categories even when malformed.
	options := Options{Enabled: false, Categories: []string{"unknown"}}
	if err := options.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
