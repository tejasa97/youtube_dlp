package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	mu                        sync.Mutex
	requests                  []string
}

func newSoundCloudSearchTransport(t *testing.T) *soundCloudSearchTransport {
	return &soundCloudSearchTransport{t: t, home: readSoundCloudSearchFixture(t, "home.html"), client: readSoundCloudSearchFixture(t, "client.js"), first: readSoundCloudSearchFixture(t, "page1.json"), next: readSoundCloudSearchFixture(t, "page2.json")}
}

func (tr *soundCloudSearchTransport) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	tr.requests = append(tr.requests, req.Method+" "+req.URL.String())
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
		if req.URL.Path != "/search/tracks" || req.Header.Get("Cookie") != "" {
			tr.t.Errorf("bad API request %s headers=%v", req.URL, req.Header)
			return nil, errors.New("bad API")
		}
		q := req.URL.Query()
		if q.Get("client_id") != soundCloudSearchClientID || q.Get("linked_partitioning") != "1" || q.Get("offset") == "" {
			tr.t.Errorf("missing API contract %s", req.URL)
			return nil, errors.New("bad query")
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
	if tr.count("/search/tracks") != 2 || tr.count("https://soundcloud.com/") != 1 || tr.count("/assets/search-fixture.js") != 1 {
		t.Fatalf("requests=%v", tr.requests)
	}
	again, err := CollectEntries(context.Background(), result.Entries, 3)
	if err != nil || len(again) != 3 || again[2].ID != "104" || tr.count("/search/tracks") != 4 {
		t.Fatalf("again=%#v err=%v requests=%v", again, err, tr.requests)
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
		{"malformed-json", []byte("{"), 0, nil, ErrInvalidMetadata}, {"malformed-shape", readSoundCloudSearchFixture(t, "malformed.json"), 0, nil, ErrInvalidMetadata}, {"alert", readSoundCloudSearchFixture(t, "alert.json"), 0, nil, ErrInvalidMetadata}, {"auth", nil, 401, nil, ErrAuthentication}, {"unavailable", nil, 404, nil, ErrUnavailable}, {"network", nil, 0, errors.New("offline"), ErrSoundCloudSearchNetwork},
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

func TestSoundCloudSearchContinuationLoopAndBound(t *testing.T) {
	tr := newSoundCloudSearchTransport(t)
	tr.first = []byte(`{"collection":[{"id":1,"title":"x","permalink_url":"https://soundcloud.com/a/x"}],"next_href":"https://api-v2.soundcloud.com/search/tracks?linked_partitioning=1&offset=1&q=x&limit=200"}`)
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
			u, e := url.Parse(entry.URL)
			if e != nil || entry.ID == "" || entry.Title == "" || entry.ExtractorKey != "soundcloud" || u.Scheme != "https" || u.Host != "soundcloud.com" {
				t.Fatalf("unsafe entry %#v", entry)
			}
		}
	})
}
