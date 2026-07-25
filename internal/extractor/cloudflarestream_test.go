package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const familyFixtureRoot = "../../conformance/extractors"

func familyFixture(t testing.TB, family, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(familyFixtureRoot, family, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCloudflareStreamSuitable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL string
		want   bool
	}{
		{"https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1", true},
		{"https://cloudflarestream.com/31c9291ab41fac05471db4e73aa11717/manifest/video.mpd", true},
		{"https://embed.cloudflarestream.com/embed/we4g.fla9.latest.js?video=31c9291ab41fac05471db4e73aa11717", true},
		{"https://embed.videodelivery.net/embed/r4xu.fla9.latest.js?video=81d80727f3022488598f68d323c1ad5e", true},
		{"https://customer-aw5py76sw8wyqzmh.cloudflarestream.com/2463f6d3e06fa29710a337f5f5389fd8/iframe", true},
		{"https://players.brightcove.net/12345/default_default/index.html?videoId=123", false},
		{"https://cloudflarestream.com/not-a-valid-id", false},
		{"http://user:pass@cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1", false},
		{"https://cloudflarestream.com:8443/9df17203414fd1db3e3ed74abbe936c1", false},
		{"https://127.0.0.1/9df17203414fd1db3e3ed74abbe936c1", false},
		{"https://cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1/%2fsecret", false},
		{"https://example.invalid/9df17203414fd1db3e3ed74abbe936c1", false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if got := NewCloudflareStream().Suitable(parsed); got != test.want {
			t.Errorf("Suitable(%q)=%t want %t", test.rawURL, got, test.want)
		}
	}
}

func TestCloudflareStreamSuccessAndSignedJWTRetention(t *testing.T) {
	t.Parallel()
	result, err := NewCloudflareStream().Extract(context.Background(), Request{
		URL: "https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := result.Info.ID()
	if !ok || id != "9df17203414fd1db3e3ed74abbe936c1" {
		t.Fatalf("id=%q ok=%t", id, ok)
	}

	// Fixed JWT: delivery URLs must retain the exact token; metadata id uses sub.
	const (
		fixedJWT = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiI4OGQ0MTA4YTM2NDIwNzNlYWJhYWY4N2RhMTgyZDI2MyJ9.signature"
		wantSub  = "88d4108a3642073eabaaf87da182d263"
	)
	rawURL := "https://watch.cloudflarestream.com/" + fixedJWT
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := parseCloudflareStreamURL(parsed)
	if !ok || target.deliveryID != fixedJWT || target.videoID != wantSub {
		t.Fatalf("jwt target=%#v ok=%t", target, ok)
	}
	jwtResult, err := NewCloudflareStream().Extract(context.Background(), Request{URL: rawURL})
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := jwtResult.Info.ID(); !ok || id != wantSub {
		t.Fatalf("jwt metadata id=%q", id)
	}
	if title, _ := jwtResult.Info.Fields().Lookup("title").StringValue(); title != wantSub {
		t.Fatalf("jwt title=%q", title)
	}
	formats, ok := jwtResult.Info.Formats()
	if !ok || len(formats) != 2 {
		t.Fatalf("jwt formats=%v", formats)
	}
	for _, format := range formats {
		object, ok := format.Object()
		if !ok {
			t.Fatal("format object")
		}
		mediaURL, ok := object.Lookup("url").StringValue()
		if !ok || !strings.Contains(mediaURL, "/"+fixedJWT+"/") || strings.Contains(mediaURL, wantSub+"/manifest") {
			t.Fatalf("format URL must retain exact JWT delivery token: %q", mediaURL)
		}
	}
	thumb, _ := jwtResult.Info.Fields().Lookup("thumbnail").StringValue()
	if !strings.Contains(thumb, "/"+fixedJWT+"/") || strings.Contains(thumb, wantSub+"/thumbnails") {
		t.Fatalf("thumbnail must retain JWT: %q", thumb)
	}
	// JWT must never appear in categorized errors.
	if err := hostedStatusError(http.StatusUnauthorized, []byte("token="+fixedJWT)); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), fixedJWT) {
		t.Fatalf("jwt must not leak through errors: %v", err)
	}
}

func TestCloudflareStreamNegativeMatrix(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewCloudflareStream().Extract(canceled, Request{URL: "https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	badClaims, _ := json.Marshal(map[string]string{"sub": "not-hex"})
	badJWT := "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(badClaims) + ".sig"
	if _, _, ok := normalizeCloudflareStreamID(badJWT); ok {
		t.Fatal("expected malformed JWT rejection")
	}
	truncated := "eyJhbGciOiJSUzI1NiJ9.e30" // missing signature part
	if _, _, ok := normalizeCloudflareStreamID(truncated); ok {
		t.Fatal("expected truncated JWT rejection")
	}
	oversized := "eyJ" + strings.Repeat("a", cloudflareStreamMaxIDBytes) + ".b.c"
	if _, _, ok := normalizeCloudflareStreamID(oversized); ok {
		t.Fatal("expected oversized rejection")
	}
	if _, err := normalizeCloudflareStream(cloudflareStreamTarget{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty target=%v", err)
	}
}

func TestHytaleAdapterPlaylistAndHandoff(t *testing.T) {
	t.Parallel()
	page := familyFixture(t, "hytale", "news.html")
	transport := &sharedFixtureTransport{pages: map[string][]byte{
		"https://hytale.com/news/2021/07/summer-2021-development-update": page,
	}}
	result, err := NewHytale().Extract(context.Background(), Request{
		URL:       "https://www.hytale.com/news/2021/07/summer-2021-development-update",
		Transport: transport,
	})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	iterator := result.Entries.Iterator()
	entry, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || entry.ExtractorKey != "cloudflarestream" || entry.ID != "ed51a2609d21bad6e14145c37c334999" {
		t.Fatalf("entry=%#v ok=%t err=%v", entry, ok, err)
	}
	registry := NewRegistry(NewHytale(), NewCloudflareStream())
	selected, err := registry.SelectFor(entry.URL, entry.ExtractorKey)
	if err != nil || selected.Name() != "cloudflarestream" {
		t.Fatalf("handoff=%v err=%v", selected, err)
	}
	media, err := selected.Extract(context.Background(), Request{URL: entry.URL})
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := media.Info.ID(); !ok || id != entry.ID {
		t.Fatalf("re-entry id=%q", id)
	}
	formats, ok := media.Info.Formats()
	if !ok || len(formats) == 0 {
		t.Fatal("missing formats after re-entry")
	}
}

func TestHytaleNegativeMatrix(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewHytale().Extract(canceled, Request{
		URL: "https://hytale.com/news/2021/07/summer-2021-development-update", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	authPage := []byte(`<html>sign in password required</html>`)
	transport := &sharedFixtureTransport{pages: map[string][]byte{
		"https://hytale.com/news/2021/07/summer-2021-development-update": authPage,
	}}
	if _, err := NewHytale().Extract(context.Background(), Request{
		URL: "https://hytale.com/news/2021/07/summer-2021-development-update", Transport: transport,
	}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("auth=%v", err)
	}
	missing := &sharedFixtureTransport{pages: map[string][]byte{
		"https://hytale.com/news/2021/07/summer-2021-development-update": []byte(`<html>not found</html>`),
	}}
	if _, err := NewHytale().Extract(context.Background(), Request{
		URL: "https://hytale.com/news/2021/07/summer-2021-development-update", Transport: missing,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable=%v", err)
	}
	hostile := &sharedFixtureTransport{pages: map[string][]byte{
		"https://hytale.com/news/2021/07/summer-2021-development-update": []byte(`<html><a href="https://evil.example/">x</a></html>`),
	}}
	if _, err := NewHytale().Extract(context.Background(), Request{
		URL: "https://hytale.com/news/2021/07/summer-2021-development-update", Transport: hostile,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile=%v", err)
	}
	oversized := &sharedFixtureTransport{pages: map[string][]byte{
		"https://hytale.com/news/2021/07/summer-2021-development-update": bytesRepeat(hytaleMaxPageBytes + 1),
	}}
	if _, err := NewHytale().Extract(context.Background(), Request{
		URL: "https://hytale.com/news/2021/07/summer-2021-development-update", Transport: oversized,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized=%v", err)
	}
}

func bytesRepeat(n int) []byte {
	return []byte(strings.Repeat("a", n))
}

func TestStrictHostedURLPolicyDoesNotChangeLegacySemantics(t *testing.T) {
	t.Parallel()
	// Legacy validHostedHTTPURL still accepts explicit ports used by some fixtures.
	if !validHostedHTTPURL("https://media.example.invalid:8443/video.mp4") {
		t.Fatal("legacy validHostedHTTPURL must still accept ports")
	}
	if strictValidHostedHTTPURL("https://media.example.invalid:8443/video.mp4") {
		t.Fatal("strict helper must reject ports")
	}
	if strictValidHostedHTTPURL("https://127.0.0.1/video.mp4") {
		t.Fatal("strict helper must reject IP literals")
	}
	if _, ok := hostedURLFormat("x", "https://media.example.invalid:8443/video.mp4"); !ok {
		t.Fatal("legacy hostedURLFormat must accept ports")
	}
	if _, ok := strictHostedURLFormat("x", "https://media.example.invalid:8443/video.mp4"); ok {
		t.Fatal("strictHostedURLFormat must reject ports")
	}
}

func FuzzParseCloudflareStreamURL(f *testing.F) {
	f.Add("https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1")
	f.Add("https://embed.cloudflarestream.com/embed/x.js?video=31c9291ab41fac05471db4e73aa11717")
	f.Add("not-a-url")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseCloudflareStreamURL(parsed)
		_, _ = parseHytaleURL(parsed)
	})
}
