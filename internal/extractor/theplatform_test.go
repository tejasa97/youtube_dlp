package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestThePlatformSuitableSuccessAndFeed(t *testing.T) {
	t.Parallel()
	rawURL := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT"
	parsed, err := url.Parse(rawURL)
	if err != nil || !NewThePlatform().Suitable(parsed) {
		t.Fatalf("Suitable: %v", err)
	}
	smilURL := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT?mbr=true&format=SMIL"
	metaURL := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT?format=preview"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		smilURL: {body: familyFixture(t, "theplatform", "media.smil")},
		metaURL: {body: familyFixture(t, "theplatform", "preview.json")},
	}}
	result, err := NewThePlatform().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := result.Info.ID()
	if !ok || id != "22d_qsQ6MIRT" {
		t.Fatalf("id=%q", id)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) == 0 || !sharedHasProtocol(formats, "m3u8_native") {
		t.Fatalf("formats=%v", formats)
	}

	feedURL := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?byGuid=n_hardball_5biden_140207"
	feedEndpoint := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?form=json&byGuid=n_hardball_5biden_140207"
	feedTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		feedEndpoint: {body: familyFixture(t, "theplatform", "feed.json")},
	}}
	feed, err := NewThePlatformFeed().Extract(context.Background(), Request{URL: feedURL, Transport: feedTransport})
	if err != nil {
		t.Fatal(err)
	}
	feedID, ok := feed.Info.ID()
	if !ok || feedID != "n_hardball_5biden_140207" {
		t.Fatalf("feed id=%q", feedID)
	}
}

func TestThePlatformErrorsAdaptersAndSecurity(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewThePlatform().Extract(canceled, Request{
		URL: "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	geoSMIL := []byte(`<?xml version="1.0"?><smil><body><ref src="http://link.theplatform.com/s/errorFiles/Unavailable." abstract="blocked"><param name="exception" value="GeoLocationBlocked"/></ref></body></smil>`)
	if _, _, err := parseThePlatformSMIL(geoSMIL); !errors.Is(err, ErrRegionRestricted) {
		t.Fatalf("geo=%v", err)
	}
	if err := hostedStatusError(http.StatusUnauthorized, []byte("sig=must-not-leak")); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("secret-safe=%v", err)
	}

	nbc := "https://vplayer.nbcolympics.com/p/NnzsPC/widget/select/media/4Y0TlYUr_ZT7"
	result, err := NewNBCOlympics().Extract(context.Background(), Request{URL: nbc})
	if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "theplatform" {
		t.Fatalf("nbc=%#v err=%v", result, err)
	}
	if !strings.HasPrefix(result.Redirect.URL, "https://player.theplatform.com/") {
		t.Fatalf("rewrite=%q", result.Redirect.URL)
	}

	weatherURL := "https://weather.com/storms/hurricane/video/invest-95l-fixture"
	weatherTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://weather.com/api/v1/p/redux-dal": {body: familyFixture(t, "weathercom", "redux.json")},
	}}
	weather, err := NewWeatherCom().Extract(context.Background(), Request{URL: weatherURL, Transport: weatherTransport})
	if err != nil {
		t.Fatal(err)
	}
	wid, ok := weather.Info.ID()
	if !ok || wid != "81acef2d-ee8c-4545-ba83-bff3cc80db97" {
		t.Fatalf("weather id=%q", wid)
	}

	for _, bad := range []string{
		"https://link.theplatform.com/s/../secret",
		"http://user:pass@link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT",
		"https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test",
		"https://weather.com/",
		"https://evil.example/p/NnzsPC/media/4Y0TlYUr_ZT7",
	} {
		parsed, err := url.Parse(bad)
		if err != nil {
			t.Fatal(err)
		}
		if NewThePlatform().Suitable(parsed) || NewThePlatformFeed().Suitable(parsed) || NewWeatherCom().Suitable(parsed) || NewNBCOlympics().Suitable(parsed) {
			t.Fatalf("unexpected Suitable(%q)", bad)
		}
	}
}

func FuzzParseThePlatformURL(f *testing.F) {
	f.Add("https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT")
	f.Add("https://player.theplatform.com/p/NnzsPC/widget/select/media/4Y0TlYUr_ZT7")
	f.Add("https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?byGuid=n_hardball_5biden_140207")
	f.Add("nope")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseThePlatformURL(parsed)
		_, _ = parseThePlatformFeedURL(parsed)
		_, _ = parseWeatherComURL(parsed)
		_, _ = parseNBCOlympicsURL(parsed)
		_, _, _ = parseThePlatformSMIL([]byte(raw))
	})
}
