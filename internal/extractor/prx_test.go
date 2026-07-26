package extractor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type prxTransport struct {
	body     string
	status   int
	requests []string
}

func (t *prxTransport) Do(_ context.Context, r *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, r.URL.String())
	return &http.Response{StatusCode: t.status, Body: io.NopCloser(strings.NewReader(t.body)), Header: make(http.Header)}, nil
}
func (t *prxTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, r *http.Request) (*http.Response, error) {
	return t.Do(ctx, r)
}
func (t *prxTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unused")
}

func TestPRXRoutesAndStory(t *testing.T) {
	for _, raw := range []string{"https://prx.org/stories/1", "https://beta.prx.org/series/2", "https://listen.prx.org/accounts/3"} {
		u, _ := url.Parse(raw)
		if _, _, ok := prxTarget(u); !ok {
			t.Fatalf("reject %s", raw)
		}
	}
	for _, raw := range []string{"http://prx.org/stories/1", "https://evilprx.org/stories/1", "https://prx.org/stories/1%2f2", "https://prx.org/stories/1#x", "https://prx.org:443/stories/1"} {
		u, _ := url.Parse(raw)
		if _, _, ok := prxTarget(u); ok {
			t.Fatalf("accept hostile %s", raw)
		}
	}
	tx := &prxTransport{status: 200, body: `{"id":"1","title":"Story","description":"<p>text</p>","duration":60,"_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","label":"main","size":2,"duration":60,"position":1,"contentType":"audio/mpeg","frequency":44100,"bitRate":128,"_links":{"enclosure":{"href":"https://media.example/a.mp3?sig=secret"}}}]}}}}`}
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsPlaylist() || len(tx.requests) != 1 || !strings.HasPrefix(tx.requests[0], prxAPI+"stories/1") {
		t.Fatalf("unexpected story result: %#v %v", r, tx.requests)
	}
}
func TestPRXStatusAndPagination(t *testing.T) {
	for _, tc := range []struct {
		s    int
		want error
	}{{401, ErrAuthentication}, {404, ErrUnavailable}, {500, nil}} {
		tx := &prxTransport{status: tc.s, body: "{}"}
		_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Fatalf("%d: %v", tc.s, err)
		}
	}
	tx := &prxTransport{status: 200, body: `{"id":"2","title":"Series"}`}
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/2", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	a, err := CollectEntries(context.Background(), r.Entries, 2)
	if err != nil || len(a) != 0 {
		t.Fatalf("entries=%v err=%v", a, err)
	}
}

func TestPRXMultipartPartReentryAndNumericIDs(t *testing.T) {
	body := `{"id":1,"title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":11,"position":2,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/two.mp3"}}},{"id":"12","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/one.mp3"}}}]}}}}`
	tx := &prxTransport{status: http.StatusOK, body: body}
	parent, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil || !parent.IsPlaylist() {
		t.Fatalf("parent=%#v err=%v", parent, err)
	}
	entries, err := CollectEntries(context.Background(), parent.Entries, 3)
	if err != nil || len(entries) != 2 || entries[0].ID != "1_part1" || !strings.Contains(entries[0].URL, "prx_part=1") {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	resolved, err := NewPRXStory().Extract(context.Background(), Request{URL: entries[0].URL, Transport: tx})
	if err != nil || resolved.IsPlaylist() {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	id, _ := resolved.Info.Lookup("id").StringValue()
	if id != "1_part1" {
		t.Fatalf("part id=%q", id)
	}
}

func FuzzPRXTarget(f *testing.F) {
	f.Add("https://prx.org/stories/1")
	f.Fuzz(func(t *testing.T, raw string) {
		u, e := url.Parse(raw)
		if e == nil {
			_, _, _ = prxTarget(u)
		}
	})
}
