package extractor

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestSoundCloudEmbedRoutesAndCanonicalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			"standard player",
			"https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Fartist%2Ftrack",
			"https://soundcloud.com/artist/track",
		},
		{
			"apex iframe route",
			"https://soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Fartist%2Ftrack",
			"https://soundcloud.com/artist/track",
		},
		{
			"legacy player host and http",
			"http://player.soundcloud.com/player?color=orange&url=http%3A%2F%2Fwww.soundcloud.com%2Fartist%2Ftrack",
			"https://soundcloud.com/artist/track",
		},
		{
			"short player host",
			"https://p.soundcloud.com/player/?url=https%3A%2F%2Fapi.soundcloud.com%2Ftracks%2F123",
			"https://api.soundcloud.com/tracks/123",
		},
		{
			"playlist API",
			"https://w.soundcloud.com/player/?url=https%3A%2F%2Fapi-v2.soundcloud.com%2Fplaylists%2F456",
			"https://api.soundcloud.com/playlists/456",
		},
		{
			"inner secret token",
			"https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Fartist%2Ftrack%2Fs-inner",
			"https://soundcloud.com/artist/track?secret_token=s-inner",
		},
		{
			"outer token overrides inner",
			"https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Fartist%2Fsets%2Fmix%2Fs-inner&secret_token=s-outer",
			"https://soundcloud.com/artist/sets/mix?secret_token=s-outer",
		},
		{
			"unrelated target query dropped",
			"https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Fartist%2Ftrack%3Futm_source%3Dwidget&visual=true",
			"https://soundcloud.com/artist/track",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			got, _, ok := parseSoundCloudEmbedURL(parsed)
			if !ok || got != test.want {
				t.Fatalf("parse = %q, %v; want %q", got, ok, test.want)
			}
			result, err := NewSoundCloudEmbed().Extract(context.Background(), Request{URL: test.raw})
			if err != nil || !result.IsURL() || result.Redirect == nil ||
				result.Redirect.URL != test.want || result.Redirect.ExtractorKey != "soundcloud" ||
				!result.Redirect.Transparent {
				t.Fatalf("Extract = %#v, %v", result, err)
			}
		})
	}
}

func TestSoundCloudEmbedRejectsUnsafeAndAmbiguousURLs(t *testing.T) {
	t.Parallel()
	validTarget := "https%3A%2F%2Fsoundcloud.com%2Fartist%2Ftrack"
	for _, raw := range []string{
		"https://w.soundcloud.com/player/",
		"https://w.soundcloud.com/player/?url=",
		"https://w.soundcloud.com/player/?url=" + validTarget + "&url=" + validTarget,
		"https://w.soundcloud.com/player/?url=" + validTarget + "&secret_token=s-one&secret_token=s-two",
		"https://w.soundcloud.com/player/?url=" + validTarget + "&secret_token=invalid",
		"https://w.soundcloud.com/not-player/?url=" + validTarget,
		"https://w.soundcloud.com.evil.invalid/player/?url=" + validTarget,
		"https://user@w.soundcloud.com/player/?url=" + validTarget,
		"https://w.soundcloud.com:443/player/?url=" + validTarget,
		"https://w.soundcloud.com/player/?url=" + validTarget + "#fragment",
		"https://w.soundcloud.com/player%2f?url=" + validTarget,
		"https://w.soundcloud.com/player/?url=https%3A%2F%2Fevil.invalid%2Ftrack",
		"https://w.soundcloud.com/player/?url=https%3A%2F%2Fuser%40soundcloud.com%2Fartist%2Ftrack",
		"https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%3A443%2Fartist%2Ftrack",
		"https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Fartist%2Ftrack%23fragment",
		"https://w.soundcloud.com/player/?url=https%253A%252F%252Fsoundcloud.com%252Fartist%252Ftrack",
		"https://w.soundcloud.com/player/?url=%zz",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if canonical, _, ok := parseSoundCloudEmbedURL(parsed); ok {
			t.Errorf("accepted %q as %q", raw, canonical)
		}
	}

	for _, raw := range []string{
		"https://soundcloud.com/player",
		"https://soundcloud.com/player/not-player?url=" + validTarget,
		"https://soundcloud.com/player?url=https%3A%2F%2Fevil.invalid%2Ftrack",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if NewSoundCloud().Suitable(parsed) {
			t.Errorf("malformed apex player stolen by core extractor: %q", raw)
		}
	}
	track, err := url.Parse("https://soundcloud.com/artist/player")
	if err != nil {
		t.Fatal(err)
	}
	if target, ok := classifySoundCloudURL(track); !ok || target.kind != soundCloudTrackTarget {
		t.Fatalf("valid track slug player rejected: %#v, %v", target, ok)
	}
}

func TestSoundCloudEmbedCancellationAndNoNetwork(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	const raw = "https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Fartist%2Ftrack"
	if _, err := NewSoundCloudEmbed().Extract(ctx, Request{URL: raw}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, err := NewSoundCloudEmbed().Extract(context.Background(), Request{URL: raw}); err != nil {
		t.Fatalf("network-free extraction error = %v", err)
	}
}

func FuzzParseSoundCloudEmbedURL(f *testing.F) {
	f.Add("https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Fartist%2Ftrack")
	f.Add("https://player.soundcloud.com/player?url=https%3A%2F%2Fapi.soundcloud.com%2Ftracks%2F123&secret_token=s-token")
	f.Add("https://evil.invalid/player/?url=https%3A%2F%2Fsoundcloud.com%2Fa%2Fb")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		canonical, target, ok := parseSoundCloudEmbedURL(parsed)
		if !ok {
			return
		}
		if !strings.HasPrefix(canonical, "https://") || strings.Contains(canonical, "#") {
			t.Fatalf("unsafe canonical URL %q", canonical)
		}
		inner, err := url.Parse(canonical)
		if err != nil {
			t.Fatal(err)
		}
		verified, innerOK := classifySoundCloudURL(inner)
		if !innerOK || verified.kind != target.kind || verified.id != target.id ||
			verified.secretToken != target.secretToken {
			t.Fatalf("canonical target mismatch: %#v %#v", target, verified)
		}
		reencoded := "https://w.soundcloud.com/player/?url=" + url.QueryEscape(canonical)
		again, err := url.Parse(reencoded)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, _, roundTripOK := parseSoundCloudEmbedURL(again)
		if !roundTripOK || roundTrip != canonical {
			t.Fatalf("round trip = %q, %v; want %q", roundTrip, roundTripOK, canonical)
		}
	})
}
