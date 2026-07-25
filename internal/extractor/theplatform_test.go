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

func TestThePlatformFeedExpandsSMILAndRejectsDirectSMIL(t *testing.T) {
	t.Parallel()
	feedURL := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?byGuid=n_hardball_5biden_140207"
	feedEndpoint := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?form=json&byGuid=n_hardball_5biden_140207"
	smilRelease := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT"
	smilURL := smilRelease + "?mbr=true&format=SMIL"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		feedEndpoint: {body: []byte(`{"entries":[{"title":"SMIL Feed","guid":"n_hardball_5biden_140207","media$content":[{"plfile$url":"` + smilRelease + `","plfile$format":"SMIL"}]}]}`)},
		smilURL:      {body: familyFixture(t, "theplatform", "media.smil")},
	}}
	result, err := NewThePlatformFeed().Extract(context.Background(), Request{URL: feedURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) == 0 || !sharedHasProtocol(formats, "m3u8_native") {
		t.Fatalf("expanded SMIL formats=%v", formats)
	}
	for _, format := range formats {
		object, _ := format.Object()
		mediaURL, _ := object.Lookup("url").StringValue()
		if strings.Contains(strings.ToLower(mediaURL), ".smil") || mediaURL == smilRelease {
			t.Fatalf("SMIL URL advertised as direct media: %q", mediaURL)
		}
	}
}

func TestThePlatformNegativeMatrix(t *testing.T) {
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
	if _, _, err := parseThePlatformSMIL([]byte(`<smil><body></body></smil><extra/>`)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("trailing=%v", err)
	}
	if _, _, err := parseThePlatformSMIL([]byte(`<smil><body>`)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("truncated=%v", err)
	}
	smilURL := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT?mbr=true&format=SMIL"
	auth := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		smilURL: {status: http.StatusUnauthorized, body: []byte("sig=must-not-leak")},
	}}
	if _, err := NewThePlatform().Extract(context.Background(), Request{
		URL: "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT", Transport: auth,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("auth=%v", err)
	}
	oversized := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		smilURL: {body: []byte(strings.Repeat("a", thePlatformMaxSMILBytes+1))},
	}}
	if _, err := NewThePlatform().Extract(context.Background(), Request{
		URL: "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT", Transport: oversized,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized=%v", err)
	}
}

func TestWeatherComAndNBCOlympicsHandoffEvidence(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewNBCOlympics(), NewWeatherCom(), NewThePlatform(), NewThePlatformFeed())

	nbc := "https://vplayer.nbcolympics.com/p/NnzsPC/widget/select/media/4Y0TlYUr_ZT7"
	result, err := NewNBCOlympics().Extract(context.Background(), Request{URL: nbc})
	if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "theplatform" {
		t.Fatalf("nbc=%#v err=%v", result, err)
	}
	if !strings.HasPrefix(result.Redirect.URL, "https://player.theplatform.com/") {
		t.Fatalf("rewrite=%q", result.Redirect.URL)
	}
	selected, err := registry.SelectFor(result.Redirect.URL, result.Redirect.ExtractorKey)
	if err != nil || selected.Name() != "theplatform" {
		t.Fatalf("nbc SelectFor=%v", err)
	}
	// Player media path canonicalizes to link path via parse; drive SMIL for media id.
	smilURL := "https://link.theplatform.com/s/NnzsPC/media/4Y0TlYUr_ZT7?mbr=true&format=SMIL"
	metaURL := "https://link.theplatform.com/s/NnzsPC/media/4Y0TlYUr_ZT7?format=preview"
	tpTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		smilURL: {body: familyFixture(t, "theplatform", "media.smil")},
		metaURL: {body: familyFixture(t, "theplatform", "preview.json")},
	}}
	media, err := selected.Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: tpTransport})
	if err != nil {
		t.Fatal(err)
	}
	if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
		t.Fatal("nbc re-entry missing formats")
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
	if formats, ok := weather.Info.Formats(); !ok || len(formats) == 0 {
		t.Fatal("weather missing formats")
	}
}

func TestWeatherComFailsClosedOnThePlatformErrors(t *testing.T) {
	t.Parallel()
	weatherURL := "https://weather.com/storms/hurricane/video/invest-95l-fixture"
	redux := []byte(`{"dal":{"getCMSAssetsUrlConfig":{"asset":{"data":[{"id":"81acef2d-ee8c-4545-ba83-bff3cc80db97","title":"x","variants":{"tp":"https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT"}}]}}}}`)
	smilURL := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT?mbr=true&format=SMIL"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://weather.com/api/v1/p/redux-dal": {body: redux},
		smilURL:                                  {status: http.StatusForbidden, body: []byte(`geo token=must-not-leak`)},
	}}
	_, err := NewWeatherCom().Extract(context.Background(), Request{URL: weatherURL, Transport: transport})
	if !errors.Is(err, ErrRegionRestricted) && !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected fail-closed TP error, got %v", err)
	}
	if strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("secret leak: %v", err)
	}
}

func TestWeatherComAndNBCNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewWeatherCom().Extract(canceled, Request{
		URL: "https://weather.com/storms/hurricane/video/invest-95l-fixture", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("weather cancel=%v", err)
	}
	if _, err := NewNBCOlympics().Extract(canceled, Request{URL: "https://vplayer.nbcolympics.com/p/NnzsPC/widget/select/media/4Y0TlYUr_ZT7"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("nbc cancel=%v", err)
	}
	auth := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://weather.com/api/v1/p/redux-dal": {status: http.StatusUnauthorized, body: []byte("token=must-not-leak")},
	}}
	if _, err := NewWeatherCom().Extract(context.Background(), Request{
		URL: "https://weather.com/storms/hurricane/video/invest-95l-fixture", Transport: auth,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("weather auth=%v", err)
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

func buildThePlatformSMIL(videos, captions int) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><smil><body><switch>`)
	for i := 0; i < videos; i++ {
		b.WriteString(`<video src="https://media.example.invalid/tp/v`)
		b.WriteByte(byte('0' + (i/100)%10))
		b.WriteByte(byte('0' + (i/10)%10))
		b.WriteByte(byte('0' + i%10))
		b.WriteString(`.mp4"/>`)
	}
	for i := 0; i < captions; i++ {
		b.WriteString(`<textstream src="https://media.example.invalid/tp/c`)
		b.WriteByte(byte('0' + (i/100)%10))
		b.WriteByte(byte('0' + (i/10)%10))
		b.WriteByte(byte('0' + i%10))
		b.WriteString(`.vtt" lang="en"/>`)
	}
	b.WriteString(`</switch></body></smil>`)
	return []byte(b.String())
}

func TestThePlatformCardinalityFailClosed(t *testing.T) {
	t.Parallel()

	formats, subs, err := parseThePlatformSMIL(buildThePlatformSMIL(thePlatformMaxFormats, 0))
	if err != nil || len(formats) != thePlatformMaxFormats || subs != nil {
		t.Fatalf("boundary formats: n=%d err=%v subs=%v", len(formats), err, subs)
	}
	if _, _, err := parseThePlatformSMIL(buildThePlatformSMIL(thePlatformMaxFormats+1, 0)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("format overflow=%v", err)
	}

	formats, subs, err = parseThePlatformSMIL(buildThePlatformSMIL(1, thePlatformMaxCaptions))
	if err != nil || len(formats) != 1 || subs == nil {
		t.Fatalf("boundary captions: formats=%d err=%v", len(formats), err)
	}
	if _, _, err := parseThePlatformSMIL(buildThePlatformSMIL(1, thePlatformMaxCaptions+1)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("caption overflow=%v", err)
	}

	// Feed: two SMIL expansions that together exceed the global format cap.
	half := thePlatformMaxFormats/2 + 1
	smilA := "https://link.theplatform.com/s/kYEXFC/feedA"
	smilB := "https://link.theplatform.com/s/kYEXFC/feedB"
	feedURL := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?byGuid=n_hardball_5biden_140207"
	feedEndpoint := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?form=json&byGuid=n_hardball_5biden_140207"
	feedTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		feedEndpoint:                    {body: []byte(`{"entries":[{"title":"Feed Overflow","guid":"n_hardball_5biden_140207","media$content":[{"plfile$url":"` + smilA + `"},{"plfile$url":"` + smilB + `"}]}]}`)},
		smilA + "?mbr=true&format=SMIL": {body: buildThePlatformSMIL(half, 0)},
		smilB + "?mbr=true&format=SMIL": {body: buildThePlatformSMIL(half, 0)},
	}}
	if _, err := NewThePlatformFeed().Extract(context.Background(), Request{URL: feedURL, Transport: feedTransport}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("feed format overflow=%v", err)
	}

	// Feed content entry cardinality fail-closed.
	var content strings.Builder
	content.WriteString(`{"entries":[{"title":"Too Many","guid":"n_hardball_5biden_140207","media$content":[`)
	for i := 0; i <= thePlatformMaxFeedContent; i++ {
		if i > 0 {
			content.WriteByte(',')
		}
		content.WriteString(`{"plfile$url":"https://media.example.invalid/tp/direct`)
		content.WriteByte(byte('0' + (i/100)%10))
		content.WriteByte(byte('0' + (i/10)%10))
		content.WriteByte(byte('0' + i%10))
		content.WriteString(`.mp4"}`)
	}
	content.WriteString(`]}]}`)
	contentTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		feedEndpoint: {body: []byte(content.String())},
	}}
	if _, err := NewThePlatformFeed().Extract(context.Background(), Request{URL: feedURL, Transport: contentTransport}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("feed content overflow=%v", err)
	}

	// WeatherCom: one ThePlatform SMIL variant expands past the global format cap.
	weatherURL := "https://weather.com/storms/hurricane/video/invest-95l-fixture"
	tpRelease := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT"
	weatherTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://weather.com/api/v1/p/redux-dal": {body: []byte(`{"dal":{"getCMSAssetsUrlConfig":{"asset":{"data":[{"id":"81acef2d-ee8c-4545-ba83-bff3cc80db97","title":"x","variants":{"tp":"` + tpRelease + `"}}]}}}}`)},
		tpRelease + "?mbr=true&format=SMIL":      {body: buildThePlatformSMIL(thePlatformMaxFormats+1, 0)},
	}}
	if _, err := NewWeatherCom().Extract(context.Background(), Request{URL: weatherURL, Transport: weatherTransport}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("weather format overflow=%v", err)
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
