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

const musicInitial = `ytcfg.set({"INNERTUBE_API_KEY":"key","INNERTUBE_CLIENT_VERSION":"version","VISITOR_DATA":"visitor"});ytInitialData={"contents":{"tabbedSearchResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[{"musicShelfRenderer":{"contents":[{"musicResponsiveListItemRenderer":{"navigationEndpoint":{"watchEndpoint":{"videoId":"aaaaaaaaaaa"}},"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"song"}]}}}]}},{"musicResponsiveListItemRenderer":{"navigationEndpoint":{"browseEndpoint":{"browseId":"album"}},"flexColumns":[]}}]}}]}}}}]}},"continuationContents":{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"next"}}}}};`
const musicContinuation = `{"onResponseReceivedCommands":[{"appendContinuationItemsAction":{"continuationItems":[{"videoRenderer":{"videoId":"bbbbbbbbbbb","title":{"simpleText":"video"}}},{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"next"}}}}]}}]}`

type musicTransport struct {
	page, continuation []byte
	status, calls      int
	body               string
	headers            http.Header
	raw                string
}

func (m *musicTransport) ReadPage(_ context.Context, raw string) ([]byte, http.Header, error) {
	m.raw = raw
	return m.page, make(http.Header), nil
}
func (m *musicTransport) Do(_ context.Context, r *http.Request) (*http.Response, error) {
	m.calls++
	m.headers = r.Header.Clone()
	b, _ := io.ReadAll(r.Body)
	m.body = string(b)
	s := m.status
	if s == 0 {
		s = 200
	}
	return &http.Response{StatusCode: s, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(m.continuation))}, nil
}
func TestYouTubeMusicSearchSectionsAndPaging(t *testing.T) {
	m := &musicTransport{page: []byte(musicInitial), continuation: []byte(musicContinuation)}
	out, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?q=hello#songs", Transport: m})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectEntries(context.Background(), out.Entries, 3)
	if err != nil || len(got) != 2 || got[0].ID != "aaaaaaaaaaa" || got[1].ID != "bbbbbbbbbbb" {
		t.Fatalf("%v %v", got, err)
	}
	if !strings.Contains(m.raw, "sp=EgWKAQII") || m.calls != 1 || !strings.Contains(m.body, "WEB_REMIX") || m.headers.Get("X-Youtube-Client-Name") != "67" {
		t.Fatalf("raw=%s calls=%d body=%s headers=%v", m.raw, m.calls, m.body, m.headers)
	}
}
func TestYouTubeMusicSearchLazyReusableAndFailures(t *testing.T) {
	m := &musicTransport{page: []byte(musicInitial), continuation: []byte(musicContinuation)}
	out, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?search_query=x#videos", Transport: m})
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := out.Entries.Iterator().Next(context.Background())
	if err != nil || !ok || first.ID != "aaaaaaaaaaa" || m.calls != 0 {
		t.Fatal(first, ok, err, m.calls)
	}
	got, _ := CollectEntries(context.Background(), out.Entries, 2)
	if len(got) != 2 || m.calls != 1 {
		t.Fatal(got, m.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok, err = out.Entries.Iterator().Next(ctx)
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatal(ok, err)
	}
	m.status = 429
	out, _ = NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?q=x", Transport: m})
	_, err = CollectEntries(context.Background(), out.Entries, 3)
	if !errors.Is(err, ErrYouTubeMusicSearchRateLimited) {
		t.Fatal(err)
	}
}
func TestYouTubeMusicSearchTargetPolicy(t *testing.T) {
	good := []string{"https://music.youtube.com/search?q=x", "http://music.youtube.com/search?search_query=x#videos"}
	bad := []string{"https://www.music.youtube.com/search?q=x", "https://music.youtube.com:443/search?q=x", "https://u@music.youtube.com/search?q=x", "https://music.youtube.com/search?q=x#albums", "https://music.youtube.com/search%2fno?q=x", "https://music.youtube.com/search?q=a%2fb"}
	for _, raw := range good {
		u, _ := url.Parse(raw)
		if !NewYouTubeMusicSearch().Suitable(u) {
			t.Error(raw)
		}
	}
	for _, raw := range bad {
		u, _ := url.Parse(raw)
		if NewYouTubeMusicSearch().Suitable(u) {
			t.Error(raw)
		}
	}
}
func TestYouTubeMusicSearchErrors(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{401, ErrAuthentication}, {404, ErrUnavailable}, {429, ErrYouTubeMusicSearchRateLimited}, {500, ErrYouTubeMusicSearchNetwork}} {
		m := &musicTransport{page: []byte(musicInitial), continuation: []byte(musicContinuation), status: test.status}
		out, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?q=x", Transport: m})
		if err == nil {
			_, err = CollectEntries(context.Background(), out.Entries, 3)
		}
		if !errors.Is(err, test.want) {
			t.Errorf("status=%d err=%v want=%v", test.status, err, test.want)
		}
	}
	m := &musicTransport{page: []byte(`ytInitialData={broken};`)}
	_, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?q=x", Transport: m})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatal(err)
	}
}
func FuzzParseYouTubeMusicSearchData(f *testing.F) {
	f.Add([]byte(musicContinuation))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, e := parseYouTubeMusicSearchData(b)
		if e != nil {
			return
		}
		for _, x := range p.entries {
			if !youtubeIDPattern.MatchString(x.ID) || x.URL != "https://www.youtube.com/watch?v="+x.ID {
				t.Fatalf("%#v", x)
			}
		}
	})
}
func FuzzYouTubeMusicSearchTarget(f *testing.F) {
	f.Add("https://music.youtube.com/search?q=x#songs")
	f.Fuzz(func(t *testing.T, raw string) {
		u, e := url.Parse(raw)
		if e != nil {
			return
		}
		q, n, c, _, ok := youtubeMusicSearchTarget(u)
		if !ok {
			return
		}
		cu, e := url.Parse(c)
		if e != nil || q == "" || n < 1 || n > youtubeMusicSearchMaxCount || cu.Scheme != "https" || cu.Host != "music.youtube.com" || cu.Path != "/search" {
			t.Fatalf("unsafe %q", raw)
		}
	})
}
