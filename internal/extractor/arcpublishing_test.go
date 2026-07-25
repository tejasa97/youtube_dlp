package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestArcPublishingSuitableAndSuccess(t *testing.T) {
	t.Parallel()
	rawURL := "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab"
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if !NewArcPublishing().Suitable(parsed) {
		t.Fatal("expected Suitable")
	}
	for _, bad := range []string{
		"arcpublishing:ADN:8c99cb6e-b29c-4bc9-9173-7bf9979225ab",
		"arcpublishing:adn:not-a-uuid",
		"https://adn.com/video",
		"arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab:extra",
	} {
		parsed, err := url.Parse(bad)
		if err != nil {
			t.Fatal(err)
		}
		if NewArcPublishing().Suitable(parsed) {
			t.Fatalf("unexpected Suitable(%q)", bad)
		}
	}

	endpoint := arcAPIEndpoint("adn") + "?uuid=8c99cb6e-b29c-4bc9-9173-7bf9979225ab"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		endpoint: {body: familyFixture(t, "arcpublishing", "video.json")},
	}}
	result, err := NewArcPublishing().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := result.Info.ID()
	if !ok || id != "8c99cb6e-b29c-4bc9-9173-7bf9979225ab" {
		t.Fatalf("id=%q ok=%t", id, ok)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) == 0 || !sharedHasProtocol(formats, "m3u8_native") {
		t.Fatalf("formats=%v", formats)
	}
}

func TestArcPublishingErrorsAndCancellation(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewArcPublishing().Extract(canceled, Request{
		URL:       "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab",
		Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	endpoint := arcAPIEndpoint("adn") + "?uuid=8c99cb6e-b29c-4bc9-9173-7bf9979225ab"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		endpoint: {status: http.StatusNotFound, body: []byte(`{"token":"must-not-leak"}`)},
	}}
	err := func() error {
		_, err := NewArcPublishing().Extract(context.Background(), Request{
			URL: "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab", Transport: transport,
		})
		return err
	}()
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("unavailable secret-safe=%v", err)
	}
	authTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		endpoint: {status: http.StatusUnauthorized, body: []byte("cookie=secret")},
	}}
	if _, err := NewArcPublishing().Extract(context.Background(), Request{
		URL: "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab", Transport: authTransport,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("auth=%v", err)
	}
	emptyTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		endpoint: {body: []byte(`[]`)},
	}}
	if _, err := NewArcPublishing().Extract(context.Background(), Request{
		URL: "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab", Transport: emptyTransport,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty=%v", err)
	}
}

func TestArcAdaptersExactRoutingAndHandoff(t *testing.T) {
	t.Parallel()
	wapoURL := "https://www.washingtonpost.com/video/c/video/480ba4ee-1ec7-11e6-82c2-a7dcb313287d"
	result, err := NewWashingtonPost().Extract(context.Background(), Request{URL: wapoURL})
	if err != nil || !result.IsURL() || result.Redirect.URL != "arcpublishing:wapo:480ba4ee-1ec7-11e6-82c2-a7dcb313287d" {
		t.Fatalf("wapo=%#v err=%v", result, err)
	}

	page := familyFixture(t, "adn", "powa.html")
	transport := &sharedFixtureTransport{pages: map[string][]byte{
		"https://adn.com/politics/2020/11/02/video-senate-candidates/": page,
	}}
	adn, err := NewADN().Extract(context.Background(), Request{
		URL: "https://www.adn.com/politics/2020/11/02/video-senate-candidates/", Transport: transport,
	})
	if err != nil || !adn.IsURL() || adn.Redirect.ExtractorKey != "arcpublishing" {
		t.Fatalf("adn=%#v err=%v", adn, err)
	}

	registry := NewRegistry(
		NewWashingtonPost(), NewADN(), NewBostonGlobe(), NewGray(), NewClickOnDetroit(), NewArcPublishing(),
	)
	for _, test := range []struct {
		rawURL, want string
	}{
		{wapoURL, "washingtonpost"},
		{"https://www.adn.com/politics/2020/11/02/video-senate-candidates/", "adn"},
		{"https://www.bostonglobe.com/video/2020/12/30/metro/example/", "bostonglobe"},
		{"https://www.wabi.tv/video/2020/12/30/example/", "gray"},
		{"https://www.clickondetroit.com/video/community/2020/05/15/example/", "clickondetroit"},
		{"arcpublishing:gmg:c8793fb2-8d44-4242-881e-2db31da2d9fe", "arcpublishing"},
		{"https://players.brightcove.net/12345/default_default/index.html?videoId=123", ""},
	} {
		selected, err := registry.Select(test.rawURL)
		if test.want == "" {
			if err == nil {
				t.Fatalf("Select(%q) unexpectedly got %q", test.rawURL, selected.Name())
			}
			continue
		}
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func FuzzParseArcPublishingURL(f *testing.F) {
	f.Add("arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab")
	f.Add("arcpublishing:wapo:480ba4ee-1ec7-11e6-82c2-a7dcb313287d")
	f.Add("not-arc")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseArcPublishingURL(parsed)
		_, _ = parseWashingtonPostURL(parsed)
		_, _ = extractArcPowaEntries([]byte(raw), "adn")
	})
}
