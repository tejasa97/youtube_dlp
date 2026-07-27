package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func readDiscoveryFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile("testdata/dplay/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestDiscoveryDPlayRoutesAllConcreteAdapters(t *testing.T) {
	tests := []struct {
		extractor DiscoveryDPlay
		rawURL    string
	}{
		{NewAmHistoryChannel(), "https://www.ahctv.com/video/show/episode"}, {NewAnimalPlanet(), "https://animalplanet.com/video/show/episode"}, {NewCookingChannel(), "https://watch.cookingchanneltv.com/video/show/episode"}, {NewDPlay(), "https://www.dplay.no/videoer/show/episode"}, {NewDestinationAmerica(), "https://www.destinationamerica.com/video/show/episode"}, {NewDiscoveryLife(), "https://discoverylife.com/video/show/episode"}, {NewDiscoveryNetworksDe(), "https://tlc.de/sendungen/show/episode"}, {NewDiscoveryPlus(), "https://discoveryplus.com/gb/video/show/episode"}, {NewDiscoveryPlusIndia(), "https://www.discoveryplus.in/videos/show/episode"}, {NewDiscoveryPlusIndiaShow(), "https://discoveryplus.in/show/a-show"}, {NewDiscoveryPlusItaly(), "https://discoveryplus.com/it/video/show/episode"}, {NewDiscoveryPlusItalyShow(), "https://discoveryplus.it/programmi/a-show"}, {NewFoodNetwork(), "https://watch.foodnetwork.com/video/show/episode"}, {NewGoDiscovery(), "https://go.discovery.com/video/show/episode"}, {NewHGTVDe(), "https://de.hgtv.com/sendungen/show/episode"}, {NewHGTVUsa(), "https://watch.hgtv.com/video/show/episode"}, {NewInvestigationDiscovery(), "https://investigationdiscovery.com/video/show/episode"}, {NewScienceChannel(), "https://sciencechannel.com/video/show/episode"}, {NewTLC(), "https://go.tlc.com/video/show/episode"}, {NewTravelChannel(), "https://watch.travelchannel.com/video/show/episode"}, {NewTele5(), "https://tele5.de/mediathek/star-trek/vox-sola"},
	}
	for _, test := range tests {
		t.Run(test.extractor.Name(), func(t *testing.T) {
			parsed, err := url.Parse(test.rawURL)
			if err != nil || !test.extractor.Suitable(parsed) {
				t.Fatalf("%s did not accept %q", test.extractor.Name(), test.rawURL)
			}
		})
	}
}

func TestDiscoveryDPlayRejectsUnsafeRouting(t *testing.T) {
	adapter := NewGoDiscovery()
	for _, rawURL := range []string{"https://discovery.com.evil.invalid/video/a/b", "https://user@discovery.com/video/a/b", "https://discovery.com:444/video/a/b", "https://discovery.com/video/a%2fb", "https://discovery.com/video/a/b#fragment", "https://discovery.com/video/a/../b"} {
		parsed, _ := url.Parse(rawURL)
		if adapter.Suitable(parsed) {
			t.Fatalf("accepted unsafe URL %q", rawURL)
		}
	}
}

func TestDiscoveryEveryAdapterRejectsLookalikeHost(t *testing.T) {
	adapters := []DiscoveryDPlay{NewAmHistoryChannel(), NewAnimalPlanet(), NewCookingChannel(), NewDPlay(), NewDestinationAmerica(), NewDiscoveryLife(), NewDiscoveryNetworksDe(), NewDiscoveryPlus(), NewDiscoveryPlusIndia(), NewDiscoveryPlusIndiaShow(), NewDiscoveryPlusItaly(), NewDiscoveryPlusItalyShow(), NewFoodNetwork(), NewGoDiscovery(), NewHGTVDe(), NewHGTVUsa(), NewInvestigationDiscovery(), NewScienceChannel(), NewTLC(), NewTravelChannel(), NewTele5()}
	parsed, _ := url.Parse("https://discoveryplus.com.evil.invalid/video/show/episode")
	for _, adapter := range adapters {
		if adapter.Suitable(parsed) {
			t.Fatalf("%s accepted lookalike", adapter.Name())
		}
	}
	for _, test := range []struct {
		adapter DiscoveryDPlay
		rawURL  string
	}{
		{NewAmHistoryChannel(), "https://ahctv.com/video/a/b"}, {NewAnimalPlanet(), "https://animalplanet.com/video/a/b"}, {NewCookingChannel(), "https://watch.cookingchanneltv.com/video/a/b"}, {NewDPlay(), "https://dplay.no/videoer/a/b"}, {NewDestinationAmerica(), "https://destinationamerica.com/video/a/b"}, {NewDiscoveryLife(), "https://discoverylife.com/video/a/b"}, {NewDiscoveryNetworksDe(), "https://dmax.de/sendungen/a/b"}, {NewDiscoveryPlus(), "https://discoveryplus.com/gb/video/a/b"}, {NewDiscoveryPlusIndia(), "https://discoveryplus.in/videos/a/b"}, {NewDiscoveryPlusIndiaShow(), "https://discoveryplus.in/show/a"}, {NewDiscoveryPlusItaly(), "https://discoveryplus.com/it/video/a/b"}, {NewDiscoveryPlusItalyShow(), "https://discoveryplus.it/programmi/a"}, {NewFoodNetwork(), "https://foodnetwork.com/video/a/b"}, {NewGoDiscovery(), "https://go.discovery.com/video/a/b"}, {NewHGTVDe(), "https://de.hgtv.com/sendungen/a/b"}, {NewHGTVUsa(), "https://hgtv.com/video/a/b"}, {NewInvestigationDiscovery(), "https://investigationdiscovery.com/video/a/b"}, {NewScienceChannel(), "https://sciencechannel.com/video/a/b"}, {NewTLC(), "https://go.tlc.com/video/a/b"}, {NewTravelChannel(), "https://travelchannel.com/video/a/b"}, {NewTele5(), "https://tele5.de/mediathek/a/b"},
	} {
		parsed, _ := url.Parse(test.rawURL)
		parsed.Host = parsed.Hostname() + ".evil.invalid"
		if test.adapter.Suitable(parsed) {
			t.Fatalf("%s accepted own-host lookalike %q", test.adapter.Name(), parsed)
		}
	}
	hgtv, _ := url.Parse("https://de.hgtv.com/video/show/episode")
	if NewHGTVDe().Suitable(hgtv) {
		t.Fatal("HGTV Germany accepted non-sendungen route")
	}
}

func TestDiscoveryDPlayScopedCredentialsAndMetadata(t *testing.T) {
	transport := &discoveryFixtureTransport{st: "fixture-token"}
	result, err := NewGoDiscovery().Extract(context.Background(), Request{URL: "https://go.discovery.com/video/show/episode", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "video-1" {
		t.Fatalf("id = %q", id)
	}
	if transport.tokenRequests != 0 {
		t.Fatalf("token request despite valid st cookie")
	}
	if len(transport.authorizations) != 2 {
		t.Fatalf("authorization requests = %d", len(transport.authorizations))
	}
	for _, authorization := range transport.authorizations {
		if authorization != "Bearer fixture-token" {
			t.Fatalf("bad authorization %q", authorization)
		}
	}
}

func TestDiscoveryAdapterHeaderConventions(t *testing.T) {
	for _, test := range []struct {
		adapter                           DiscoveryDPlay
		apiHost, realm, product           string
		client, params, referer, authName string
	}{
		{NewAmHistoryChannel(), "us1-prod-direct.ahctv.com", "go", "ahc", "WEB:UNKNOWN:ahc:27.43.0", "realm=go,siteLookupKey=ahc", "https://ahctv.com/video/a/b", "Authorization"},
		{NewAnimalPlanet(), "us1-prod-direct.animalplanet.com", "go", "apl", "WEB:UNKNOWN:apl:27.43.0", "realm=go,siteLookupKey=apl", "https://animalplanet.com/video/a/b", "Authorization"},
		{NewCookingChannel(), "us1-prod-direct.watch.cookingchanneltv.com", "go", "cook", "WEB:UNKNOWN:cook:27.43.0", "realm=go,siteLookupKey=cook", "https://watch.cookingchanneltv.com/video/a/b", "Authorization"},
		{NewDPlay(), "disco-api.dplay.se", "dplayse", "", "", "", "https://dplay.se/videoer/a/b", "Authorization"},
		{NewDestinationAmerica(), "us1-prod-direct.destinationamerica.com", "go", "dam", "WEB:UNKNOWN:dam:27.43.0", "realm=go,siteLookupKey=dam", "https://destinationamerica.com/video/a/b", "Authorization"},
		{NewDiscoveryLife(), "us1-prod-direct.discoverylife.com", "go", "dlf", "WEB:UNKNOWN:dlf:27.43.0", "realm=go,siteLookupKey=dlf", "https://discoverylife.com/video/a/b", "Authorization"},
		{NewDiscoveryNetworksDe(), "eu1-prod.disco-api.com", "dmaxde", "", "Alps:HyogaPlayer:0.0.0", "realm=dmaxde", "https://dmax.de/sendungen/a/b", "Authorization"},
		{NewDiscoveryPlus(), "us1-prod-direct.discoveryplus.com", "go", "dplus_us", "WEB:UNKNOWN:dplus_us:27.43.0", "realm=go,siteLookupKey=dplus_us", "https://discoveryplus.com/video/a/b", "Authorization"},
		{NewDiscoveryPlusIndia(), "ap2-prod-direct.discoveryplus.in", "dplusindia", "dplus-india", "WEB:UNKNOWN:dplus-india:17.0.0", "realm=dplusindia", "https://discoveryplus.in/videos/a/b", "Authorization"},
		{NewDiscoveryPlusIndiaShow(), "ap2-prod-direct.discoveryplus.in", "dplusindia", "dplus-india", "WEB:UNKNOWN:dplus-india:prod", "realm=dplusindia", "https://www.discoveryplus.in/", "Authentication"},
		{NewDiscoveryPlusItaly(), "eu1-prod-direct.discoveryplus.com", "dplay", "dplus_it", "WEB:UNKNOWN:dplus_us:27.43.0", "realm=dplay,siteLookupKey=dplus_it", "https://discoveryplus.com/it/video/a/b", "Authorization"},
		{NewDiscoveryPlusItalyShow(), "disco-api.discoveryplus.it", "dplayit", "dplay-client", "WEB:UNKNOWN:dplay-client:2.6.0", "realm=dplayit", "https://www.discoveryplus.it/", "Authentication"},
		{NewFoodNetwork(), "us1-prod-direct.watch.foodnetwork.com", "go", "food", "WEB:UNKNOWN:food:27.43.0", "realm=go,siteLookupKey=food", "https://foodnetwork.com/video/a/b", "Authorization"},
		{NewGoDiscovery(), "us1-prod-direct.go.discovery.com", "go", "dsc", "WEB:UNKNOWN:dsc:27.43.0", "realm=go,siteLookupKey=dsc", "https://go.discovery.com/video/a/b", "Authorization"},
		{NewHGTVDe(), "eu1-prod.disco-api.com", "hgtv", "hgtv", "Alps:HyogaPlayer:0.0.0", "realm=hgtv", "https://de.hgtv.com/sendungen/a/b", "Authorization"},
		{NewHGTVUsa(), "us1-prod-direct.watch.hgtv.com", "go", "hgtv", "WEB:UNKNOWN:hgtv:27.43.0", "realm=go,siteLookupKey=hgtv", "https://hgtv.com/video/a/b", "Authorization"},
		{NewInvestigationDiscovery(), "us1-prod-direct.investigationdiscovery.com", "go", "ids", "WEB:UNKNOWN:ids:27.43.0", "realm=go,siteLookupKey=ids", "https://investigationdiscovery.com/video/a/b", "Authorization"},
		{NewScienceChannel(), "us1-prod-direct.sciencechannel.com", "go", "sci", "WEB:UNKNOWN:sci:27.43.0", "realm=go,siteLookupKey=sci", "https://sciencechannel.com/video/a/b", "Authorization"},
		{NewTLC(), "us1-prod-direct.tlc.com", "go", "tlc", "WEB:UNKNOWN:tlc:27.43.0", "realm=go,siteLookupKey=tlc", "https://go.tlc.com/video/a/b", "Authorization"},
		{NewTravelChannel(), "us1-prod-direct.watch.travelchannel.com", "go", "trav", "WEB:UNKNOWN:trav:27.43.0", "realm=go,siteLookupKey=trav", "https://travelchannel.com/video/a/b", "Authorization"},
		{NewTele5(), "eu1-prod.disco-api.com", "dmaxde", "", "Alps:HyogaPlayer:0.0.0", "realm=dmaxde", "https://tele5.de/mediathek/a/b", "Authorization"},
	} {
		if test.adapter.config.apiHost != test.apiHost || test.adapter.config.realm != test.realm || test.adapter.config.product != test.product {
			t.Fatalf("%s config=%#v", test.adapter.Name(), test.adapter.config)
		}
		var headers http.Header
		if test.adapter.config.route == discoveryShowRoute {
			headers = test.adapter.showHeaders("Bearer scoped")
		} else {
			headers = test.adapter.headers(test.referer, "Bearer scoped")
		}
		if headers.Get(test.authName) != "Bearer scoped" || headers.Get("Referer") != test.referer || headers.Get("x-disco-client") != test.client || headers.Get("x-disco-params") != test.params {
			t.Fatalf("%s headers=%#v", test.adapter.Name(), headers)
		}
	}
}

func TestDiscoveryAcceptedSourceHostIsCanonicalReferer(t *testing.T) {
	for _, test := range []struct {
		adapter DiscoveryDPlay
		rawURL  string
	}{
		{NewGoDiscovery(), "https://go.discovery.com/video/show/episode?ignored=1"},
		{NewFoodNetwork(), "https://watch.foodnetwork.com/video/show/episode"},
		{NewHGTVUsa(), "https://watch.hgtv.com/video/show/episode"},
		{NewCookingChannel(), "https://watch.cookingchanneltv.com/video/show/episode"},
		{NewTravelChannel(), "https://watch.travelchannel.com/video/show/episode"},
		{NewTLC(), "https://go.tlc.com/video/show/episode"},
		{NewDPlay(), "https://www.dplay.no/videoer/show/episode"},
		{NewDPlay(), "https://es.dplay.com/channel/show/episode"},
		{NewDiscoveryPlus(), "https://www.discoveryplus.com/gb/video/show/episode"},
	} {
		parsed, err := url.Parse(test.rawURL)
		if err != nil {
			t.Fatal(err)
		}
		adapter := test.adapter
		adapter.config = adapter.configFor(parsed)
		target, ok := adapter.target(parsed)
		if !ok {
			t.Fatalf("%s rejected %q", adapter.Name(), test.rawURL)
		}
		want := "https://" + strings.ToLower(parsed.Hostname()) + parsed.EscapedPath()
		if target.canonical != want || adapter.headers(target.canonical, "Bearer scoped").Get("Referer") != want {
			t.Fatalf("%s canonical=%q Referer=%q want=%q", adapter.Name(), target.canonical, adapter.headers(target.canonical, "Bearer scoped").Get("Referer"), want)
		}
	}
}

func TestDiscoveryDPlayAcquiresBoundedTokenWhenCookieAbsent(t *testing.T) {
	transport := &discoveryFixtureTransport{}
	adapter := NewGoDiscovery()
	adapter.deviceID = func() (string, error) { return "0123456789abcdef0123456789abcdef", nil }
	_, err := adapter.Extract(context.Background(), Request{URL: "https://go.discovery.com/video/show/episode", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if transport.tokenRequests != 1 || len(transport.tokenURLs) != 1 || !strings.Contains(transport.tokenURLs[0], "realm=go") || !strings.Contains(transport.tokenURLs[0], "deviceId=0123456789abcdef0123456789abcdef") {
		t.Fatalf("token requests: %#v", transport.tokenURLs)
	}
	for _, authorization := range transport.authorizations {
		if authorization != "Bearer fresh-token" {
			t.Fatalf("token was not scoped to API call: %q", authorization)
		}
	}
	invalid := NewGoDiscovery()
	invalid.deviceID = func() (string, error) { return "must-not-leak", nil }
	if _, err := invalid.Extract(context.Background(), Request{URL: "https://go.discovery.com/video/show/episode", Transport: &discoveryFixtureTransport{}}); !errors.Is(err, ErrInvalidMetadata) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("invalid device ID error=%v", err)
	}
}

func TestDiscoveryContentEndpointSegmentsAndPinnedFields(t *testing.T) {
	endpoint, err := discoveryContentURL("https://api.example.invalid/", "show/episode")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/content/videos/show/episode" {
		t.Fatalf("path = %q", parsed.Path)
	}
	query := parsed.Query()
	for key, want := range map[string]string{"fields[channel]": "name", "fields[image]": "height,src,width", "fields[show]": "name", "fields[tag]": "name", "fields[video]": "description,episodeNumber,name,publishStart,seasonNumber,videoDuration", "include": "images,primaryChannel,show,tags"} {
		if got := query.Get(key); got != want {
			t.Fatalf("%s = %q", key, got)
		}
	}
}

func TestDiscoveryLegacyPlaybackAndTokenOmitDeviceID(t *testing.T) {
	transport := &discoveryFixtureTransport{}
	adapter := NewDPlay()
	adapter.deviceID = func() (string, error) { return "", io.ErrUnexpectedEOF }
	result, err := adapter.Extract(context.Background(), Request{URL: "https://dplay.no/video/show/episode", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Info.Formats(); !ok {
		t.Fatal("missing legacy format")
	}
	if len(transport.tokenURLs) != 1 || strings.Contains(transport.tokenURLs[0], "deviceId=") || !strings.Contains(transport.tokenURLs[0], "realm=dplayno") {
		t.Fatalf("legacy token URL %#v", transport.tokenURLs)
	}
}

func TestDiscoveryV3PlaybackFixture(t *testing.T) {
	payload := readDiscoveryFixture(t, "v3-playback.json")
	var playback discoveryPlaybackResponse
	if err := json.Unmarshal(payload, &playback); err != nil {
		t.Fatal(err)
	}
	if len(playback.Streaming) != 2 || playback.Streaming[0].Type != "hls" || playback.Streaming[1].Type != "dash" {
		t.Fatalf("v3 streams %#v", playback.Streaming)
	}
}

func TestDiscoveryAllCommittedFixturesAreExercised(t *testing.T) {
	var legacy discoveryPlaybackResponse
	if err := json.Unmarshal(readDiscoveryFixture(t, "legacy-playback.json"), &legacy); err != nil || len(legacy.Streaming) != 2 {
		t.Fatalf("legacy=%#v err=%v", legacy, err)
	}
	var content discoveryContentResponse
	if err := json.Unmarshal(readDiscoveryFixture(t, "content.json"), &content); err != nil || content.Data.ID != "video-1" {
		t.Fatalf("content=%#v err=%v", content, err)
	}
	for _, test := range []struct {
		name    string
		adapter DiscoveryDPlay
		want    string
	}{{"india-show.json", NewDiscoveryPlusIndiaShow(), "show-india"}, {"italy-show.json", NewDiscoveryPlusItalyShow(), "show-italy"}} {
		var cms discoveryShowCMS
		if err := json.Unmarshal(readDiscoveryFixture(t, test.name), &cms); err != nil {
			t.Fatal(err)
		}
		plan, err := test.adapter.showPlan(cms, "fixture")
		if err != nil || plan.showID != test.want || len(plan.seasons) != 2 {
			t.Fatalf("%s plan=%#v err=%v", test.name, plan, err)
		}
	}
	var page discoveryShowPage
	if err := json.Unmarshal(readDiscoveryFixture(t, "show-page.json"), &page); err != nil {
		t.Fatal(err)
	}
	if fingerprint, err := discoveryShowPageFingerprint(page); err != nil || fingerprint == "" {
		t.Fatalf("fingerprint=%q err=%v", fingerprint, err)
	}
	var tele5 discoveryTele5CMS
	if err := json.Unmarshal(readDiscoveryFixture(t, "tele5.json"), &tele5); err != nil || len(tele5.Blocks) != 3 {
		t.Fatalf("tele5=%#v err=%v", tele5, err)
	}
	var german discoveryGermanCMS
	if err := json.Unmarshal(readDiscoveryFixture(t, "german.json"), &german); err != nil || german.UID != "synthetic-4756322" {
		t.Fatalf("german=%#v err=%v", german, err)
	}
	if !bytes.Contains(readDiscoveryFixture(t, "error-geo.json"), []byte("geoblocked")) {
		t.Fatal("geo fixture")
	}
	if _, _, err := discoveryHLSManifest(context.Background(), fixtureBodyTransport{body: readDiscoveryFixture(t, "master.m3u8")}, "https://cdn.example.invalid/master.m3u8"); err != nil {
		t.Fatal(err)
	}
	hlsFormats, hlsSubs, err := discoveryHLSManifest(context.Background(), fixtureBodyTransport{body: readDiscoveryFixture(t, "master.m3u8")}, "https://cdn.example.invalid/master.m3u8")
	if err != nil || len(hlsFormats) != 1 || len(hlsSubs["en"]) != 1 {
		t.Fatalf("HLS formats=%d subtitles=%#v err=%v", len(hlsFormats), hlsSubs, err)
	}
	dashFormats, dashSubs, err := discoveryDASHManifest(context.Background(), fixtureBodyTransport{body: readDiscoveryFixture(t, "master.mpd")}, "https://cdn.example.invalid/master.mpd")
	if err != nil || len(dashFormats) != 1 || len(dashSubs["en"]) != 1 {
		t.Fatalf("DASH formats=%d subtitles=%#v err=%v", len(dashFormats), dashSubs, err)
	}
	dashObject, _ := dashFormats[0].Object()
	if formatID, _ := dashObject.Lookup("format_id").StringValue(); formatID != "dash" {
		t.Fatalf("DASH format ID=%q", formatID)
	}
	if _, _, err := discoveryDASHManifest(context.Background(), fixtureBodyTransport{body: readDiscoveryFixture(t, "master.mpd")}, "https://cdn.example.invalid/master.mpd"); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryManifestBoundsErrorsAndCancellation(t *testing.T) {
	var huge strings.Builder
	huge.WriteString("#EXTM3U\n")
	for i := 0; i < 65; i++ {
		huge.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n")
	}
	if _, _, err := discoveryHLSManifest(context.Background(), fixtureBodyTransport{body: []byte(huge.String())}, "https://cdn.example.invalid/master.m3u8"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("overflow=%v", err)
	}
	for _, test := range []struct {
		name string
		body []byte
		dash bool
	}{{"empty", nil, false}, {"malformed", []byte("bad"), false}, {"dash-empty", nil, true}, {"dash-text", []byte(`<MPD><Period><AdaptationSet contentType="text"><Representation id="s"><BaseURL>s.vtt</BaseURL></Representation></AdaptationSet></Period></MPD>`), true}} {
		if test.dash {
			_, _, err := discoveryDASHManifest(context.Background(), fixtureBodyTransport{body: test.body}, "https://cdn.example.invalid/master.mpd")
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("%s=%v", test.name, err)
			}
		} else {
			_, _, err := discoveryHLSManifest(context.Background(), fixtureBodyTransport{body: test.body}, "https://cdn.example.invalid/master.m3u8")
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("%s=%v", test.name, err)
			}
		}
	}
	oversized := make([]byte, discoveryMaxManifestBytes+1)
	copy(oversized, "#EXTM3U\n")
	if _, _, err := discoveryHLSManifest(context.Background(), fixtureBodyTransport{body: oversized}, "https://cdn.example.invalid/master.m3u8"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized HLS=%v", err)
	}
	var dashOverflow strings.Builder
	dashOverflow.WriteString("<MPD><Period><AdaptationSet contentType=\"video\">")
	for i := 0; i <= discoveryMaxStreaming; i++ {
		dashOverflow.WriteString(`<Representation id="v"><BaseURL>v.mp4</BaseURL></Representation>`)
	}
	dashOverflow.WriteString("</AdaptationSet></Period></MPD>")
	if _, _, err := discoveryDASHManifest(context.Background(), fixtureBodyTransport{body: []byte(dashOverflow.String())}, "https://cdn.example.invalid/master.mpd"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized DASH=%v", err)
	}
	deduplicated, _, err := discoveryHLSManifest(context.Background(), fixtureBodyTransport{body: []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2\nv.m3u8\n")}, "https://cdn.example.invalid/master.m3u8")
	if err != nil || len(deduplicated) != 1 {
		t.Fatalf("deduplicated HLS=%d err=%v", len(deduplicated), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = discoveryManifest(ctx, fixtureBodyTransport{readErr: io.ErrUnexpectedEOF}, "https://cdn.example.invalid/master.m3u8")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	deadline, deadlineCancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer deadlineCancel()
	_, err = discoveryManifest(deadline, fixtureBodyTransport{readErr: io.ErrUnexpectedEOF}, "https://cdn.example.invalid/master.m3u8")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	var candidates []value.Value
	for i := 0; i < discoveryMaxFormats+100; i++ {
		rawURL := fmt.Sprintf("https://cdn.example.invalid/%03d.mp4", i)
		format, _ := strictHostedURLFormat("http", rawURL)
		candidates = append(candidates, value.ObjectValue(format), value.ObjectValue(format))
	}
	stable := discoveryStableFormats(candidates)
	if len(stable) != discoveryMaxFormats {
		t.Fatalf("aggregate format bound=%d", len(stable))
	}
	ids := make(map[string]bool, len(stable))
	for _, item := range stable {
		object, _ := item.Object()
		id, _ := object.Lookup("format_id").StringValue()
		if ids[id] {
			t.Fatalf("duplicate stable format ID %q", id)
		}
		ids[id] = true
	}
}

func TestDiscoveryMixedPlaybackFormatsSubtitlesAndFallback(t *testing.T) {
	var content discoveryContentResponse
	if err := json.Unmarshal(readDiscoveryFixture(t, "content.json"), &content); err != nil {
		t.Fatal(err)
	}
	transport := manifestMatrixTransport{hls: readDiscoveryFixture(t, "master.m3u8"), dash: readDiscoveryFixture(t, "master.mpd")}
	playback := discoveryPlaybackResponse{Streaming: []discoveryStream{
		{Type: "hls", URL: "https://cdn.example.invalid/master.m3u8"},
		{Type: "hls", URL: "https://cdn.example.invalid/master.m3u8"},
		{Type: "dash", URL: "https://cdn.example.invalid/master.mpd"},
		{Type: "http", URL: "https://cdn.example.invalid/video.mp4"},
		{Type: "mystery", URL: "https://cdn.example.invalid/alternate.webm"},
		{Type: "http", URL: "http://127.0.0.1/private.mp4"},
	}}
	result, err := NewGoDiscovery().media(context.Background(), transport, content, playback, discoveryTarget{displayID: "show/episode", canonical: "https://go.discovery.com/video/show/episode"})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 4 {
		t.Fatalf("mixed formats=%d", len(formats))
	}
	formatIDs := make(map[string]bool, len(formats))
	for _, item := range formats {
		object, _ := item.Object()
		id, _ := object.Lookup("format_id").StringValue()
		if formatIDs[id] {
			t.Fatalf("duplicate format ID %q", id)
		}
		formatIDs[id] = true
	}
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok {
		t.Fatal("missing manifest subtitles")
	}
	english, _ := subtitles.Lookup("en").ListValue()
	if len(english) != 2 {
		t.Fatalf("English subtitles=%d", len(english))
	}
	for _, item := range english {
		object, _ := item.Object()
		isolated, _ := object.Lookup("_credential_isolated").Bool()
		if !isolated {
			t.Fatalf("subtitle is not credential-isolated: %#v", object)
		}
	}
	fallbackTransport := manifestMatrixTransport{hls: []byte("malformed")}
	fallbackPlayback := discoveryPlaybackResponse{Streaming: []discoveryStream{{Type: "hls", URL: "https://cdn.example.invalid/master.m3u8"}, {Type: "http", URL: "https://cdn.example.invalid/video.mp4"}}}
	fallback, err := NewGoDiscovery().media(context.Background(), fallbackTransport, content, fallbackPlayback, discoveryTarget{displayID: "show/episode", canonical: "https://go.discovery.com/video/show/episode"})
	if err != nil {
		t.Fatal(err)
	}
	fallbackFormats, _ := fallback.Info.Formats()
	if len(fallbackFormats) != 1 {
		t.Fatalf("manifest fallback formats=%d", len(fallbackFormats))
	}
}

func TestDiscoveryMetadataMappingAndThumbnailValidation(t *testing.T) {
	var content discoveryContentResponse
	payload := []byte(`{"data":{"id":"video-1","attributes":{"name":" Fixture ","description":" Description ","videoDuration":2500,"seasonNumber":3,"episodeNumber":4,"publishStart":"2020-01-02T03:04:05Z"}},"included":[{"type":"channel","attributes":{"name":"Network"}},{"type":"show","attributes":{"name":"Series"}},{"type":"tag","attributes":{"name":"Tag"}},{"type":"image","attributes":{"src":"https://images.example.invalid/poster.jpg","width":1280,"height":720}},{"type":"image","attributes":{"src":"http://127.0.0.1/private.jpg","width":1,"height":1}}]}`)
	if err := json.Unmarshal(payload, &content); err != nil {
		t.Fatal(err)
	}
	playback := discoveryPlaybackResponse{Streaming: []discoveryStream{{Type: "http", URL: "https://cdn.example.invalid/video.mp4"}}}
	result, err := NewGoDiscovery().media(context.Background(), manifestMatrixTransport{}, content, playback, discoveryTarget{displayID: "show/episode", canonical: "https://go.discovery.com/video/show/episode"})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"title": "Fixture", "description": "Description", "creator": "Network", "series": "Series", "display_id": "show/episode", "webpage_url": "https://go.discovery.com/video/show/episode"} {
		got, _ := result.Info.Lookup(key).StringValue()
		if got != want {
			t.Fatalf("%s=%q want=%q", key, got, want)
		}
	}
	for key, want := range map[string]int64{"season_number": 3, "episode_number": 4, "timestamp": 1577934245} {
		got, _ := result.Info.Lookup(key).Int()
		if got != want {
			t.Fatalf("%s=%d want=%d", key, got, want)
		}
	}
	if duration, _ := result.Info.Lookup("duration").Float(); duration != 2.5 {
		t.Fatalf("duration=%v", duration)
	}
	tags, _ := result.Info.Lookup("tags").ListValue()
	thumbnails, _ := result.Info.Lookup("thumbnails").ListValue()
	if len(tags) != 1 || len(thumbnails) != 1 {
		t.Fatalf("tags=%d thumbnails=%d", len(tags), len(thumbnails))
	}
	thumbnail, _ := thumbnails[0].Object()
	width, _ := thumbnail.Lookup("width").Int()
	height, _ := thumbnail.Lookup("height").Int()
	if width != 1280 || height != 720 {
		t.Fatalf("thumbnail dimensions=%dx%d", width, height)
	}
}

type manifestMatrixTransport struct{ hls, dash []byte }

func (manifestMatrixTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, io.EOF
}
func (manifestMatrixTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, io.EOF
}
func (transport manifestMatrixTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, ".mpd") {
		return discoveryHTTP(200, string(transport.dash)), nil
	}
	return discoveryHTTP(200, string(transport.hls)), nil
}

func TestDiscoveryJSONBoundsNilResponsesAndCancellation(t *testing.T) {
	var target any
	for _, test := range []struct {
		name string
		call func(Transport) error
	}{
		{"content", func(transport Transport) error {
			return discoveryRequestJSON(context.Background(), transport, http.MethodGet, "https://api.example.invalid/content", nil, make(http.Header), &target)
		}},
		{"token", func(transport Transport) error {
			return discoveryTokenJSON(context.Background(), transport, "https://api.example.invalid/token", &target)
		}},
		{"public", func(transport Transport) error {
			return discoveryPublicJSON(context.Background(), transport, "https://public.example.invalid/page", &target)
		}},
	} {
		if err := test.call(nilDiscoveryResponseTransport{}); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("%s nil response=%v", test.name, err)
		}
	}
	if _, err := discoveryManifest(context.Background(), nilDiscoveryResponseTransport{}, "https://cdn.example.invalid/master.m3u8"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("manifest nil response=%v", err)
	}
	oversized := bytes.Repeat([]byte(" "), int(maxExtractorJSONBytes)+1)
	if err := discoveryTokenJSON(context.Background(), fixtureBodyTransport{body: oversized}, "https://api.example.invalid/token", &target); !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversized token=%v", err)
	}
	for _, payload := range [][]byte{
		[]byte(`{"data":{"attributes":{"streaming":17}}}`),
		[]byte(`{"data":{"attributes":{"streaming":[{"type":"hls","url":1}]}}}`),
		[]byte(`{"data":{"attributes":{"streaming":null}}} trailing`),
	} {
		var playback discoveryPlaybackResponse
		if err := json.Unmarshal(payload, &playback); err == nil {
			t.Fatalf("accepted malformed playback %q", payload)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := discoveryTokenJSON(ctx, contextErrorTransport{}, "https://api.example.invalid/token", &target); !errors.Is(err, context.Canceled) {
		t.Fatalf("token cancellation=%v", err)
	}
}

type nilDiscoveryResponseTransport struct{}

func (nilDiscoveryResponseTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (nilDiscoveryResponseTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, nil
}
func (nilDiscoveryResponseTransport) DoWithoutCredentialsNoRedirect(context.Context, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (nilDiscoveryResponseTransport) DoWithScopedAuthorizationNoRedirect(context.Context, *http.Request) (*http.Response, error) {
	return nil, nil
}

type contextErrorTransport struct{}

func (contextErrorTransport) Do(ctx context.Context, _ *http.Request) (*http.Response, error) {
	return nil, ctx.Err()
}
func (contextErrorTransport) ReadPage(ctx context.Context, _ string) ([]byte, http.Header, error) {
	return nil, nil, ctx.Err()
}
func (contextErrorTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, _ *http.Request) (*http.Response, error) {
	return nil, ctx.Err()
}
func (contextErrorTransport) DoWithScopedAuthorizationNoRedirect(ctx context.Context, _ *http.Request) (*http.Response, error) {
	return nil, ctx.Err()
}

func TestDiscoveryStructuredErrorMatrix(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		want   error
	}{{401, "{}", ErrAuthentication}, {403, "{}", ErrAuthentication}, {404, "{}", ErrUnavailable}, {410, "{}", ErrUnavailable}, {429, "{}", ErrDiscoveryRateLimited}, {500, "{}", ErrDiscoveryNetwork}, {451, "{}", ErrRegionRestricted}, {400, string(readDiscoveryFixture(t, "error-geo.json")), ErrRegionRestricted}, {400, `{"errors":[{"code":"access.denied.missingpackage"}]}`, ErrAuthentication}} {
		var target any
		err := discoveryRequestJSON(context.Background(), fixtureBodyTransport{status: test.status, body: []byte(test.body)}, http.MethodGet, "https://api.example.invalid/x", nil, make(http.Header), &target)
		err = discoveryError(err)
		if !errors.Is(err, test.want) {
			t.Fatalf("status %d error=%v want=%v", test.status, err, test.want)
		}
	}
	var target any
	err := discoveryRequestJSON(context.Background(), fixtureBodyTransport{body: []byte(`{} trailing`)}, http.MethodGet, "https://api.example.invalid/x", nil, make(http.Header), &target)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("trailing=%v", err)
	}
	err = discoveryRequestJSON(context.Background(), fixtureBodyTransport{readErr: io.ErrUnexpectedEOF}, http.MethodGet, "https://api.example.invalid/x", nil, make(http.Header), &target)
	if !errors.Is(err, ErrDiscoveryNetwork) {
		t.Fatalf("read=%v", err)
	}
	err = discoveryRequestJSON(context.Background(), fixtureBodyTransport{status: 400, body: []byte(`{"errors":[`)}, http.MethodGet, "https://api.example.invalid/x", nil, make(http.Header), &target)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed error JSON=%v", err)
	}
}

func TestDiscoveryCredentialIsolationRequestsAreBare(t *testing.T) {
	capture := &isolationCapture{}
	var token discoveryTokenResponse
	if err := discoveryTokenJSON(context.Background(), capture, "https://api.example.invalid/token?realm=go", &token); err != nil {
		t.Fatal(err)
	}
	var public discoveryTele5CMS
	if err := discoveryPublicJSON(context.Background(), capture, "https://public.example.invalid/page", &public); err != nil {
		t.Fatal(err)
	}
	if _, err := discoveryManifest(context.Background(), capture, "https://cdn.example.invalid/media.m3u8"); err != nil {
		t.Fatal(err)
	}
	for _, request := range capture.requests {
		for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Referer"} {
			if got := request.Header.Get(key); got != "" {
				t.Fatalf("%s leaked to %s: %q", key, request.URL, got)
			}
		}
	}
}

type isolationCapture struct{ requests []*http.Request }

func (capture *isolationCapture) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, io.EOF
}
func (capture *isolationCapture) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, io.EOF
}
func (capture *isolationCapture) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	capture.requests = append(capture.requests, request.Clone(request.Context()))
	switch {
	case strings.Contains(request.URL.Path, "token"):
		return discoveryHTTP(200, `{"data":{"attributes":{"token":"fixture-token"}}}`), nil
	case strings.HasSuffix(request.URL.Path, ".m3u8"):
		return discoveryHTTP(200, "#EXTM3U\n#EXTINF:1,\nsegment.ts\n"), nil
	default:
		return discoveryHTTP(200, `{"blocks":[]}`), nil
	}
}

func TestDiscoveryTele5CMSOpaqueVideoIdentity(t *testing.T) {
	transport := newDiscoveryRoutedTransport(t)
	transport.public["public.aurora.enhanced.live"] = readDiscoveryFixture(t, "tele5.json")
	root, err := NewTele5().Extract(context.Background(), Request{URL: "https://tele5.de/mediathek/star-trek/vox-sola", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), root.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if entries[0].Referer != "https://tele5.de/mediathek/star-trek/vox-sola" || !strings.HasPrefix(entries[0].URL, "discovery:tele5:") {
		t.Fatalf("entry=%#v", entries[0])
	}
	child, err := NewTele5().Extract(context.Background(), Request{URL: entries[0].URL, Referer: entries[0].Referer, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if webpage, _ := child.Info.WebpageURL(); webpage != entries[0].Referer || strings.Contains(webpage, "discovery:tele5:") {
		t.Fatalf("webpage=%q", webpage)
	}
	empty := newDiscoveryRoutedTransport(t)
	empty.public["public.aurora.enhanced.live"] = []byte(`{"blocks":[]}`)
	if _, err := NewTele5().Extract(context.Background(), Request{URL: "https://tele5.de/mediathek/star-trek/vox-sola", Transport: empty}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty Tele5=%v", err)
	}
}

func TestDiscoveryGermanCMSSuccessAndFallback(t *testing.T) {
	transport := newDiscoveryRoutedTransport(t)
	transport.public["de-api.loma-cms.com"] = readDiscoveryFixture(t, "german.json")
	result, err := NewDiscoveryNetworksDe().Extract(context.Background(), Request{URL: "https://dmax.de/sendungen/gold/german-gold", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if display, _ := result.Info.Lookup("display_id").StringValue(); display != "gold/german-gold" {
		t.Fatalf("display=%q", display)
	}
	categories, ok := result.Info.Lookup("categories").ListValue()
	if !ok || len(categories) != 1 {
		t.Fatalf("categories=%#v", categories)
	}
	fallback := newDiscoveryRoutedTransport(t)
	fallbackResult, err := NewDiscoveryNetworksDe().Extract(context.Background(), Request{URL: "https://dmax.de/sendungen/gold/german-gold", Transport: fallback})
	if err != nil {
		t.Fatal(err)
	}
	if display, _ := fallbackResult.Info.Lookup("display_id").StringValue(); display != "gold/german-gold" {
		t.Fatalf("fallback display=%q", display)
	}
	if _, ok := fallbackResult.Info.Lookup("categories").ListValue(); ok {
		t.Fatal("fallback unexpectedly invented categories")
	}
	bad := newDiscoveryRoutedTransport(t)
	bad.public["de-api.loma-cms.com"] = []byte(`{"uid":"short"}`)
	if _, err := NewDiscoveryNetworksDe().Extract(context.Background(), Request{URL: "https://dmax.de/sendungen/gold/german-gold", Transport: bad}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("short UID=%v", err)
	}
	var taxonomies strings.Builder
	taxonomies.WriteString(`{"taxonomies":[`)
	for i := 0; i <= discoveryMaxIncluded; i++ {
		if i > 0 {
			taxonomies.WriteByte(',')
		}
		taxonomies.WriteString(`{"category":"genre","title":"x"}`)
	}
	taxonomies.WriteString(`]}`)
	overflow := newDiscoveryRoutedTransport(t)
	overflow.public["de-api.loma-cms.com"] = []byte(taxonomies.String())
	if _, err := NewDiscoveryNetworksDe().Extract(context.Background(), Request{URL: "https://dmax.de/sendungen/gold/german-gold", Transport: overflow}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("taxonomy overflow=%v", err)
	}
}

func TestDiscoveryMissingCapabilitiesConcurrencyAndSecretSafety(t *testing.T) {
	if _, err := NewGoDiscovery().Extract(context.Background(), Request{URL: "https://go.discovery.com/video/a/b", Transport: basicDiscoveryTransport{}}); !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("missing capability=%v", err)
	}
	const workers = 16
	var group sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := NewGoDiscovery().Extract(context.Background(), Request{URL: "https://go.discovery.com/video/show/episode", Transport: &discoveryFixtureTransport{st: "concurrent-token"}})
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	secret := "must-not-leak"
	err := discoveryError(errors.New(secret))
	if strings.Contains(err.Error(), secret) || !errors.Is(err, ErrDiscoveryNetwork) {
		t.Fatalf("secret error=%v", err)
	}
}

func TestDiscoveryShowPaginationSeasonsReuseAndFailures(t *testing.T) {
	for _, adapter := range []DiscoveryDPlay{NewDiscoveryPlusIndiaShow(), NewDiscoveryPlusItalyShow()} {
		sequence := discoveryShowEntries{extractor: adapter, transport: showFixtureTransport{}, authentication: "Bearer fixture", plan: discoveryShowPlan{showID: "show-id", seasons: []string{"1", "2"}}}
		first, err := CollectEntries(context.Background(), sequence, 10)
		if err != nil || len(first) != 4 {
			t.Fatalf("%s first=%#v err=%v", adapter.Name(), first, err)
		}
		second, err := CollectEntries(context.Background(), sequence, 10)
		if err != nil || len(second) != 4 {
			t.Fatalf("%s reusable=%#v err=%v", adapter.Name(), second, err)
		}
		for _, entry := range first {
			if adapter.Name() == "discoveryplusindiashow" && entry.ExtractorKey != "discoveryplusindia" {
				t.Fatalf("India entry=%#v", entry)
			}
			if adapter.Name() == "discoveryplusitalyshow" && entry.ExtractorKey != "dplay" {
				t.Fatalf("Italy entry=%#v", entry)
			}
		}
	}
	scoped := discoveryShowEntries{extractor: NewDiscoveryPlusIndiaShow(), transport: showFixtureTransport{mode: "cross-season"}, authentication: "Bearer fixture", plan: discoveryShowPlan{showID: "show-id", seasons: []string{"1", "2"}}}
	if entries, err := CollectEntries(context.Background(), scoped, 10); err != nil || len(entries) != 1 {
		t.Fatalf("season-scoped identity=%#v err=%v", entries, err)
	}
	repeated := discoveryShowEntries{extractor: NewDiscoveryPlusIndiaShow(), transport: showFixtureTransport{mode: "repeated"}, authentication: "Bearer fixture", plan: discoveryShowPlan{showID: "show-id", seasons: []string{"1"}}}
	if _, err := CollectEntries(context.Background(), repeated, 10); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("repeated=%v", err)
	}
	inconsistent := discoveryShowEntries{extractor: NewDiscoveryPlusIndiaShow(), transport: showFixtureTransport{mode: "inconsistent"}, authentication: "Bearer fixture", plan: discoveryShowPlan{showID: "show-id", seasons: []string{"1"}}}
	if _, err := CollectEntries(context.Background(), inconsistent, 10); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("inconsistent=%v", err)
	}
	empty := discoveryShowEntries{extractor: NewDiscoveryPlusItalyShow(), transport: showFixtureTransport{mode: "empty"}, authentication: "Bearer fixture", plan: discoveryShowPlan{showID: "show-id", seasons: []string{"1"}}}
	if entries, err := CollectEntries(context.Background(), empty, 10); err != nil || len(entries) != 0 {
		t.Fatalf("empty=%#v err=%v", entries, err)
	}
	zeroTotal := discoveryShowEntries{extractor: NewDiscoveryPlusItalyShow(), transport: showFixtureTransport{mode: "zero-total"}, authentication: "Bearer fixture", plan: discoveryShowPlan{showID: "show-id", seasons: []string{"1"}}}
	if entries, err := CollectEntries(context.Background(), zeroTotal, 10); err != nil || len(entries) != 0 {
		t.Fatalf("zero-total=%#v err=%v", entries, err)
	}
	var options strings.Builder
	options.WriteString(`{"included":[{"attributes":{"component":{"mandatoryParams":"show.id=show-id","filters":[{"options":[`)
	for i := 0; i <= discoveryMaxIncluded; i++ {
		if i > 0 {
			options.WriteByte(',')
		}
		fmt.Fprintf(&options, `{"id":%d}`, i+1)
	}
	options.WriteString(`]}]}}}]}`)
	var oversizedCMS discoveryShowCMS
	if err := json.Unmarshal([]byte(options.String()), &oversizedCMS); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDiscoveryPlusIndiaShow().showPlan(oversizedCMS, "show"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("season limit=%v", err)
	}
	for _, mode := range []string{"page-overflow", "total-overflow"} {
		sequence := discoveryShowEntries{extractor: NewDiscoveryPlusIndiaShow(), transport: showFixtureTransport{mode: mode}, authentication: "Bearer fixture", plan: discoveryShowPlan{showID: "show-id", seasons: []string{"1"}}}
		if _, err := CollectEntries(context.Background(), sequence, 200); !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("%s=%v", mode, err)
		}
	}
	limited := (&discoveryShowEntries{extractor: NewDiscoveryPlusIndiaShow(), transport: showFixtureTransport{}, authentication: "Bearer fixture", plan: discoveryShowPlan{showID: "show-id", seasons: []string{"1"}}}).Iterator().(*discoveryShowIterator)
	limited.entriesTotal = defaultMaxPlaylistEntries
	if _, _, err := limited.Next(context.Background()); !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("entry limit=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	iterator := repeated.Iterator()
	if _, _, err := iterator.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

type showFixtureTransport struct{ mode string }

func (showFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, io.EOF
}
func (showFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, io.EOF
}
func (transport showFixtureTransport) DoWithScopedAuthorizationNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	query := request.URL.Query()
	season, page := query.Get("filter[seasonNumber]"), query.Get("page[number]")
	if season == "" {
		season = "1"
	}
	if page == "" {
		page = "1"
	}
	total, id := 1, season+"-"+page
	switch transport.mode {
	case "repeated":
		total = 2
		id = "same"
	case "inconsistent":
		if page == "1" {
			total = 2
		} else {
			total = 3
		}
	case "empty":
		return discoveryHTTP(200, `{"data":[],"meta":{"totalPages":1}}`), nil
	case "zero-total":
		return discoveryHTTP(200, `{"data":[],"meta":{"totalPages":0}}`), nil
	case "cross-season":
		return discoveryHTTP(200, `{"data":[{"id":"same","attributes":{"path":"show/same"}}],"meta":{"totalPages":1}}`), nil
	case "page-overflow":
		var body strings.Builder
		body.WriteString(`{"data":[`)
		for i := 0; i <= 100; i++ {
			if i > 0 {
				body.WriteByte(',')
			}
			fmt.Fprintf(&body, `{"id":"e%d","attributes":{"path":"show/e%d"}}`, i, i)
		}
		body.WriteString(`],"meta":{"totalPages":1}}`)
		return discoveryHTTP(200, body.String()), nil
	case "total-overflow":
		return discoveryHTTP(200, `{"data":[],"meta":{"totalPages":10001}}`), nil
	}
	body := fmt.Sprintf(`{"data":[{"id":"e%s-a","attributes":{"path":"show/e%s-a"}},{"id":"e%s-b","attributes":{"path":"show/e%s-b"}}],"meta":{"totalPages":%d}}`, id, id, id, id, total)
	return discoveryHTTP(200, body), nil
}

type basicDiscoveryTransport struct{}

func (basicDiscoveryTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, io.EOF
}
func (basicDiscoveryTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, io.EOF
}

type discoveryRoutedTransport struct {
	public                      map[string][]byte
	content, playback, manifest []byte
	headers                     []http.Header
}

func newDiscoveryRoutedTransport(t *testing.T) *discoveryRoutedTransport {
	return &discoveryRoutedTransport{public: make(map[string][]byte), content: readDiscoveryFixture(t, "content.json"), playback: readDiscoveryFixture(t, "v3-playback.json"), manifest: readDiscoveryFixture(t, "master.m3u8")}
}
func (transport *discoveryRoutedTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, io.EOF
}
func (transport *discoveryRoutedTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, io.EOF
}
func (transport *discoveryRoutedTransport) Cookies(string) ([]*http.Cookie, error) {
	return []*http.Cookie{{Name: "st", Value: "fixture-token"}}, nil
}
func (transport *discoveryRoutedTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	for key, body := range transport.public {
		if strings.Contains(request.URL.String(), key) {
			return discoveryHTTP(200, string(body)), nil
		}
	}
	if strings.HasSuffix(request.URL.Path, ".m3u8") {
		return discoveryHTTP(200, string(transport.manifest)), nil
	}
	return discoveryHTTP(404, "{}"), nil
}
func (transport *discoveryRoutedTransport) DoWithScopedAuthorizationNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.headers = append(transport.headers, request.Header.Clone())
	if strings.Contains(request.URL.Path, "videoPlaybackInfo") {
		return discoveryHTTP(200, string(transport.playback)), nil
	}
	return discoveryHTTP(200, string(transport.content)), nil
}

type fixtureBodyTransport struct {
	status  int
	body    []byte
	readErr error
}

func (f fixtureBodyTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, io.EOF
}
func (f fixtureBodyTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, io.EOF
}
func (f fixtureBodyTransport) DoWithoutCredentialsNoRedirect(context.Context, *http.Request) (*http.Response, error) {
	return f.response(), nil
}
func (f fixtureBodyTransport) DoWithScopedAuthorizationNoRedirect(context.Context, *http.Request) (*http.Response, error) {
	return f.response(), nil
}
func (f fixtureBodyTransport) response() *http.Response {
	status := f.status
	if status == 0 {
		status = 200
	}
	reader := io.Reader(bytes.NewReader(f.body))
	if f.readErr != nil {
		reader = errorReader{err: f.readErr}
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(reader)}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

func TestDiscoveryHLSMediaPlaylistIsAFormat(t *testing.T) {
	transport := &discoveryFixtureTransport{}
	formats, _, err := discoveryHLSManifest(context.Background(), transport, "https://cdn.example.invalid/media.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if len(formats) != 1 {
		t.Fatalf("formats = %d, want media-playlist fallback", len(formats))
	}
}

func TestDiscoveryRejectsEmptyHLSMediaPlaylist(t *testing.T) {
	_, _, err := discoveryHLSManifest(context.Background(), &discoveryFixtureTransport{}, "https://cdn.example.invalid/empty.m3u8")
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("error = %v", err)
	}
}

func TestDiscoveryTele5InternalReentryIsNotPublicNumericRoute(t *testing.T) {
	adapter := NewTele5()
	public, _ := url.Parse("https://tele5.de/anything/12345")
	target, ok := adapter.target(public)
	if !ok || target.canonical == "" {
		t.Fatalf("public target = %#v, %v", target, ok)
	}
	internal, _ := url.Parse("discovery:tele5:12345")
	target, ok = adapter.target(internal)
	if !ok || target.canonical != "" || target.displayID != "12345" {
		t.Fatalf("internal target = %#v, %v", target, ok)
	}
}

type discoveryFixtureTransport struct {
	st             string
	tokenRequests  int
	tokenURLs      []string
	authorizations []string
}

func (transport *discoveryFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, io.EOF
}
func (transport *discoveryFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, io.EOF
}
func (transport *discoveryFixtureTransport) Cookies(string) ([]*http.Cookie, error) {
	if transport.st == "" {
		return nil, nil
	}
	return []*http.Cookie{{Name: "st", Value: transport.st}}, nil
}
func (transport *discoveryFixtureTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/empty.m3u8") {
		return discoveryHTTP(http.StatusOK, "#EXTM3U\n"), nil
	}
	if strings.HasSuffix(request.URL.Path, "/media.m3u8") {
		return discoveryHTTP(http.StatusOK, "#EXTM3U\n#EXTINF:1,\nsegment.ts\n"), nil
	}
	if strings.HasSuffix(request.URL.Path, ".m3u8") {
		return discoveryHTTP(http.StatusOK, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nvideo.m3u8\n"), nil
	}
	transport.tokenRequests++
	transport.tokenURLs = append(transport.tokenURLs, request.URL.String())
	return discoveryHTTP(http.StatusOK, `{"data":{"attributes":{"token":"fresh-token"}}}`), nil
}
func (transport *discoveryFixtureTransport) DoWithScopedAuthorizationNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.authorizations = append(transport.authorizations, request.Header.Get("Authorization"))
	if strings.Contains(request.URL.Path, "videoPlaybackInfo") {
		if request.Method == http.MethodPost {
			return discoveryHTTP(http.StatusOK, `{"data":{"attributes":{"streaming":[{"type":"hls","url":"https://cdn.example.invalid/master.m3u8"}]}}}`), nil
		}
		return discoveryHTTP(http.StatusOK, `{"data":{"attributes":{"streaming":{"hls":{"url":"https://cdn.example.invalid/master.m3u8"}}}}}`), nil
	}
	return discoveryHTTP(http.StatusOK, `{"data":{"id":"video-1","attributes":{"name":"Fixture episode","description":"fixture","videoDuration":1200,"seasonNumber":1,"episodeNumber":2,"publishStart":"2020-01-01T00:00:00Z"}},"included":[{"type":"show","attributes":{"name":"Fixture Show"}}]}`), nil
}
func discoveryHTTP(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(body))}
}

func FuzzDiscoveryDPlayRouting(f *testing.F) {
	for _, seed := range []string{"https://ahctv.com/video/show/episode", "https://animalplanet.com/video/show/episode", "https://watch.cookingchanneltv.com/video/show/episode", "https://dplay.no/videoer/show/episode", "https://dmax.de/sendungen/show/episode", "https://discoveryplus.com/gb/video/show/episode", "https://discoveryplus.in/show/a-show", "https://discoveryplus.it/programmi/a-show", "https://de.hgtv.com/sendungen/show/episode", "https://go.discovery.com/video/show/episode", "https://tele5.de/mediathek/show/episode"} {
		f.Add(seed)
	}
	f.Add("https://go.discovery.com/video/a%2fb")
	f.Fuzz(func(t *testing.T, rawURL string) {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		for _, original := range []DiscoveryDPlay{NewAmHistoryChannel(), NewAnimalPlanet(), NewCookingChannel(), NewDPlay(), NewDestinationAmerica(), NewDiscoveryLife(), NewDiscoveryNetworksDe(), NewDiscoveryPlus(), NewDiscoveryPlusIndia(), NewDiscoveryPlusIndiaShow(), NewDiscoveryPlusItaly(), NewDiscoveryPlusItalyShow(), NewFoodNetwork(), NewGoDiscovery(), NewHGTVDe(), NewHGTVUsa(), NewInvestigationDiscovery(), NewScienceChannel(), NewTLC(), NewTravelChannel(), NewTele5()} {
			adapter := original
			adapter.config = adapter.configFor(parsed)
			target, ok := adapter.target(parsed)
			if !ok {
				continue
			}
			if len(target.displayID) == 0 || len(target.displayID) > discoveryMaxIDBytes {
				t.Fatalf("%s accepted unbounded target %#v", adapter.Name(), target)
			}
			if target.canonical != "" && (!strictValidHostedHTTPURL(target.canonical) || !discoveryHostMatches(strings.ToLower(parsed.Hostname()), adapter.config)) {
				t.Fatalf("%s accepted unsafe canonical target %#v", adapter.Name(), target)
			}
		}
	})
}

func FuzzDiscoveryPlaybackSchemas(f *testing.F) {
	f.Add([]byte(`{"data":{"attributes":{"streaming":[{"type":"hls","url":"https://cdn.example.invalid/x.m3u8"}]}}}`))
	f.Add([]byte(`{"data":{"attributes":{"streaming":{"hls":{"url":"https://cdn.example.invalid/x.m3u8"}}}}}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		var playback discoveryPlaybackResponse
		err := json.Unmarshal(payload, &playback)
		if err == nil {
			if len(playback.Streaming) > discoveryMaxStreaming {
				t.Fatalf("stream overflow %d", len(playback.Streaming))
			}
			for _, stream := range playback.Streaming {
				if len(stream.Type) > len(payload) || len(stream.URL) > len(payload) {
					t.Fatal("unbounded stream")
				}
			}
		}
	})
}

func FuzzDiscoveryShowPageIdentity(f *testing.F) {
	f.Add([]byte(`{"data":[{"id":"e1","attributes":{"path":"show/episode"}}],"meta":{"totalPages":1}}`))
	f.Add([]byte(`{"data":[],"meta":{"totalPages":1}}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		var page discoveryShowPage
		if json.Unmarshal(payload, &page) != nil {
			return
		}
		fingerprint, err := discoveryShowPageFingerprint(page)
		if err == nil && (len(fingerprint) == 0 || len(fingerprint) > 8192) {
			t.Fatalf("fingerprint length %d", len(fingerprint))
		}
	})
}

func FuzzDiscoveryManifestPolicy(f *testing.F) {
	f.Add([]byte("#EXTM3U\n#EXTINF:1,\nsegment.ts\n"), false)
	f.Add([]byte(`<MPD><Period><AdaptationSet contentType="video"><Representation id="v"><BaseURL>v.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`), true)
	f.Fuzz(func(t *testing.T, payload []byte, dashManifest bool) {
		if len(payload) > discoveryMaxManifestBytes+1 {
			return
		}
		var formats []value.Value
		var err error
		if dashManifest {
			formats, _, err = discoveryDASHManifest(context.Background(), fixtureBodyTransport{body: payload}, "https://cdn.example.invalid/master.mpd")
		} else {
			formats, _, err = discoveryHLSManifest(context.Background(), fixtureBodyTransport{body: payload}, "https://cdn.example.invalid/master.m3u8")
		}
		if err == nil && (len(formats) == 0 || len(formats) > discoveryMaxStreaming) {
			t.Fatalf("format bound=%d", len(formats))
		}
	})
}

func FuzzDiscoveryTokenContentAndErrorPolicy(f *testing.F) {
	f.Add([]byte(`{"data":{"attributes":{"token":"fixture-token"}}}`))
	f.Add([]byte(`{"data":{"id":"video-1","attributes":{"name":"Fixture"}}}`))
	f.Add([]byte(`{"errors":[{"detail":"must-not-leak"}]}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 1<<20 {
			return
		}
		var token discoveryTokenResponse
		if json.Unmarshal(payload, &token) == nil && discoveryToken(token.Data.Attributes.Token) {
			if len(token.Data.Attributes.Token) > discoveryMaxTokenBytes || strings.ContainsAny(token.Data.Attributes.Token, " \t\r\n") {
				t.Fatal("accepted unsafe token")
			}
		}
		var content discoveryContentResponse
		if json.Unmarshal(payload, &content) == nil && discoverySegment(content.Data.ID) && len(content.Included) <= discoveryMaxIncluded && strings.TrimSpace(content.Data.Attributes.Name) != "" {
			playback := discoveryPlaybackResponse{Streaming: []discoveryStream{{Type: "http", URL: "https://cdn.example.invalid/video.mp4"}}}
			result, err := NewGoDiscovery().media(context.Background(), manifestMatrixTransport{}, content, playback, discoveryTarget{displayID: "show/episode", canonical: "https://go.discovery.com/video/show/episode"})
			if err == nil {
				id, _ := result.Info.ID()
				formats, _ := result.Info.Formats()
				if !discoverySegment(id) || len(formats) == 0 || len(formats) > discoveryMaxFormats {
					t.Fatalf("unsafe successful content id=%q formats=%d", id, len(formats))
				}
			}
		}
		var target any
		err := discoveryError(discoveryRequestJSON(context.Background(), fixtureBodyTransport{status: 400, body: payload}, http.MethodGet, "https://api.example.invalid/error", nil, make(http.Header), &target))
		if err != nil && (len(err.Error()) > 512 || bytes.Contains(payload, []byte("must-not-leak")) && strings.Contains(err.Error(), "must-not-leak")) {
			t.Fatalf("unsafe error %q", err)
		}
	})
}

func FuzzDiscoveryCMSPolicy(f *testing.F) {
	f.Add([]byte(`{"blocks":[{"videoId":"4140114"}]}`), byte(0))
	f.Add([]byte(`{"uid":"synthetic-4756322","taxonomies":[{"category":"genre","title":"Gold"}]}`), byte(1))
	f.Add([]byte(`{"included":[]}`), byte(2))
	f.Fuzz(func(t *testing.T, payload []byte, mode byte) {
		if len(payload) > 1<<20 {
			return
		}
		switch mode % 3 {
		case 0:
			var cms discoveryTele5CMS
			if json.Unmarshal(payload, &cms) == nil && len(cms.Blocks) <= discoveryMaxIncluded {
				seen := make(map[string]bool)
				for _, block := range cms.Blocks {
					if discoveryTele5ID(block.VideoID) {
						seen[block.VideoID] = true
					}
				}
				if len(seen) > discoveryMaxIncluded {
					t.Fatal("Tele5 entry overflow")
				}
			}
		case 1:
			var cms discoveryGermanCMS
			if json.Unmarshal(payload, &cms) == nil && len(cms.Taxonomies) <= discoveryMaxIncluded {
				categories := 0
				for _, taxonomy := range cms.Taxonomies {
					if taxonomy.Category == "genre" && strings.TrimSpace(taxonomy.Title) != "" {
						categories++
					}
				}
				if categories > discoveryMaxIncluded {
					t.Fatal("German category overflow")
				}
			}
		case 2:
			var cms discoveryShowCMS
			if json.Unmarshal(payload, &cms) == nil {
				for _, adapter := range []DiscoveryDPlay{NewDiscoveryPlusIndiaShow(), NewDiscoveryPlusItalyShow()} {
					if plan, err := adapter.showPlan(cms, "show"); err == nil && (len(plan.seasons) == 0 || len(plan.seasons) > discoveryMaxIncluded || !discoverySegment(plan.showID)) {
						t.Fatalf("unsafe show plan %#v", plan)
					}
				}
			}
		}
	})
}
