package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

func TestCloudflareStreamSuccessAndJWT(t *testing.T) {
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
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 2 || !sharedHasProtocol(formats, "m3u8_native") || !sharedHasProtocol(formats, "http_dash_segments") {
		t.Fatalf("formats=%v", formats)
	}

	claims, _ := json.Marshal(map[string]string{"sub": "88d4108a3642073eabaaf87da182d263"})
	jwt := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	parsed, err := url.Parse("https://watch.cloudflarestream.com/" + jwt)
	if err != nil {
		t.Fatal(err)
	}
	target, ok := parseCloudflareStreamURL(parsed)
	if !ok || target.videoID != "88d4108a3642073eabaaf87da182d263" {
		t.Fatalf("jwt target=%#v ok=%t", target, ok)
	}
}

func TestCloudflareStreamErrorsCancellationAndSecrets(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewCloudflareStream().Extract(canceled, Request{URL: "https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if _, err := normalizeCloudflareStream(cloudflareStreamTarget{videoID: "9df17203414fd1db3e3ed74abbe936c1", domain: "cloudflarestream.com", canonical: "https://cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1"}); err != nil {
		t.Fatal(err)
	}
	badClaims, _ := json.Marshal(map[string]string{"sub": "not-hex"})
	badJWT := "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(badClaims) + ".sig"
	if _, ok := normalizeCloudflareStreamID(badJWT); ok {
		t.Fatal("expected malformed JWT rejection")
	}
	if err := hostedStatusError(401, []byte("token=must-not-leak")); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("secret-safe auth=%v", err)
	}
}

func TestHytaleAdapterPlaylist(t *testing.T) {
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
	selected, err := NewRegistry(NewHytale(), NewCloudflareStream()).SelectFor(entry.URL, entry.ExtractorKey)
	if err != nil || selected.Name() != "cloudflarestream" {
		t.Fatalf("handoff=%v err=%v", selected, err)
	}
}

func TestHytaleSuitableNegative(t *testing.T) {
	t.Parallel()
	for _, rawURL := range []string{
		"https://hytale.com/media",
		"https://cloudflarestream.com/ed51a2609d21bad6e14145c37c334999",
		"https://evil.example/news/2021/07/summer-2021-development-update",
		"https://hytale.com/news/21/07/summer",
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if NewHytale().Suitable(parsed) {
			t.Fatalf("unexpected Suitable(%q)", rawURL)
		}
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
