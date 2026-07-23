package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

type youtubeConditionalRedirectTransport struct {
	page  []byte
	reads []string
}

func (transport *youtubeConditionalRedirectTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	transport.reads = append(transport.reads, rawURL)
	return append([]byte(nil), transport.page...), nil, nil
}

func (*youtubeConditionalRedirectTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected conditional redirect request")
}

func readYouTubeConditionalRedirectFixture(t *testing.T) []byte {
	t.Helper()
	page, err := os.ReadFile("../../conformance/extractors/youtube_channel_redirect/regional.html")
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestYouTubeConditionalChannelRedirectIntegratesEveryRouteFamily(t *testing.T) {
	const targetRoot = "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv"
	tests := []struct {
		name, rawURL, fetched, wantURL, wantKey string
		extract                                 func(context.Context, Request) (Extraction, error)
	}{
		{
			name: "channel videos", rawURL: "https://youtube.com/channel/UC1234567890abcdefghijkl/videos?view=0",
			fetched: "https://www.youtube.com/channel/UC1234567890abcdefghijkl/videos",
			wantURL: targetRoot + "/videos", wantKey: "youtube_channel_tab",
			extract: NewYouTubeChannelTab().Extract,
		},
		{
			name: "handle shorts", rawURL: "https://youtube.com/@global-handle/shorts",
			fetched: "https://www.youtube.com/@global-handle/shorts",
			wantURL: targetRoot + "/shorts", wantKey: "youtube_channel_tab",
			extract: NewYouTubeHandleTab().Extract,
		},
		{
			name: "alias streams", rawURL: "https://youtube.com/user/GlobalAlias/streams",
			fetched: "https://www.youtube.com/user/GlobalAlias/streams",
			wantURL: targetRoot + "/streams", wantKey: "youtube_channel_tab",
			extract: NewYouTubeAliasTab().Extract,
		},
		{
			name: "bare handle", rawURL: "https://youtube.com/@global-handle",
			fetched: "https://www.youtube.com/@global-handle/videos",
			wantURL: targetRoot, wantKey: "youtube_channel_tab",
			extract: NewYouTubeHandleTab().Extract,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &youtubeConditionalRedirectTransport{page: readYouTubeConditionalRedirectFixture(t)}
			result, err := test.extract(context.Background(), Request{URL: test.rawURL, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsURL() || result.IsPlaylist() {
				t.Fatalf("result=%#v", result)
			}
			if result.Redirect.URL != test.wantURL || result.Redirect.ExtractorKey != test.wantKey ||
				result.Redirect.Transparent {
				t.Fatalf("redirect=%#v", result.Redirect)
			}
			registry := NewRegistry(NewYouTubeChannelTab(), NewYouTubeHandleTab(), NewYouTubeAliasTab(), NewYouTube())
			selected, err := registry.SelectFor(result.Redirect.URL, result.Redirect.ExtractorKey)
			if err != nil || selected.Name() != test.wantKey {
				t.Fatalf("registry selected=%v err=%v", selected, err)
			}
			if len(transport.reads) != 1 || transport.reads[0] != test.fetched {
				t.Fatalf("reads=%#v", transport.reads)
			}
		})
	}
}

func TestYouTubeConditionalChannelRedirectCancellationBeforeRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &youtubeConditionalRedirectTransport{page: readYouTubeConditionalRedirectFixture(t)}
	_, err := NewYouTubeHandleTab().Extract(ctx, Request{
		URL: "https://www.youtube.com/@global-handle/videos", Transport: transport,
	})
	if !errors.Is(err, context.Canceled) || len(transport.reads) != 0 {
		t.Fatalf("err=%v reads=%#v", err, transport.reads)
	}
}

func TestYouTubeConditionalChannelRedirectDestinationFamiliesAndDuplicates(t *testing.T) {
	tests := []struct {
		name, target, tab, wantURL, wantKey string
	}{
		{"channel relative", "/channel/UCabcdefghijklmnopqrstuv", "community", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/community", "youtube_channel_tab"},
		{"channel absolute", "https://youtube.com/channel/UCabcdefghijklmnopqrstuv", "", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv", "youtube_channel_tab"},
		{"handle", "/@Regional.Handle", "videos", "https://www.youtube.com/@Regional.Handle/videos", "youtube_handle_tab"},
		{"user alias", "/user/RegionalAlias", "shorts", "https://www.youtube.com/user/RegionalAlias/shorts", "youtube_alias_tab"},
		{"custom alias", "/c/RegionalAlias", "streams", "https://www.youtube.com/c/RegionalAlias/streams", "youtube_alias_tab"},
		{"Unicode alias", "/c/日本語", "videos", "https://www.youtube.com/c/%E6%97%A5%E6%9C%AC%E8%AA%9E/videos", "youtube_alias_tab"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := []byte(`{"onResponseReceivedActions":[
				{"navigateAction":{"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"` + test.target + `"}}}}},
				{"navigateAction":{"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"` + test.target + `"}}}}}
			]}`)
			entry, ok, err := youtubeConditionalChannelRedirect(data, "https://www.youtube.com/source", test.tab)
			if err != nil || !ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if entry.URL != test.wantURL || entry.ExtractorKey != test.wantKey {
				t.Fatalf("entry=%#v", entry)
			}
		})
	}

	entry, ok, err := youtubeConditionalChannelRedirect([]byte(`{"onResponseReceivedActions":[{"signalAction":{}}]}`), "https://www.youtube.com/source", "")
	if err != nil || ok || entry != (Entry{}) {
		t.Fatalf("absent entry=%#v ok=%v err=%v", entry, ok, err)
	}

	bounded := `{"onResponseReceivedActions":[` +
		strings.Repeat(`{"signalAction":{}},`, youtubeMaxConditionalRedirectActions-1) +
		`{"navigateAction":{"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv"}}}}}]}`
	entry, ok, err = youtubeConditionalChannelRedirect([]byte(bounded), "https://www.youtube.com/source", "")
	if err != nil || !ok || entry.URL != "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv" {
		t.Fatalf("bounded entry=%#v ok=%v err=%v", entry, ok, err)
	}
}

func TestYouTubeConditionalChannelRedirectRejectsHostileAmbiguousAndSelfTargets(t *testing.T) {
	const valid = `{"onResponseReceivedActions":[{"navigateAction":{"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv"}}}}}]}`
	many := `{"onResponseReceivedActions":[` +
		strings.Repeat(`{"signalAction":{}},`, youtubeMaxConditionalRedirectActions) +
		`{"signalAction":{}}]}`
	tests := []struct {
		name, data, source, tab string
	}{
		{"malformed JSON", `{`, "https://www.youtube.com/source", ""},
		{"invalid UTF-8", string([]byte{0xff}), "https://www.youtube.com/source", ""},
		{"non-object root", `[]`, "https://www.youtube.com/source", ""},
		{"too many actions", many, "https://www.youtube.com/source", ""},
		{"conflicting redirects", `{"onResponseReceivedActions":[
			{"navigateAction":{"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv"}}}}},
			{"navigateAction":{"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/@Regional.Handle"}}}}}
		]}`, "https://www.youtube.com/source", ""},
		{"external host", redirectJSON("https://evil.example/channel/UCabcdefghijklmnopqrstuv"), "https://www.youtube.com/source", ""},
		{"lookalike host", redirectJSON("https://www.youtube.com.evil.example/channel/UCabcdefghijklmnopqrstuv"), "https://www.youtube.com/source", ""},
		{"userinfo", redirectJSON("https://user@www.youtube.com/channel/UCabcdefghijklmnopqrstuv"), "https://www.youtube.com/source", ""},
		{"port", redirectJSON("https://www.youtube.com:443/channel/UCabcdefghijklmnopqrstuv"), "https://www.youtube.com/source", ""},
		{"query", redirectJSON("/channel/UCabcdefghijklmnopqrstuv?feature=x"), "https://www.youtube.com/source", ""},
		{"fragment", redirectJSON("/channel/UCabcdefghijklmnopqrstuv#x"), "https://www.youtube.com/source", ""},
		{"encoded separator", redirectJSON("/channel%2fUCabcdefghijklmnopqrstuv"), "https://www.youtube.com/source", ""},
		{"already tabbed", redirectJSON("/channel/UCabcdefghijklmnopqrstuv/videos"), "https://www.youtube.com/source", ""},
		{"unsupported route", redirectJSON("/watch?v=dQw4w9WgXcQ"), "https://www.youtube.com/source", ""},
		{"control", redirectJSON("/channel/UCabcdefghijklmnopqrstuv\n"), "https://www.youtube.com/source", ""},
		{"overlong", redirectJSON("/channel/" + strings.Repeat("a", youtubeMaxConditionalRedirectURLBytes)), "https://www.youtube.com/source", ""},
		{"unsupported requested tab", valid, "https://www.youtube.com/source", "membership"},
		{"self", valid, "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, ok, err := youtubeConditionalChannelRedirect([]byte(test.data), test.source, test.tab); ok || !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
		})
	}
}

func redirectJSON(target string) string {
	return `{"onResponseReceivedActions":[{"navigateAction":{"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":` +
		strconv.Quote(target) + `}}}}}]}`
}

func FuzzYouTubeConditionalChannelRedirect(f *testing.F) {
	f.Add([]byte(`{"onResponseReceivedActions":[{"navigateAction":{"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv"}}}}}]}`), "videos")
	f.Add([]byte(`{"onResponseReceivedActions":[]}`), "")
	f.Fuzz(func(t *testing.T, data []byte, requestedTab string) {
		entry, ok, err := youtubeConditionalChannelRedirect(data, "https://www.youtube.com/source", requestedTab)
		if err != nil || !ok {
			return
		}
		parsed, parseErr := url.Parse(entry.URL)
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host != "www.youtube.com" ||
			parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
			t.Fatalf("unsafe redirect=%#v parsed=%#v err=%v", entry, parsed, parseErr)
		}
		switch entry.ExtractorKey {
		case "youtube_channel_tab":
			_, tab, routeOK := youtubeChannelTabTarget(parsed)
			if !routeOK || tab != requestedTab {
				t.Fatalf("channel redirect=%#v tab=%q", entry, tab)
			}
		case "youtube_handle_tab":
			_, tab, routeOK := youtubeHandleTabTarget(parsed)
			if !routeOK || tab != requestedTab {
				t.Fatalf("handle redirect=%#v tab=%q", entry, tab)
			}
		case "youtube_alias_tab":
			_, _, tab, routeOK := youtubeAliasTabTarget(parsed)
			if !routeOK || tab != requestedTab {
				t.Fatalf("alias redirect=%#v tab=%q", entry, tab)
			}
		default:
			t.Fatalf("extractor key=%q", entry.ExtractorKey)
		}
	})
}
