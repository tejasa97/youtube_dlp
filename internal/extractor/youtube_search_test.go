package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const searchInitial = `ytcfg.set({"INNERTUBE_API_KEY":"fixture-key","INNERTUBE_CLIENT_VERSION":"fixture-version","VISITOR_DATA":"fixture-visitor"});ytInitialData={"contents":{"twoColumnSearchResultsRenderer":{"primaryContents":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":[{"videoRenderer":{"videoId":"aaaaaaaaaaa","title":{"simpleText":"first"}}},{"reelItemRenderer":{"videoId":"bbbbbbbbbbb","videoType":"SHORT","title":{"simpleText":"short"}}}]}}]}}}},"continuationContents":{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"next-token"}}}}};`
const searchContinuation = `{"onResponseReceivedCommands":[{"appendContinuationItemsAction":{"continuationItems":[{"videoRenderer":{"videoId":"ccccccccccc","title":{"simpleText":"third"}}},{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"next-token"}}}}]}}]}`

type searchTransport struct {
	page, continuation []byte
	requests           int
	continuationURL    string
	headers            http.Header
	body               string
	status             int
}

func readYouTubeSearchFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "youtube_search", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (t *searchTransport) ReadPage(_ context.Context, raw string) ([]byte, http.Header, error) {
	if !strings.Contains(raw, "search_query=") {
		return nil, nil, errors.New("bad search URL")
	}
	return t.page, make(http.Header), nil
}
func (t *searchTransport) Do(_ context.Context, r *http.Request) (*http.Response, error) {
	t.requests++
	t.continuationURL = r.URL.String()
	t.headers = r.Header.Clone()
	data, _ := io.ReadAll(r.Body)
	t.body = string(data)
	if r.Method != http.MethodPost || r.URL.Path != "/youtubei/v1/search" {
		return nil, errors.New("bad continuation request")
	}
	status := t.status
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(t.continuation))}, nil
}

func TestYouTubeSearchInitialContinuationAndAllCap(t *testing.T) {
	transport := &searchTransport{page: readYouTubeSearchFixture(t, "initial.html"), continuation: readYouTubeSearchFixture(t, "continuation.json")}
	result, err := NewYouTubeSearch().Extract(context.Background(), Request{URL: "ytsearchall:fixture query", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].ID != "aaaaaaaaaaa" || entries[1].URL != "https://www.youtube.com/shorts/bbbbbbbbbbb" || entries[2].ID != "ccccccccccc" {
		t.Fatalf("entries = %#v", entries)
	}
	if transport.requests != 1 || !strings.Contains(transport.body, `"continuation":"next-token"`) || !strings.Contains(transport.body, `"visitorData":"fixture-visitor"`) || !strings.Contains(transport.continuationURL, "key=fixture-key") || transport.headers.Get("X-Youtube-Client-Version") != "fixture-version" {
		t.Fatalf("requests=%d url=%s headers=%v body=%s", transport.requests, transport.continuationURL, transport.headers, transport.body)
	}
}

func TestYouTubeSearchLazyReusableAndCancellation(t *testing.T) {
	transport := &searchTransport{page: readYouTubeSearchFixture(t, "initial.html"), continuation: readYouTubeSearchFixture(t, "continuation.json")}
	result, err := NewYouTubeSearch().Extract(context.Background(), Request{URL: "ytsearch:fixture", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	first, err := CollectEntries(context.Background(), result.Entries, 2)
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%v err=%v", first, err)
	}
	if transport.requests != 0 {
		t.Fatalf("continuation should be lazy: %d", transport.requests)
	}
	second, err := CollectEntries(context.Background(), result.Entries, 3)
	if err != nil || len(second) != 1 {
		t.Fatalf("second=%v err=%v", second, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok, err := result.Entries.Iterator().Next(ctx)
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v %v", ok, err)
	}
}

func TestYouTubeSearchTargetHostileAndRouting(t *testing.T) {
	good := []string{"ytsearch:cats", "ytsearch5:cats", "ytsearchall:cats", "https://www.youtube.com/results?search_query=cats", "http://youtube.com/search?q=cats"}
	for _, raw := range good {
		parsed, _ := url.Parse(raw)
		if !NewYouTubeSearch().Suitable(parsed) {
			t.Errorf("not suitable: %q", raw)
		}
	}
	bad := []string{"ytsearch0:cats", "ytsearch51:cats", "ytsearch:", "ftp://youtube.com/results?search_query=x", "https://evil.com/results?search_query=x", "https://www.youtube.com/results%2fno?search_query=x", "https://www.youtube.com/results?search_query=a%2fb", "https://u@youtube.com/results?search_query=x", "https://youtube.com:443/results?search_query=x"}
	for _, raw := range bad {
		parsed, _ := url.Parse(raw)
		if NewYouTubeSearch().Suitable(parsed) {
			t.Errorf("accepted hostile URL: %q", raw)
		}
	}
}

func TestYouTubeSearchTargetCounts(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int
	}{{"ytsearch:one", 1}, {"ytsearch3:three", 3}, {"ytsearchall:all", youtubeSearchMaxCount}} {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		_, count, _, ok := youtubeSearchTarget(parsed)
		if !ok || count != test.want {
			t.Errorf("target %q = count %d ok %v", test.raw, count, ok)
		}
	}
	entries := make([]Entry, youtubeSearchMaxCount+1)
	sequence, err := youtubeSearchEntries(entries, "", youtubeSearchMaxCount, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectEntries(context.Background(), sequence, youtubeSearchMaxCount+1)
	if err != nil || len(got) != youtubeSearchMaxCount {
		t.Fatalf("bounded all=%d err=%v", len(got), err)
	}
}

func TestYouTubeSearchFailures(t *testing.T) {
	for _, test := range []struct {
		page   []byte
		status int
		want   error
	}{{[]byte(`ytInitialData={broken};`), 0, ErrInvalidMetadata}, {[]byte(searchInitial), 401, ErrAuthentication}, {[]byte(searchInitial), 404, ErrUnavailable}, {[]byte(searchInitial), 429, ErrYouTubeSearchRateLimited}, {[]byte(searchInitial), 500, ErrYouTubeSearchNetwork}} {
		transport := &searchTransport{page: test.page, continuation: []byte(`{}`), status: test.status}
		result, err := NewYouTubeSearch().Extract(context.Background(), Request{URL: "ytsearch3:x", Transport: transport})
		if err == nil && test.status != 0 {
			_, err = CollectEntries(context.Background(), result.Entries, 10)
		}
		if !errors.Is(err, test.want) {
			t.Errorf("status=%d: err=%v want %v", test.status, err, test.want)
		}
	}
}

func FuzzParseYouTubeSearchData(f *testing.F) {
	f.Add([]byte(`{"contents":{}}`))
	f.Add([]byte(searchContinuation))
	f.Fuzz(func(t *testing.T, data []byte) {
		page, err := parseYouTubeSearchData(data)
		if err != nil {
			return
		}
		for _, entry := range page.entries {
			if !youtubeIDPattern.MatchString(entry.ID) || entry.ExtractorKey != "youtube" {
				t.Fatalf("unsafe entry: %#v", entry)
			}
			parsed, err := url.Parse(entry.URL)
			if err != nil || parsed.Scheme != "https" || parsed.Host != "www.youtube.com" || (parsed.Path != "/watch" && !strings.HasPrefix(parsed.Path, "/shorts/")) {
				t.Fatalf("unsafe entry URL: %#v", entry)
			}
		}
	})
}
func FuzzYouTubeSearchTarget(f *testing.F) {
	f.Add("ytsearch:cats")
	f.Add("https://www.youtube.com/results?search_query=x")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		query, count, canonical, ok := youtubeSearchTarget(parsed)
		if !ok {
			return
		}
		if !validYouTubeSearchQuery(query) || count < 1 || count > youtubeSearchMaxCount {
			t.Fatalf("unsafe target %q: %q %d", raw, query, count)
		}
		canonicalURL, err := url.Parse(canonical)
		if err != nil || canonicalURL.Scheme != "https" || canonicalURL.Host != "www.youtube.com" || canonicalURL.User != nil || canonicalURL.Port() != "" || (canonicalURL.Path != "/results" && canonicalURL.Path != "/search") {
			t.Fatalf("unsafe canonical route %q", canonical)
		}
	})
}
