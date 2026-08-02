package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

type publicExtractorTransport struct {
	pages map[string][]byte
	api   func(context.Context, *http.Request) (int, []byte, error)
}

func (transport *publicExtractorTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if transport.api == nil {
		return publicExtractorResponse(http.StatusNotFound, nil), nil
	}
	status, body, err := transport.api(ctx, request)
	if err != nil {
		return nil, err
	}
	return publicExtractorResponse(status, body), nil
}

func (transport *publicExtractorTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	body, ok := transport.pages[rawURL]
	if !ok {
		return nil, nil, errors.New("unexpected public fixture page")
	}
	return append([]byte(nil), body...), make(http.Header), nil
}

func publicExtractorResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

func readPublicFixture(t testing.TB, site, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../../conformance/extractors/public/" + site + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDailymotionPublicMetadata(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	result, err := NewDailymotion().Extract(context.Background(), Request{
		URL: "https://dai.ly/xfixture?utm_source=fixture", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() || result.IsURL() {
		t.Fatalf("unexpected result kind: %#v", result)
	}
	for key, want := range map[string]string{
		"id": "xfixture", "title": "Fixture Daily", "description": "GraphQL public description",
		"uploader": "Fixture uploader", "uploader_id": "xowner1",
	} {
		if got, _ := result.Info.Lookup(key).StringValue(); got != want {
			t.Fatalf("%s=%q want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{"timestamp": 1700000000, "view_count": 4567, "like_count": 23, "age_limit": 0} {
		if got, _ := result.Info.Lookup(key).Int(); got != want {
			t.Fatalf("%s=%d want %d", key, got, want)
		}
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats=%d", len(formats))
	}
	for _, format := range formats {
		object, _ := format.Object()
		isolated, _ := object.Lookup("_credential_isolated").Bool()
		policy, _ := object.Lookup("_host_policy").StringValue()
		rawURL, _ := object.Lookup("url").StringValue()
		if !isolated || policy != dailymotionHostPolicy || strings.Contains(rawURL, "#discarded") {
			t.Fatalf("format=%#v", object)
		}
	}
	subtitles, _ := result.Info.Lookup("subtitles").Object()
	english, _ := subtitles.Lookup("en").ListValue()
	if len(english) != 1 {
		t.Fatalf("subtitles=%#v", subtitles)
	}
	track, _ := english[0].Object()
	if policy, _ := track.Lookup("_host_policy").StringValue(); policy != dailymotionHostPolicy {
		t.Fatalf("subtitle policy=%q", policy)
	}
	thumbnails, _ := result.Info.Lookup("thumbnails").ListValue()
	if len(thumbnails) != 4 {
		t.Fatalf("thumbnails=%d", len(thumbnails))
	}
	if len(transport.tokenRequests) != 1 || len(transport.graphQLBodies) != 1 {
		t.Fatalf("token=%d graphql=%d", len(transport.tokenRequests), len(transport.graphQLBodies))
	}
	var payload struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(transport.graphQLBodies[0], &payload) != nil || !strings.Contains(payload.Query, `media(xid: "xfixture")`) {
		t.Fatalf("media payload=%s", transport.graphQLBodies[0])
	}
}

func TestDailymotionPlaylistAliasReentersRegisteredExtractor(t *testing.T) {
	for _, rawURL := range []string{
		"https://www.dailymotion.com/video/xfixture?playlist=xlist01",
		"https://geo.dailymotion.com/player/x86gw.html?playlist=xlist01&mute=true",
	} {
		result, err := NewDailymotion().Extract(context.Background(), Request{URL: rawURL, Transport: &publicExtractorTransport{}})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "dailymotion_playlist" || result.Redirect.ID != "xlist01" {
			t.Fatalf("%s result=%#v err=%v", rawURL, result, err)
		}
	}
}

func TestDailymotionNoPlaylistAmbiguousURLChoice(t *testing.T) {
	rawURL := "https://www.dailymotion.com/video/xfixture?playlist=xlist01&sig=a%2Bb&token=keep"

	t.Run("default-prefers-playlist", func(t *testing.T) {
		transport := dailymotionDiscoveryFixture(t)
		result, err := NewDailymotion().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "dailymotion_playlist" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		if result.Redirect.URL != "https://www.dailymotion.com/playlist/xlist01" {
			t.Fatalf("redirect=%q", result.Redirect.URL)
		}
		if len(transport.tokenRequests) != 0 || len(transport.graphQLBodies) != 0 {
			t.Fatalf("discarded video branch made requests: tokens=%d graphql=%d", len(transport.tokenRequests), len(transport.graphQLBodies))
		}
	})

	t.Run("no-playlist-prefers-video-and-preserves-query", func(t *testing.T) {
		transport := dailymotionDiscoveryFixture(t)
		result, err := NewDailymotion().Extract(context.Background(), Request{URL: rawURL, Transport: transport, NoPlaylist: true})
		if err != nil || result.IsURL() || result.IsPlaylist() {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		if webpage, _ := result.Info.Lookup("webpage_url").StringValue(); webpage != rawURL {
			t.Fatalf("webpage_url=%q want exact %q", webpage, rawURL)
		}
		if len(transport.tokenRequests) != 1 || len(transport.graphQLBodies) != 1 {
			t.Fatalf("video requests: tokens=%d graphql=%d", len(transport.tokenRequests), len(transport.graphQLBodies))
		}
	})

	t.Run("playlist-only-player-remains-playlist", func(t *testing.T) {
		result, err := NewDailymotion().Extract(context.Background(), Request{
			URL: "https://geo.dailymotion.com/player/x86gw.html?playlist=xlist01&sig=a%2Bb", Transport: dailymotionDiscoveryFixture(t), NoPlaylist: true,
		})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "dailymotion_playlist" {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
}

func TestDailymotionFailuresIsolationAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		body   []byte
		status int
		want   error
	}{
		{name: "geo", body: []byte(`{"error":{"code":"DM007"}}`), want: ErrRegionRestricted},
		{name: "private", body: []byte(`{"error":{"title":"private"}}`), want: ErrAuthentication},
		{name: "age", body: []byte(`{"title":"Age","explicit":true,"qualities":{"auto":[{"url":"https://stream-01.dmcdn.net/a.m3u8","type":"application/x-mpegURL"}]}}`), want: ErrDailymotionAgeRestricted},
		{name: "malformed", body: []byte(`{"title":`), want: ErrInvalidMetadata},
		{name: "redirect", status: http.StatusFound, want: ErrDailymotionRedirect},
		{name: "not-found", status: http.StatusNotFound, want: ErrUnavailable},
		{name: "rate", status: http.StatusTooManyRequests, want: ErrDailymotionRateLimited},
		{name: "legal", status: http.StatusUnavailableForLegalReasons, want: ErrRegionRestricted},
		{name: "server", status: http.StatusServiceUnavailable, want: ErrDailymotionNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := dailymotionDiscoveryFixture(t)
			transport.metadataBodies["xfixture"] = test.body
			transport.metadataStatus["xfixture"] = test.status
			_, err := NewDailymotion().Extract(context.Background(), Request{URL: "https://www.dailymotion.com/video/xfixture", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
			for _, secret := range []string{dailymotionAnonymousClientSecret, "fixture-dailymotion-token", "discarded"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
	_, err := NewDailymotion().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/video/xfixture", Transport: &publicExtractorTransport{},
	})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("isolation=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewDailymotion().Extract(ctx, Request{URL: "https://www.dailymotion.com/video/xfixture", Transport: dailymotionDiscoveryFixture(t)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestDailymotionRoutesAndExactNonOverlap(t *testing.T) {
	videoRoutes := []string{
		"http://www.dailymotion.com/video/xfixture_fixture-slug",
		"https://touch.dailymotion.fr/VIDEO/xfixture",
		"https://www.dailymotion.com/swf/video/xfixture",
		"https://www.dailymotion.com/swf/xfixture_slug",
		"https://www.dailymotion.com/crawler/video/xfixture",
		"https://www.dailymotion.com/embed/video/xfixture",
		"https://www.lequipe.fr/video/k7MtHciueyTcrFtFKA2",
		"https://geo.dailymotion.com/player.html?video=xfixture&mute=true",
		"https://geo.dailymotion.com/player/x86gw.html?video=xfixture&customConfig%5Bfoo%5D=bar",
		"https://dai.ly/xfixture",
		"https://www.dailymotion.com/video/xfixture?playlist=xlist01",
		"https://geo.dailymotion.com/player/x86gw.html?playlist=xlist01",
	}
	playlistRoutes := []string{
		"http://www.dailymotion.com/playlist/xlist01",
		"https://dailymotion.fr/playlist/xlist01_fixture-slug/1#video=xfixture",
	}
	registry := NewRegistry(NewDailymotionPlaylist(), NewDailymotionSearch(), NewDailymotionUser(), NewDailymotion())
	for _, rawURL := range videoRoutes {
		parsed, err := url.Parse(rawURL)
		if err != nil || !NewDailymotion().Suitable(parsed) || NewDailymotionPlaylist().Suitable(parsed) || NewDailymotionSearch().Suitable(parsed) || NewDailymotionUser().Suitable(parsed) {
			t.Fatalf("video overlap %q parsed=%v err=%v", rawURL, parsed, err)
		}
		selected, err := registry.Select(rawURL)
		if err != nil || selected.Name() != "dailymotion" {
			t.Fatalf("Select(%q)=%v err=%v", rawURL, selected, err)
		}
	}
	for _, rawURL := range playlistRoutes {
		parsed, err := url.Parse(rawURL)
		if err != nil || !NewDailymotionPlaylist().Suitable(parsed) || NewDailymotion().Suitable(parsed) || NewDailymotionSearch().Suitable(parsed) || NewDailymotionUser().Suitable(parsed) {
			t.Fatalf("playlist overlap %q parsed=%v err=%v", rawURL, parsed, err)
		}
	}
}

func TestDailymotionRouteRejections(t *testing.T) {
	for _, rawURL := range []string{
		"ftp://www.dailymotion.com/video/xfixture",
		"https://user@www.dailymotion.com/video/xfixture",
		"https://www.dailymotion.com:443/video/xfixture",
		"https://foo.dailymotion.com/video/xfixture",
		"https://www.notdailymotion.com/video/xfixture",
		"https://www.dailymotion.com./video/xfixture",
		"https://www.dailymotion.com/video/xfixture/extra",
		"https://www.dailymotion.com/video/xfixture/",
		"https://www.dailymotion.com/video/x%2ffixture",
		"https://www.dailymotion.com/video/xfixture#fragment",
		"https://www.dailymotion.com/video/xfixture?playlist=xlist01&playlist=xlist02",
		"https://geo.dailymotion.com/player.html?video=xfixture&video=xother",
		"https://geo.dailymotion.com/player.html?video=xfixture&playlist=xlist01",
		"https://geo.dailymotion.com/player.html?mute=true&video=xfixture",
		"https://geo.dailymotion.com/player.html?mute=true",
		"https://geo.dailymotion.com/player/a-b.html?video=xfixture",
		"https://dai.ly/xfixture/extra",
		"https://www.dailymotion.com/playlist/xlist01",
	} {
		parsed, err := url.Parse(rawURL)
		if err == nil && NewDailymotion().Suitable(parsed) {
			t.Fatalf("unexpectedly suitable %q", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://geo.dailymotion.com/playlist/xlist01",
		"https://www.dailymotion.com/playlist/Xlist01",
		"https://www.dailymotion.com/playlist/x",
		"https://www.dailymotion.com/playlist/xlist01/extra",
		"https://www.dailymotion.com/playlist/xlist01?x=1",
		"https://www.dailymotion.com/playlist/xlist01#video=xfixture&video=xother",
		"https://www.dailymotion.com/playlist/xlist01#other=xfixture",
		"https://www.dailymotion.com/playlist/xlist01/1#video=x%2ffixture",
	} {
		parsed, err := url.Parse(rawURL)
		if err == nil && NewDailymotionPlaylist().Suitable(parsed) {
			t.Fatalf("unexpected playlist %q", rawURL)
		}
	}
}

func TestDailymotionAttributableURLPolicies(t *testing.T) {
	for _, test := range []struct {
		rawURL string
		role   string
		want   bool
	}{
		{"https://stream-01.dmcdn.net/a/master.m3u8?sig=a%2Bb&token=1", "manifest", true},
		{"https://stream-01.dmcdn.net/a/master.m3u8?sig=a%2Bb&token=1", "playback", true},
		{"https://proxy-01.dailymotion.com/a/segment.ts?sig=a%2Bb", "segment", true},
		{"https://proxy-01.dailymotion.com/a/segment.ts?sig=a%2Bb", "playback", true},
		{"https://proxy-01.dailymotion.com/a/video.mp4?sig=a%2Bb", "media", true},
		{"https://s1.dmcdn.net/a/thumb.jpg?sig=a%2Bb", "thumbnail", true},
		{"https://www.dailymotion.com/video/xfixture", "page", true},
		{"https://s1.dmcdn.net/a/page.html", "page", false},
		{"https://proxy-01.dailymotion.com/a/thumb.jpg", "thumbnail", false},
		{"http://s1.dmcdn.net/a/video.mp4", "media", false},
		{"https://evil.invalid/a/video.mp4", "media", false},
		{"https://dmcdn.net.evil.invalid/a/video.mp4", "media", false},
		{"https://user@s1.dmcdn.net/a/video.mp4", "media", false},
		{"https://s1.dmcdn.net:443/a/video.mp4", "media", false},
		{"https://s1.dmcdn.net/a/video.mp4#fragment", "media", false},
	} {
		if got := DailymotionAttributableURL(test.rawURL, test.role); got != test.want {
			t.Fatalf("DailymotionAttributableURL(%q,%q)=%t want %t", test.rawURL, test.role, got, test.want)
		}
	}
}

func FuzzNormalizeDailymotion(f *testing.F) {
	f.Add(readPublicFixture(f, "dailymotion", "success.json"))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var metadata dailymotionMetadata
		_ = json.Unmarshal(data, &metadata)
		_, _ = normalizeDailymotion(metadata, dailymotionMedia{XID: "xfixture"}, "xfixture", "https://www.dailymotion.com/video/xfixture")
	})
}
