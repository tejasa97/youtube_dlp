package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestYouTubeShowRendererEmitsPlaylist(t *testing.T) {
	data := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":[
		{"showRenderer":{"playlistId":"PLshow000001","title":{"simpleText":"Show"}}},
		{"gridShowRenderer":{"playlistId":"PLshow000002","title":{"simpleText":"Grid Show"}}}
	]}}]}}}}]}}}`)
	page, err := parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererPlaylist})
	if err != nil || len(page.entries) != 2 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if page.entries[0].ID != "PLshow000001" || page.entries[1].ID != "PLshow000002" {
		t.Fatalf("entries=%#v", page.entries)
	}
}

func TestYouTubeRendererAvailabilityBadges(t *testing.T) {
	data := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":[
		{"videoRenderer":{"videoId":"aaaaaaaaaaa","badges":[{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_MEMBERS_ONLY","label":{"simpleText":"Members only"}}}]}},
		{"videoRenderer":{"videoId":"bbbbbbbbbbb","badges":[{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_PRIVATE"}}}]}},
		{"videoRenderer":{"videoId":"ccccccccccc","badges":[{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_PREMIUM"}}]}},
		{"videoRenderer":{"videoId":"ddddddddddd","badges":[{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_UNLISTED"}}}]}}
	]}}]}}}}]}}}`)
	page, err := parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererVideo})
	if err != nil || len(page.entries) != 4 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	want := []string{"subscriber_only", "private", "premium", "unlisted"}
	for i, availability := range want {
		if page.entries[i].Availability != availability {
			t.Fatalf("entry[%d].Availability=%q want %q", i, page.entries[i].Availability, availability)
		}
		object := page.entries[i].Object()
		if got, _ := object.Lookup("availability").StringValue(); got != availability {
			t.Fatalf("object availability=%q", got)
		}
	}
}

func TestYouTubePlaylistCountMetadata(t *testing.T) {
	data := []byte(`{
		"header":{"playlistHeaderRenderer":{"title":{"simpleText":"Counted"},"viewCountText":{"simpleText":"1,234 views"},"byline":[{"playlistBylineRenderer":{"text":{"simpleText":"42 videos"}}}]}},
		"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":[
			{"playlistVideoRenderer":{"videoId":"aaaaaaaaaaa"}}
		]}}]}}}}]}}
	}`)
	page, err := parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererVideo})
	if err != nil {
		t.Fatal(err)
	}
	if !page.hasCount || page.playlistCount != 42 || !page.hasViewCount || page.viewCount != 1234 {
		t.Fatalf("counts=%#v", page)
	}
	parsed, err := parseYouTubePlaylistData(data)
	if err != nil || !parsed.hasCount || parsed.playlistCount != 42 || !parsed.hasViewCount || parsed.viewCount != 1234 {
		t.Fatalf("playlist page=%#v err=%v", parsed, err)
	}
}

func TestYouTubeParseCountText(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{"42 videos", 42, true},
		{"1,234 views", 1234, true},
		{"1.5K views", 1500, true},
		{"no views", 0, true},
		{"", 0, false},
		{strings.Repeat("9", 80), 0, false},
	}
	for _, test := range cases {
		got, ok := youtubeParseCountText(test.raw)
		if ok != test.ok || (ok && got != test.want) {
			t.Fatalf("%q => %d,%t want %d,%t", test.raw, got, ok, test.want, test.ok)
		}
	}
}

func TestYouTubeHashtagExtractsLazyPlaylist(t *testing.T) {
	page := []byte(`ytcfg.set({"INNERTUBE_API_KEY":"k","INNERTUBE_CLIENT_VERSION":"v","VISITOR_DATA":"vis"});ytInitialData={"header":{"hashtagHeaderRenderer":{"hashtag":{"simpleText":"#cats"}}},"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"richGridRenderer":{"contents":[{"richItemRenderer":{"content":{"videoRenderer":{"videoId":"aaaaaaaaaaa","title":{"simpleText":"one"}}}}},{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"next"}}}}]}}}}]}},"responseContext":{"visitorData":"vis"}};`)
	continuation := []byte(`{"responseContext":{"visitorData":"vis2"},"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[{"videoRenderer":{"videoId":"bbbbbbbbbbb"}}]}}]}`)
	transport := &hashtagFixtureTransport{page: page, continuation: continuation}
	result, err := NewYouTubeHashtag().Extract(context.Background(), Request{
		URL: "https://www.youtube.com/hashtag/cats", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if title, _ := result.Info.Lookup("title").StringValue(); title != "#cats" {
		t.Fatalf("title=%q", title)
	}
	entries := collectEntries(t, result.Entries)
	if len(entries) != 2 || entries[0].ID != "aaaaaaaaaaa" || entries[1].ID != "bbbbbbbbbbb" {
		t.Fatalf("entries=%#v", entries)
	}
}

func TestYouTubeHashtagRejectsHostileURLs(t *testing.T) {
	for _, raw := range []string{
		"https://evil.com/hashtag/cats",
		"https://www.youtube.com/hashtag/cats/extra",
		"https://www.youtube.com/hashtag/cats?x=1",
		"https://www.youtube.com/hashtag/cat%2fs",
		"https://m.youtube.com/hashtag/cats",
	} {
		if NewYouTubeHashtag().Suitable(mustURL(t, raw)) {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestYouTubeHashtagUnavailableAlert(t *testing.T) {
	page := []byte(`ytcfg.set({"INNERTUBE_CLIENT_VERSION":"v"});ytInitialData={"alerts":[{"alertRenderer":{"text":{"simpleText":"This hashtag is unavailable"}}}]};`)
	_, err := NewYouTubeHashtag().Extract(context.Background(), Request{
		URL: "https://www.youtube.com/hashtag/cats", Transport: &hashtagFixtureTransport{page: page},
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

type hashtagFixtureTransport struct {
	page         []byte
	continuation []byte
}

func (transport *hashtagFixtureTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	if request.URL.Path != "/youtubei/v1/browse" {
		return nil, errors.New("unexpected path " + request.URL.Path)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(transport.continuation)),
		Request:    request,
	}, nil
}

func (transport *hashtagFixtureTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	if !strings.HasPrefix(rawURL, "https://www.youtube.com/hashtag/") {
		return nil, nil, errors.New("unexpected page " + rawURL)
	}
	return transport.page, make(http.Header), nil
}

func (transport *hashtagFixtureTransport) DoWithoutCookies(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.Do(ctx, request)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func collectEntries(t *testing.T, sequence EntrySequence) []Entry {
	t.Helper()
	if sequence == nil {
		return nil
	}
	var entries []Entry
	iterator := sequence.Iterator()
	for {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return entries
		}
		entries = append(entries, entry)
	}
}
