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

type musicTransport struct {
	page, continuation []byte
	readErr            error
	status, calls      int
	body, raw, method  string
	path, query        string
	headers            http.Header
}

func readYouTubeMusicFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "youtube_music_search", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func (m *musicTransport) ReadPage(_ context.Context, raw string) ([]byte, http.Header, error) {
	m.raw = raw
	if m.readErr != nil {
		return nil, nil, m.readErr
	}
	return m.page, make(http.Header), nil
}
func (m *musicTransport) Do(_ context.Context, r *http.Request) (*http.Response, error) {
	m.calls++
	m.method, m.path, m.query = r.Method, r.URL.Path, r.URL.RawQuery
	m.headers = r.Header.Clone()
	b, _ := io.ReadAll(r.Body)
	m.body = string(b)
	s := m.status
	if s == 0 {
		s = http.StatusOK
	}
	return &http.Response{StatusCode: s, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(m.continuation))}, nil
}
func musicFixtures(t *testing.T) *musicTransport {
	return &musicTransport{page: readYouTubeMusicFixture(t, "initial.html"), continuation: readYouTubeMusicFixture(t, "continuation.json")}
}

func TestYouTubeMusicSearchSectionsPagingAndRequest(t *testing.T) {
	m := musicFixtures(t)
	out, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?q=hello#songs", Transport: m})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectEntries(context.Background(), out.Entries, 4)
	if err != nil || len(got) != 3 || got[0].ID != "aaaaaaaaaaa" || got[1].ID != "ccccccccccc" || got[2].ID != "bbbbbbbbbbb" {
		t.Fatalf("entries=%#v err=%v", got, err)
	}
	if !strings.Contains(m.raw, "sp=EgWKAQII") || m.calls != 1 || m.method != http.MethodPost || m.path != "/youtubei/v1/search" || !strings.Contains(m.query, "key=fixture-key") || !strings.Contains(m.body, `"clientName":"WEB_REMIX"`) || !strings.Contains(m.body, `"clientVersion":"fixture-version"`) || !strings.Contains(m.body, `"visitorData":"fixture-visitor"`) || m.headers.Get("Origin") != "https://music.youtube.com" || m.headers.Get("X-Youtube-Client-Name") != "67" || m.headers.Get("X-Youtube-Client-Version") != "fixture-version" {
		t.Fatalf("raw=%s method=%s path=%s query=%s calls=%d body=%s headers=%v", m.raw, m.method, m.path, m.query, m.calls, m.body, m.headers)
	}
}
func TestYouTubeMusicSearchLazyReusableAndCancellation(t *testing.T) {
	m := musicFixtures(t)
	out, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?search_query=x#videos", Transport: m})
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := out.Entries.Iterator().Next(context.Background())
	if err != nil || !ok || first.ID != "aaaaaaaaaaa" || m.calls != 0 {
		t.Fatal(first, ok, err, m.calls)
	}
	for i := 0; i < 2; i++ {
		got, err := CollectEntries(context.Background(), out.Entries, 4)
		if err != nil || len(got) != 3 || got[0].ID != "aaaaaaaaaaa" || got[2].ID != "bbbbbbbbbbb" {
			t.Fatalf("iteration=%d entries=%v err=%v", i, got, err)
		}
	}
	if m.calls != 2 {
		t.Fatalf("independent iterators made %d continuation requests", m.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok, err = out.Entries.Iterator().Next(ctx)
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatal(ok, err)
	}
}
func TestYouTubeMusicSearchTargetPolicy(t *testing.T) {
	good := []string{"https://music.youtube.com/search?q=x", "http://music.youtube.com/search?search_query=x#videos"}
	bad := []string{"https://www.music.youtube.com/search?q=x", "https://music.youtube.com:443/search?q=x", "https://u@music.youtube.com/search?q=x", "https://music.youtube.com/search?q=x#albums", "https://music.youtube.com/search%2fno?q=x", "https://music.youtube.com/search?q=a%2fb", "https://music.youtube.com/search?q=x&sp=unsupported"}
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
func TestYouTubeMusicSearchErrorsAndAlerts(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{401, ErrAuthentication}, {404, ErrUnavailable}, {429, ErrYouTubeMusicSearchRateLimited}, {500, ErrYouTubeMusicSearchNetwork}} {
		m := musicFixtures(t)
		m.status = test.status
		out, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?q=x", Transport: m})
		if err == nil {
			_, err = CollectEntries(context.Background(), out.Entries, 4)
		}
		if !errors.Is(err, test.want) {
			t.Errorf("status=%d err=%v want=%v", test.status, err, test.want)
		}
	}
	for _, test := range []struct {
		page string
		want error
	}{{`ytInitialData={"alerts":[{"alertRenderer":{"text":{"simpleText":"Sign in to confirm"}}}]};`, ErrAuthentication}, {`ytInitialData={"alerts":[{"alertRenderer":{"text":{"simpleText":"Private content"}}}]};`, ErrUnavailable}, {`ytInitialData={broken};`, ErrInvalidMetadata}} {
		m := &musicTransport{page: []byte(test.page)}
		_, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?q=x", Transport: m})
		if !errors.Is(err, test.want) {
			t.Errorf("page err=%v want=%v", err, test.want)
		}
	}
	m := &musicTransport{readErr: errors.New("offline")}
	_, err := NewYouTubeMusicSearch().Extract(context.Background(), Request{URL: "https://music.youtube.com/search?q=x", Transport: m})
	if !errors.Is(err, ErrYouTubeMusicSearchNetwork) {
		t.Fatal(err)
	}
}
func FuzzParseYouTubeMusicSearchData(f *testing.F) {
	f.Add([]byte(`{"contents":{}}`))
	f.Add([]byte(`{"continuationContents":{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"next"}}}}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, e := parseYouTubeMusicSearchData(b)
		if e != nil {
			return
		}
		if p.continuation != "" && validYouTubeContinuationToken(p.continuation) == "" {
			t.Fatalf("unsafe continuation %q", p.continuation)
		}
		for _, x := range p.entries {
			if !youtubeIDPattern.MatchString(x.ID) || x.ExtractorKey != "youtube" || x.URL != "https://www.youtube.com/watch?v="+x.ID {
				t.Fatalf("unsafe entry %#v", x)
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
