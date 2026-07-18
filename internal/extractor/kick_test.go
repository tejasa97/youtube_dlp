package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestKickRoutingLiveExtractionAndProfile(t *testing.T) {
	for _, rawURL := range []string{
		"https://kick.com/fixture-channel",
		"https://www.kick.com/fixture/videos/5c697a87-afce-4256-b01f-3c8fe71ef5cb",
		"https://kick.com/fixture/clips/clip_01GYXVB5Y8PWAPWCWMSBCFB05X",
		"https://kick.com/fixture?clip=clip_01GYXVB5Y8PWAPWCWMSBCFB05X",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewKick().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{"https://kick.com/categories", "https://kick.com/ch/videos/not-a-uuid", "https://example.com/ch", "ftp://kick.com/ch"} {
		parsed, _ := url.Parse(rawURL)
		if NewKick().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
	endpoint := "https://kick.com/api/v2/channels/fixture-channel"
	transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
		"GET " + endpoint: {body: readRiskFixture(t, "kick", "live.json")},
	}}
	result, err := NewKick().Extract(context.Background(), Request{URL: "https://kick.com/fixture-channel", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if transport.profile != kickImpersonationProfile {
		t.Fatalf("profile = %q", transport.profile)
	}
	assertRiskString(t, result, "id", "92722911-fixture")
	assertRiskString(t, result, "title", "Fixture Live")
	assertRiskString(t, result, "live_status", "is_live")
	if live, ok := result.Info.Lookup("is_live").Bool(); !ok || !live {
		t.Fatalf("is_live = %t, %t", live, ok)
	}
	formats, _ := result.Info.Formats()
	format, _ := formats[0].Object()
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("protocol = %q", protocol)
	}
	if _, err := NewKick().Extract(context.Background(), Request{URL: "https://kick.com/fixture-channel", Transport: &memoryTransport{pages: map[string][]byte{}}}); !errors.Is(err, ErrTransportProfile) {
		t.Fatalf("missing profile error = %v", err)
	}
}

func TestKickVODAndClipManifestDirectFormats(t *testing.T) {
	vodID := "5c697a87-afce-4256-b01f-3c8fe71ef5cb"
	clipID := "clip_01GYXVB5Y8PWAPWCWMSBCFB05X"
	transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
		"GET https://kick.com/api/v1/video/" + vodID:            {body: []byte(`{"source":"https://media.example.test/vod/master.m3u8","created_at":"2026-07-17T01:02:03Z","views":9,"livestream":{"session_title":"Fixture VOD","duration":120000,"thumbnail":"https://media.example.test/vod.jpg","is_mature":false,"channel":{"id":1,"slug":"fixture","user_id":2,"user":{"username":"Uploader","bio":"Bio"}},"categories":[{"name":"Other"}]}}`)},
		"GET https://kick.com/api/v2/clips/" + clipID + "/play": {body: []byte(`{"clip":{"id":"clip_01GYXVB5Y8PWAPWCWMSBCFB05X","clip_url":"https://media.example.test/clip.mp4","title":"Fixture Clip","channel":{"id":1,"slug":"fixture"},"creator":{"id":2,"username":"Creator"},"duration":35,"views":10,"likes":3,"category":{"name":"Other"}}}`)},
	}}
	vod, err := NewKick().Extract(context.Background(), Request{URL: "https://kick.com/fixture/videos/" + vodID, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	vodFormats, _ := vod.Info.Formats()
	vodFormat, _ := vodFormats[0].Object()
	if protocol, _ := vodFormat.Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("VOD protocol = %q", protocol)
	}
	clip, err := NewKick().Extract(context.Background(), Request{URL: "https://kick.com/fixture/clips/" + clipID, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	clipFormats, _ := clip.Info.Formats()
	clipFormat, _ := clipFormats[0].Object()
	if protocol, _ := clipFormat.Lookup("protocol").StringValue(); protocol != "https" {
		t.Fatalf("clip protocol = %q", protocol)
	}
}

func TestKickFailureCategoriesCancellationAndSecretSafety(t *testing.T) {
	endpoint := "https://kick.com/api/v2/channels/fixture"
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"auth", http.StatusUnauthorized, `{}`, ErrAuthentication},
		{"challenge", http.StatusForbidden, `{}`, ErrChallengeSolver},
		{"geo", http.StatusUnavailableForLegalReasons, `{}`, ErrRegionRestricted},
		{"missing", http.StatusNotFound, `{}`, ErrUnavailable},
		{"offline", http.StatusOK, `{"livestream":null}`, ErrUnavailable},
		{"private", http.StatusOK, `{"message":"Login required for private stream"}`, ErrAuthentication},
		{"malformed", http.StatusOK, `{"secret":"kick-secret"} trailing`, ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
				"GET " + endpoint: {status: test.status, body: []byte(test.body)},
			}}
			_, err := NewKick().Extract(context.Background(), Request{URL: "https://kick.com/fixture", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "kick-secret") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewKick().Extract(ctx, Request{URL: "https://kick.com/fixture", Transport: &riskFixtureTransport{wait: true}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func FuzzClassifyKickURL(f *testing.F) {
	f.Add("https://kick.com/fixture")
	f.Add("https://kick.com/fixture/videos/5c697a87-afce-4256-b01f-3c8fe71ef5cb")
	f.Add("not a URL")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 4096 {
			t.Skip()
		}
		parsed, err := url.Parse(rawURL)
		if err == nil {
			_, _ = classifyKickURL(parsed)
		}
	})
}
