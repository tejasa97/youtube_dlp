package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
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
		{"videoRenderer":{"videoId":"ddddddddddd","badges":[{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_UNLISTED"}}}]}},
		{"videoRenderer":{"videoId":"eeeeeeeeeee","badges":[{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_PUBLIC"}}}]}}
	]}}]}}}}]}}}`)
	page, err := parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererVideo})
	if err != nil || len(page.entries) != 5 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	want := []string{"subscriber_only", "private", "premium_only", "unlisted", "public"}
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

func TestYouTubeRendererAvailabilityPrecedenceIsOrderIndependent(t *testing.T) {
	cases := []struct {
		name   string
		badges string
		want   string
	}{
		{
			name:   "public then private",
			badges: `[{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_PUBLIC"}}},{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_PRIVATE"}}}]`,
			want:   "private",
		},
		{
			name:   "private then public",
			badges: `[{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_PRIVATE"}}},{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_PUBLIC"}}}]`,
			want:   "private",
		},
		{
			name:   "unlisted then premium then subscriber",
			badges: `[{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_UNLISTED"}}},{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_PREMIUM"}},{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_MEMBERS_ONLY"}}]`,
			want:   "premium_only",
		},
		{
			name:   "subscriber then unlisted",
			badges: `[{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_MEMBERS_ONLY"}},{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_UNLISTED"}}}]`,
			want:   "subscriber_only",
		},
		{
			name:   "unknown ignored with unlisted",
			badges: `[{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_UNKNOWN","label":{"simpleText":"New"}}},{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_UNLISTED"}}}]`,
			want:   "unlisted",
		},
		{
			name:   "conflicting private wins over premium",
			badges: `[{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_PREMIUM"}},{"metadataBadgeRenderer":{"label":{"simpleText":"private"}}}]`,
			want:   "private",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":[
		{"videoRenderer":{"videoId":"aaaaaaaaaaa","badges":` + test.badges + `}}
	]}}]}}}}]}}}`)
			page, err := parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererVideo})
			if err != nil || len(page.entries) != 1 {
				t.Fatalf("page=%#v err=%v", page, err)
			}
			if page.entries[0].Availability != test.want {
				t.Fatalf("Availability=%q want %q", page.entries[0].Availability, test.want)
			}
		})
	}
}

func TestYouTubeRendererAvailabilityOmitsOnParserLimit(t *testing.T) {
	deep := strings.Repeat(`{"nested":`, youtubeMaxJSONDepth+2) + `{"metadataBadgeRenderer":{"icon":{"iconType":"PRIVACY_PRIVATE"}}}` + strings.Repeat(`}`, youtubeMaxJSONDepth+2)
	raw := []byte(`{"videoId":"aaaaaaaaaaa","badges":[` + deep + `]}`)
	var root value.Value
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	object, ok := root.Object()
	if !ok {
		t.Fatal("expected object")
	}
	if got := youtubeRendererAvailability(object); got != "" {
		t.Fatalf("parser-limit must not yield positive availability, got %q", got)
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
		{"42  videos", 42, true},
		{"42\u00a0videos", 42, true},
		{"1,234 views", 1234, true},
		{"12,345,678", 12_345_678, true},
		{"1.5K views", 1500, true},
		{"1.5M", 1_500_000, true},
		{"42", 42, true},
		{"2kk", 2_000_000, true},
		{"no views", 0, true},
		{"no videos", 0, true},
		{"No Views", 0, true},
		{"Views: 99", 99, true},
		{"42videos", 0, false},
		{"1,234views", 0, false},
		{"1.5Kviews", 0, false},
		{"42views!", 0, false},
		{"42, videos", 0, false},
		{"not no views really", 0, false},
		{"token=no views", 0, false},
		{"no views extra", 0, false},
		{"", 0, false},
		{strings.Repeat("9", 80), 0, false},
		{"1 foo 2", 0, false},
		{"1.5", 0, false},
		{"1.5x", 0, false},
		{"12kfoo", 0, false},
		{"1kfoo", 0, false},
		{"123foobar", 0, false},
		{"1,2", 0, false},
		{"12,34", 0, false},
		{"1,,234", 0, false},
		{"1..5k", 0, false},
		{"1\u202f000", 0, false},
		{"1.5B views", 1_500_000_000, true},
		{"١٢٣", 0, false},
		{"1 000", 0, false},
		{"9223372036855B", 0, false},
		{"9.999B", 9_999_000_000, true},
	}
	for _, test := range cases {
		got, ok := youtubeParseCountText(test.raw)
		if ok != test.ok || (ok && got != test.want) {
			t.Fatalf("%q => %d,%t want %d,%t", test.raw, got, ok, test.want, test.ok)
		}
	}
}

func FuzzYouTubeParseCountText(f *testing.F) {
	for _, seed := range []string{
		"42 videos", "42videos", "1,234views", "42\u00a0videos", "42  videos",
		"1.5K views", "1.5Kviews", "42views!", "1 foo 2", "1.5", "١٢٣", "9.999B",
		"1,2", "12,34", "123foobar", "1kfoo", "1,,234", "1\u202f000",
		"no views", "not no views really", "token=no views", "no views extra",
		strings.Repeat("9", 40) + "k", "1m\x00", "Views: 1,234",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, ok := youtubeParseCountText(raw)
		if !ok {
			return
		}
		if got < 0 {
			t.Fatalf("negative count %d for %q", got, raw)
		}
	})
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
