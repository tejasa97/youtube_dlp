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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

const soundCloudSearchClientID = "0123456789abcdef0123456789abcdef"

func readSoundCloudSearchFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "soundcloud_search", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type soundCloudSearchTransport struct {
	t                         *testing.T
	home, client, first, next []byte
	status                    int
	failInitial, failNext     error
	blockNext                 chan struct{}
	emptyPages                int
	mu                        sync.Mutex
	requests                  []string
	isolateCalls              int
}

// soundCloudSearchNoIsolationTransport models a cookie-bearing operation
// transport that has no isolated-request capability. It must fail closed once
// lazy API paging begins, after permitted first-party bootstrap requests.
type soundCloudSearchNoIsolationTransport struct{ base *soundCloudSearchTransport }

func (tr soundCloudSearchNoIsolationTransport) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return tr.base.Do(ctx, req)
}
func (tr soundCloudSearchNoIsolationTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	return tr.base.ReadPage(ctx, rawURL)
}

type soundCloudSearchRoundTripper func(*http.Request) (*http.Response, error)

func (fn soundCloudSearchRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func newSoundCloudSearchTransport(t *testing.T) *soundCloudSearchTransport {
	return &soundCloudSearchTransport{t: t, home: readSoundCloudSearchFixture(t, "home.html"), client: readSoundCloudSearchFixture(t, "client.js"), first: readSoundCloudSearchFixture(t, "page1.json"), next: readSoundCloudSearchFixture(t, "page2.json")}
}

func (tr *soundCloudSearchTransport) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return tr.do(ctx, req, false)
}

func (tr *soundCloudSearchTransport) DoWithoutCookies(ctx context.Context, req *http.Request) (*http.Response, error) {
	return tr.do(ctx, req, true)
}

func (tr *soundCloudSearchTransport) do(ctx context.Context, req *http.Request, isolated bool) (*http.Response, error) {
	tr.mu.Lock()
	tr.requests = append(tr.requests, req.Method+" "+req.URL.String())
	if isolated {
		tr.isolateCalls++
	}
	tr.mu.Unlock()
	if req.Method != http.MethodGet {
		tr.t.Errorf("method=%s", req.Method)
		return nil, errors.New("unexpected method")
	}
	switch req.URL.Host {
	case "soundcloud.com":
		if req.URL.Path != "/" || req.URL.RawQuery != "" {
			tr.t.Errorf("bad bootstrap %s", req.URL)
			return nil, errors.New("bad bootstrap")
		}
		return response(200, tr.home), nil
	case "a-v2.sndcdn.com":
		if req.URL.Path != "/assets/search-fixture.js" {
			tr.t.Errorf("bad client asset %s", req.URL)
			return nil, errors.New("bad client")
		}
		return response(200, tr.client), nil
	case "api-v2.soundcloud.com":
		if !isolated || req.URL.Path != "/search/tracks" || req.Header.Get("Cookie") != "" {
			tr.t.Errorf("bad API request %s headers=%v", req.URL, req.Header)
			return nil, errors.New("bad API")
		}
		q := req.URL.Query()
		if !soundCloudSearchRequestQuery(q) {
			tr.t.Errorf("missing API contract %s", req.URL)
			return nil, errors.New("bad query")
		}
		if tr.emptyPages > 0 {
			tr.emptyPages--
			offset, _ := strconv.Atoi(q.Get("offset"))
			next := soundCloudSearchEndpoint + "?" + url.Values{"q": {q.Get("q")}, "limit": {q.Get("limit")}, "linked_partitioning": {"1"}, "offset": {strconv.Itoa(offset + 1)}, "cursor": {fmt.Sprintf("empty-%d", offset+1)}}.Encode()
			return response(200, []byte(`{"collection":[],"next_href":`+strconv.Quote(next)+`}`)), nil
		}
		if q.Get("cursor") == "next-fixture" {
			if tr.blockNext != nil {
				select {
				case <-tr.blockNext:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if tr.failNext != nil {
				return nil, tr.failNext
			}
			return response(tr.responseStatus(), tr.next), nil
		}
		if q.Get("limit") == "" {
			return response(tr.responseStatus(), tr.next), nil
		}
		if q.Get("q") != "" && q.Get("offset") != "0" {
			return response(tr.responseStatus(), tr.next), nil
		}
		if q.Get("q") == "" || q.Get("limit") == "" || q.Get("offset") != "0" {
			tr.t.Errorf("bad initial query %s", req.URL)
			return nil, errors.New("bad initial")
		}
		if tr.failInitial != nil {
			return nil, tr.failInitial
		}
		return response(tr.responseStatus(), tr.first), nil
	default:
		tr.t.Errorf("unexpected host %s", req.URL.Host)
		return nil, errors.New("unexpected host")
	}
}

func soundCloudSearchRequestQuery(q url.Values) bool {
	allowed := map[string]bool{"q": true, "limit": true, "linked_partitioning": true, "offset": true, "cursor": true, "client_id": true}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	if q.Get("client_id") != soundCloudSearchClientID || q.Get("q") == "" || q.Get("linked_partitioning") != "1" || q.Get("offset") == "" || q.Get("limit") == "" {
		return false
	}
	limit, limitErr := strconv.Atoi(q.Get("limit"))
	_, offsetErr := strconv.ParseUint(q.Get("offset"), 10, 64)
	return limitErr == nil && limit >= 1 && limit <= soundCloudSearchMaxCount && offsetErr == nil
}
func (*soundCloudSearchTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("not used")
}
func (tr *soundCloudSearchTransport) responseStatus() int {
	if tr.status != 0 {
		return tr.status
	}
	return 200
}
func response(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
}
func (tr *soundCloudSearchTransport) count(path string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, r := range tr.requests {
		if strings.Contains(r, path) {
			n++
		}
	}
	return n
}

func TestSoundCloudSearchRoutingAndCounts(t *testing.T) {
	good := []string{"scsearch:cats", "scsearch3:cats", "scsearchall:cats", "https://soundcloud.com/search?q=cats"}
	for _, raw := range good {
		u, _ := url.Parse(raw)
		if !NewSoundCloudSearch().Suitable(u) {
			t.Errorf("not suitable %q", raw)
		}
	}
	bad := []string{"scsearch0:x", "scsearch201:x", "scsearch:", "scsearch:two\nlines", "http://soundcloud.com/search?q=x", "https://soundcloud.com/search?q=x&x=1", "https://soundcloud.com/search?query=x", "https://evil.example/search?q=x", "https://soundcloud.com:443/search?q=x", "https://u@soundcloud.com/search?q=x"}
	for _, raw := range bad {
		u, _ := url.Parse(raw)
		if NewSoundCloudSearch().Suitable(u) {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, tc := range []struct {
		raw  string
		want int
	}{{"scsearch:x", 1}, {"scsearch7:x", 7}, {"scsearchall:x", soundCloudSearchMaxCount}} {
		u, _ := url.Parse(tc.raw)
		_, n, _, ok := soundCloudSearchTarget(u)
		if !ok || n != tc.want {
			t.Errorf("%s: %d %v", tc.raw, n, ok)
		}
	}
}

func TestSoundCloudSearchPagesFilteringReusableAndContract(t *testing.T) {
	tr := newSoundCloudSearchTransport(t)
	result, err := NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch3:fixture query", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	if tr.count("/search/tracks") != 0 {
		t.Fatal("search was not lazy")
	}
	got, err := CollectEntries(context.Background(), result.Entries, 3)
	if err != nil || len(got) != 3 || got[0].ID != "101" || got[1].ID != "102" || got[2].ID != "104" {
		t.Fatalf("entries=%#v err=%v", got, err)
	}
	if tr.count("/search/tracks") != 2 || tr.count("https://soundcloud.com/") != 1 || tr.count("/assets/search-fixture.js") != 1 || tr.isolateCalls != 2 {
		t.Fatalf("requests=%v", tr.requests)
	}
	again, err := CollectEntries(context.Background(), result.Entries, 3)
	if err != nil || len(again) != 3 || again[2].ID != "104" || tr.count("/search/tracks") != 4 {
		t.Fatalf("again=%#v err=%v requests=%v", again, err, tr.requests)
	}
}

func TestSoundCloudSearchPageCapAndCookieIsolation(t *testing.T) {
	tr := newSoundCloudSearchTransport(t)
	tr.emptyPages = soundCloudSearchMaxPages
	result, err := NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearchall:empty", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, soundCloudSearchMaxCount)
	if len(entries) != 0 || !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if got := tr.count("/search/tracks"); got != soundCloudSearchMaxPages || tr.isolateCalls != soundCloudSearchMaxPages {
		t.Fatalf("API calls=%d isolated=%d", got, tr.isolateCalls)
	}
}

func TestSoundCloudSearchRequiresCookieIsolation(t *testing.T) {
	base := newSoundCloudSearchTransport(t)
	result, err := NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch:fixture", Transport: soundCloudSearchNoIsolationTransport{base: base}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, 1)
	if !errors.Is(err, ErrTransportIsolation) || base.isolateCalls != 0 || base.count("/search/tracks") != 0 {
		t.Fatalf("err=%v isolated=%d requests=%v", err, base.isolateCalls, base.requests)
	}
}

func TestSoundCloudSearchUsesNetworkClientCookieIsolation(t *testing.T) {
	var apiCookie string
	client, err := network.New(network.Config{
		DefaultHeaders: http.Header{"Cookie": {"default=secret"}},
		RoundTripper: soundCloudSearchRoundTripper(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Host {
			case "soundcloud.com":
				return response(200, readSoundCloudSearchFixture(t, "home.html")), nil
			case "a-v2.sndcdn.com":
				return response(200, readSoundCloudSearchFixture(t, "client.js")), nil
			case "api-v2.soundcloud.com":
				apiCookie = request.Header.Get("Cookie")
				if !soundCloudSearchRequestQuery(request.URL.Query()) {
					return nil, errors.New("invalid isolated API query")
				}
				return response(200, readSoundCloudSearchFixture(t, "page1.json")), nil
			default:
				return nil, fmt.Errorf("unexpected host %q", request.URL.Host)
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch3:fixture query", Transport: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, nextErr := result.Entries.Iterator().Next(context.Background()); !ok || nextErr != nil || apiCookie != "" {
		t.Fatalf("entry ok=%v err=%v API cookie=%q", ok, nextErr, apiCookie)
	}
}

func TestSoundCloudSearchErrorsCancellationAndContinuations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  []byte
		status int
		fail   error
		want   error
	}{
		{"malformed-json", []byte("{"), 0, nil, ErrInvalidMetadata}, {"malformed-shape", readSoundCloudSearchFixture(t, "malformed.json"), 0, nil, ErrInvalidMetadata}, {"alert", readSoundCloudSearchFixture(t, "alert.json"), 0, nil, ErrInvalidMetadata}, {"auth", nil, 401, nil, ErrAuthentication}, {"unavailable", nil, 404, nil, ErrUnavailable}, {"network", nil, 0, errors.New("offline token=top-secret"), ErrSoundCloudSearchNetwork},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newSoundCloudSearchTransport(t)
			if tc.first != nil {
				tr.first = tc.first
			}
			tr.status = tc.status
			tr.failInitial = tc.fail
			r, err := NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch2:x", Transport: tr})
			if err == nil {
				_, err = CollectEntries(context.Background(), r.Entries, 2)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want=%v", err, tc.want)
			}
			if strings.Contains(fmt.Sprint(err), "top-secret") {
				t.Fatalf("network diagnostic leaked secret: %v", err)
			}
		})
	}
	tr := newSoundCloudSearchTransport(t)
	r, err := NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch3:x", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	tr.first = []byte(`{"collection":[],"next_href":"https://evil.example/search/tracks"}`)
	_, err = CollectEntries(context.Background(), r.Entries, 3)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("cross-origin=%v", err)
	}
	tr = newSoundCloudSearchTransport(t)
	tr.first = []byte(`{"collection":[],"next_href":"https://api-v2.soundcloud.com/search/tracks?q=x&limit=2&linked_partitioning=1&offset=1&cursor=secret&extra=secret"}`)
	r, err = NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch2:x", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), r.Entries, 2)
	if !errors.Is(err, ErrInvalidPlaylist) || strings.Contains(fmt.Sprint(err), "secret") {
		t.Fatalf("extra continuation=%v", err)
	}
	tr = newSoundCloudSearchTransport(t)
	tr.first = []byte(`{"collection":[],"next_href":"http://api-v2.soundcloud.com/search/tracks?offset=1"}`)
	r, err = NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch2:x", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), r.Entries, 2)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("downgrade=%v", err)
	}
	tr = newSoundCloudSearchTransport(t)
	tr.failNext = errors.New("continuation offline")
	r, err = NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch3:fixture query", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), r.Entries, 3)
	if !errors.Is(err, ErrSoundCloudSearchNetwork) {
		t.Fatalf("continuation network=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok, err := r.Entries.Iterator().Next(ctx)
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel %v %v", ok, err)
	}
	tr = newSoundCloudSearchTransport(t)
	tr.blockNext = make(chan struct{})
	r, err = NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearch3:fixture query", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	it := r.Entries.Iterator()
	if _, _, err = it.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = it.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, _, e := it.Next(ctx); done <- e }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("during cancel=%v", err)
	}
}

func TestSoundCloudSearchCursorQueryPolicy(t *testing.T) {
	policy := soundCloudContinuationPolicy{allowedPath: "/search/tracks"}
	for _, raw := range []string{
		"https://api-v2.soundcloud.com/search/tracks?q=x&limit=2&linked_partitioning=1&offset=1&extra=secret",
		"https://api-v2.soundcloud.com/search/tracks?q=x&q=x&limit=2&linked_partitioning=1&offset=1",
		"https://api-v2.soundcloud.com/search/tracks?q=x&limit=2&linked_partitioning=1&offset=1&cursor=",
		"https://api-v2.soundcloud.com/search/tracks?q=x&limit=2&linked_partitioning=0&offset=1",
	} {
		if _, err := soundCloudSearchCursor(raw, policy, "x", 2); !errors.Is(err, ErrInvalidPlaylist) || strings.Contains(fmt.Sprint(err), "secret") {
			t.Fatalf("cursor %q err=%v", raw, err)
		}
	}
	validated, err := soundCloudSearchCursor("https://api-v2.soundcloud.com/search/tracks?q=x&limit=2&linked_partitioning=1&offset=1&cursor=next&client_id=stale", policy, "x", 2)
	if err != nil || strings.Contains(validated, "client_id") {
		t.Fatalf("stale client id validated=%q err=%v", validated, err)
	}
}

func TestSoundCloudSearchContinuationLoopAndBound(t *testing.T) {
	tr := newSoundCloudSearchTransport(t)
	tr.first = []byte(`{"collection":[{"kind":"track","id":1,"title":"x","permalink_url":"https://soundcloud.com/a/x"}],"next_href":"https://api-v2.soundcloud.com/search/tracks?linked_partitioning=1&offset=1&q=x&limit=200"}`)
	tr.next = tr.first
	r, err := NewSoundCloudSearch().Extract(context.Background(), Request{URL: "scsearchall:x", Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectEntries(context.Background(), r.Entries, 201)
	if err != nil || len(got) != 2 {
		t.Fatalf("loop got=%d err=%v", len(got), err)
	}
	entries := make([]Entry, soundCloudSearchMaxCount+1)
	seq, err := soundCloudSearchEntriesForTest(entries)
	if err != nil {
		t.Fatal(err)
	}
	got, err = CollectEntries(context.Background(), seq, soundCloudSearchMaxCount+1)
	if err != nil || len(got) != soundCloudSearchMaxCount {
		t.Fatalf("bound=%d %v", len(got), err)
	}
}
func soundCloudSearchEntriesForTest(entries []Entry) (EntrySequence, error) {
	return limitedEntries{source: StaticEntries(entries...), limit: soundCloudSearchMaxCount}, nil
}

func FuzzSoundCloudSearchTarget(f *testing.F) {
	f.Add("scsearch:cats")
	f.Add("https://soundcloud.com/search?q=x")
	f.Fuzz(func(t *testing.T, raw string) {
		u, e := url.Parse(raw)
		if e != nil {
			return
		}
		q, n, c, ok := soundCloudSearchTarget(u)
		if !ok {
			return
		}
		if !validSoundCloudSearchQuery(q) || n < 1 || n > soundCloudSearchMaxCount {
			t.Fatalf("unsafe target")
		}
		cu, e := url.Parse(c)
		if e != nil || cu.Scheme != "https" || cu.Host != "soundcloud.com" || cu.Path != "/search" || cu.User != nil || cu.Port() != "" {
			t.Fatalf("unsafe canonical %q", c)
		}
	})
}
func FuzzParseSoundCloudSearchPage(f *testing.F) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "soundcloud_search", "page1.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fixture)
	f.Add([]byte(`{"collection":[]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var p soundCloudSearchPage
		if json.Unmarshal(data, &p) != nil || p.Collection == nil {
			return
		}
		for _, track := range p.Collection {
			entry, ok := soundCloudSearchTrackEntry(track)
			if !ok {
				continue
			}
			if track.Kind != "track" {
				t.Fatalf("non-track entry %#v", track)
			}
			u, e := url.Parse(entry.URL)
			if e != nil || entry.ID == "" || entry.Title == "" || entry.ExtractorKey != "soundcloud" || u.Scheme != "https" || u.Host != "soundcloud.com" {
				t.Fatalf("unsafe entry %#v", entry)
			}
		}
	})
}
