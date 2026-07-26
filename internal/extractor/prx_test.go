package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// --- test transport ----------------------------------------------------------

type prxTransport struct {
	mu       sync.Mutex
	body     string
	status   int
	bodies   []string
	statuses []int
	requests []string
	calls    int
}

func newPrxTransport(status int, body string) *prxTransport {
	return &prxTransport{status: status, body: body}
}

func newPrxTransportSequence(statuses []int, bodies []string) *prxTransport {
	return &prxTransport{statuses: statuses, bodies: bodies}
}

func (t *prxTransport) Do(_ context.Context, r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests = append(t.requests, r.URL.String())
	idx := t.calls
	t.calls++
	body := t.body
	status := t.status
	if len(t.bodies) > 0 && idx < len(t.bodies) {
		body = t.bodies[idx]
	}
	if len(t.statuses) > 0 && idx < len(t.statuses) {
		status = t.statuses[idx]
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}
func (t *prxTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, r *http.Request) (*http.Response, error) {
	return t.Do(ctx, r)
}
func (t *prxTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unused")
}

func (t *prxTransport) requestCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func (t *prxTransport) requestURLs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]string, len(t.requests))
	copy(cp, t.requests)
	return cp
}

// plainTransport only implements Transport, not CredentialIsolatedNoRedirectTransport
type plainTransport struct{}

func (p *plainTransport) Do(_ context.Context, _ *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
}
func (p *plainTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unused")
}

// --- routing tests -----------------------------------------------------------

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

func TestPRXNameAndSuitable(t *testing.T) {
	if got := (PRXStory{}).Name(); got != "prx_story" {
		t.Fatalf("PRXStory.Name = %q", got)
	}
	if got := (PRXSeries{}).Name(); got != "prx_series" {
		t.Fatalf("PRXSeries.Name = %q", got)
	}
	if got := (PRXAccount{}).Name(); got != "prx_account" {
		t.Fatalf("PRXAccount.Name = %q", got)
	}
	storyURL, _ := url.Parse("https://prx.org/stories/42")
	if !NewPRXStory().Suitable(storyURL) {
		t.Fatal("PRXStory not suitable for stories URL")
	}
	if NewPRXSeries().Suitable(storyURL) {
		t.Fatal("PRXSeries should not match stories URL")
	}
	if NewPRXAccount().Suitable(storyURL) {
		t.Fatal("PRXAccount should not match stories URL")
	}
	seriesURL, _ := url.Parse("https://prx.org/series/10")
	if !NewPRXSeries().Suitable(seriesURL) {
		t.Fatal("PRXSeries not suitable for series URL")
	}
	accountURL, _ := url.Parse("https://prx.org/accounts/5")
	if !NewPRXAccount().Suitable(accountURL) {
		t.Fatal("PRXAccount not suitable for accounts URL")
	}
}

func TestPRXUnsupportedURLs(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    string
	}{
		{"nil URL", ""},
		{"non-prx host", "https://example.com/stories/1"},
		{"wrong kind", "https://prx.org/episodes/1"},
		{"fragment", "https://prx.org/stories/1#frag"},
		{"http scheme", "http://prx.org/stories/1"},
		{"percent-encoded path", "https://prx.org/stories/1%2f2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := url.Parse(tc.u)
			if NewPRXStory().Suitable(u) {
				t.Fatalf("should not be suitable: %s", tc.u)
			}
		})
	}
}

// --- status/error code tests -------------------------------------------------

func TestPRXStatusAndPagination(t *testing.T) {
	for _, tc := range []struct {
		s    int
		want error
	}{{401, ErrAuthentication}, {403, ErrAuthentication}, {404, ErrUnavailable}, {410, ErrUnavailable}, {429, nil}, {500, nil}} {
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

func TestPRX403AuthenticationError(t *testing.T) {
	tx := newPrxTransport(403, `{"error":"forbidden"}`)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected ErrAuthentication, got %v", err)
	}
}

func TestPRX429RateLimit(t *testing.T) {
	tx := newPrxTransport(429, `{"error":"rate limited"}`)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "rate limited") && !strings.Contains(err.Error(), "429") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPRX5xxServerErrors(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		tx := newPrxTransport(code, `{"error":"server error"}`)
		_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
		if err == nil {
			t.Fatalf("%d: expected error", code)
		}
	}
}

func TestPRX3xxNonSuccess(t *testing.T) {
	tx := newPrxTransport(301, `<html>redirect</html>`)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err == nil {
		t.Fatal("expected error for 3xx")
	}
}

func TestPRX410Gone(t *testing.T) {
	tx := newPrxTransport(410, `{"error":"gone"}`)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for 410, got %v", err)
	}
}

// --- nil/oversized/malformed response tests ----------------------------------

func TestPRXNilResponse(t *testing.T) {
	// Transport that returns nil response
	tx := &nilTransport{}
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err == nil {
		t.Fatal("expected error for nil response")
	}
}

type nilTransport struct{}

func (n *nilTransport) Do(_ context.Context, _ *http.Request) (*http.Response, error) {
	return nil, nil
}
func (n *nilTransport) DoWithoutCredentialsNoRedirect(_ context.Context, _ *http.Request) (*http.Response, error) {
	return nil, nil
}
func (n *nilTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unused")
}

func TestPRXOversizedResponse(t *testing.T) {
	big := strings.Repeat("x", int(maxExtractorJSONBytes)+100)
	tx := newPrxTransport(200, big)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
}

func TestPRXMalformedJSON(t *testing.T) {
	tx := newPrxTransport(200, `{invalid json!!!}`)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestPRXTrailingJSON(t *testing.T) {
	tx := newPrxTransport(200, `{"id":"1"}{"trailing":true}`)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err == nil {
		t.Fatal("expected error for trailing JSON")
	}
}

// --- multipart story with no recursion on re-entry ---------------------------

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

func TestPRXMultipartNoRecursion(t *testing.T) {
	body := `{"id":"42","title":"Multi","_embedded":{"prx:audio":{"_embedded":{"prx:items":[
{"id":"p1","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}},
{"id":"p2","position":2,"contentType":"audio/aac","_links":{"enclosure":{"href":"https://media.example/b.aac"}}},
{"id":"p3","position":3,"contentType":"audio/ogg","_links":{"enclosure":{"href":"https://media.example/c.ogg"}}}
]}}}}`
	tx := newPrxTransport(200, body)

	parent, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/42", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if !parent.IsPlaylist() {
		t.Fatal("expected playlist for multi-part story")
	}

	entries, err := CollectEntries(context.Background(), parent.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	for i, e := range entries {
		expectedPart := fmt.Sprintf("42_part%d", i+1)
		if e.ID != expectedPart {
			t.Fatalf("entry %d: expected ID %q, got %q", i, expectedPart, e.ID)
		}
		if e.ExtractorKey != "prx_story" {
			t.Fatalf("entry %d: expected extractor key prx_story, got %q", i, e.ExtractorKey)
		}
		if !strings.Contains(e.URL, fmt.Sprintf("prx_part=%d", i+1)) {
			t.Fatalf("entry %d: URL missing prx_part: %s", i, e.URL)
		}
	}

	for _, e := range entries {
		resolved, err := NewPRXStory().Extract(context.Background(), Request{URL: e.URL, Transport: tx})
		if err != nil {
			t.Fatalf("re-entry for %s: %v", e.ID, err)
		}
		if resolved.IsPlaylist() {
			t.Fatalf("re-entry for %s should not produce playlist", e.ID)
		}
		resolvedID, _ := resolved.Info.Lookup("id").StringValue()
		if resolvedID != e.ID {
			t.Fatalf("re-entry id mismatch: expected %q, got %q", e.ID, resolvedID)
		}
	}
}

// --- single-part story returns Media directly --------------------------------

func TestPRXSinglePartReturnsMedia(t *testing.T) {
	body := `{"id":"100","title":"Single","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"s1","position":1,"contentType":"audio/mpeg","size":1024,"duration":30,"bitRate":128,"frequency":44100,"label":"main","_links":{"enclosure":{"href":"https://media.example/solo.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/100", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsPlaylist() {
		t.Fatal("single-part story should be Media, not playlist")
	}
	id, _ := r.Info.Lookup("id").StringValue()
	if id != "100" {
		t.Fatalf("expected id=100, got %q", id)
	}
	ext, _ := r.Info.Lookup("ext").StringValue()
	if ext != "mp3" {
		t.Fatalf("expected ext=mp3, got %q", ext)
	}
}

// --- numeric/string ID handling ----------------------------------------------

func TestPRXNumericAndStringIDs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		idJSON   string
		expected string
	}{
		{"string id", `"42"`, "42"},
		{"numeric id", `42`, "42"},
		{"large numeric id", `9999999999`, "9999999999"},
		{"string alphanumeric", `"abc123"`, "abc123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var id prxID
			if err := json.Unmarshal([]byte(tc.idJSON), &id); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.idJSON, err)
			}
			if string(id) != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, string(id))
			}
		})
	}
}

func TestPRXIDRejectsInvalid(t *testing.T) {
	for _, tc := range []string{`true`, `[]`, `{}`} {
		t.Run(tc, func(t *testing.T) {
			var id prxID
			if err := json.Unmarshal([]byte(tc), &id); err == nil {
				t.Fatalf("expected error for %s", tc)
			}
		})
	}
}

func TestPRXStoryWithNumericID(t *testing.T) {
	body := `{"id":77,"title":"NumStory","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":88,"position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/n.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/77", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.Info.Lookup("id").StringValue()
	if id != "77" {
		t.Fatalf("expected id=77, got %q", id)
	}
}

// --- identity mismatch -------------------------------------------------------

func TestPRXStoryIdentityMismatch(t *testing.T) {
	body := `{"id":"999","title":"Wrong","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/x.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata for identity mismatch, got %v", err)
	}
}

func TestPRXSeriesIdentityMismatch(t *testing.T) {
	body := `{"id":"wrong","title":"Series"}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/42", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata, got %v", err)
	}
}

func TestPRXAccountIdentityMismatch(t *testing.T) {
	body := `{"id":"wrong","title":"Account"}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/5", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata, got %v", err)
	}
}

// --- missing audio / empty pieces -------------------------------------------

func TestPRXStoryMissingAudio(t *testing.T) {
	tx := newPrxTransport(200, `{"id":"1","title":"No Audio"}`)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for missing audio, got %v", err)
	}
}

func TestPRXStoryEmptyPieces(t *testing.T) {
	body := `{"id":"1","title":"Empty","_embedded":{"prx:audio":{"_embedded":{"prx:items":[]}}}}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for empty pieces, got %v", err)
	}
}

func TestPRXStoryInvalidAudioPiece(t *testing.T) {
	body := `{"id":"1","title":"Bad Piece","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/x.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata for empty piece ID, got %v", err)
	}
}

func TestPRXStoryUnsafeEnclosureURL(t *testing.T) {
	body := `{"id":"1","title":"Bad URL","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"http://evil.example/x.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata for unsafe enclosure, got %v", err)
	}
}

// --- part query validation ---------------------------------------------------

func TestPRXPartQueryOK(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"https://prx.org/stories/1", true},
		{"https://prx.org/stories/1?prx_part=1", true},
		{"https://prx.org/stories/1?prx_part=100", true},
		{"https://prx.org/stories/1?prx_part=0", false},
		{"https://prx.org/stories/1?prx_part=101", false},
		{"https://prx.org/stories/1?prx_part=abc", false},
		{"https://prx.org/stories/1?other=1", false},
		{"https://prx.org/stories/1?prx_part=1&extra=2", false},
	} {
		u, _ := url.Parse(tc.raw)
		got := prxPartQueryOK(u)
		if got != tc.want {
			t.Fatalf("prxPartQueryOK(%s) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestPRXPartOutOfRange(t *testing.T) {
	body := `{"id":"1","title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1?prx_part=5", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata for out-of-range part, got %v", err)
	}
}

// --- transport isolation enforcement -----------------------------------------

func TestPRXRequiresIsolatedTransport(t *testing.T) {
	tx := &plainTransport{}
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("expected ErrTransportIsolation, got %v", err)
	}
}

func TestPRXNilTransport(t *testing.T) {
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported for nil transport, got %v", err)
	}
}

func TestPRXSeriesNilTransport(t *testing.T) {
	_, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/1"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func TestPRXAccountNilTransport(t *testing.T) {
	_, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/1"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

// --- hostile image URLs and secret safety ------------------------------------

func TestPRXHostileImageURL(t *testing.T) {
	body := `{"id":"1","title":"Story","_embedded":{"prx:image":{"_links":{"enclosure":{"href":"http://evil.com/steal"}}},"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata for unsafe image URL, got %v", err)
	}
}

func TestPRXSecretNotInError(t *testing.T) {
	body := `{"id":"1","title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3?sig=supersecrettoken123"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsPlaylist() {
		t.Fatal("expected media result")
	}
}

func TestPRXHostileAudioURL(t *testing.T) {
	body := `{"id":"1","title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"ftp://evil.com/steal"}}}]}}}}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata for ftp audio URL, got %v", err)
	}
}

// --- series collection -------------------------------------------------------

func TestPRXSeriesCollection(t *testing.T) {
	body := `{"id":"10","title":"MySeries","description":"A series","_embedded":{"prx:account":{"id":"5","name":"Owner"}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsPlaylist() {
		t.Fatal("series should be playlist")
	}
	v, _ := r.Info.Lookup("series_id").StringValue()
	if v != "10" {
		t.Fatalf("expected series_id=10, got %q", v)
	}
	v, _ = r.Info.Lookup("channel_id").StringValue()
	if v != "5" {
		t.Fatalf("expected channel_id=5, got %q", v)
	}
}

func TestPRXSeriesAccountMetadata(t *testing.T) {
	body := `{"id":"20","title":"TestSeries","_embedded":{"prx:account":{"id":"99","name":"Acme","title":"Acme Studios"}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/20", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	channel, _ := r.Info.Lookup("channel").StringValue()
	if channel != "Acme" {
		t.Fatalf("expected channel=Acme, got %q", channel)
	}
	series, _ := r.Info.Lookup("series").StringValue()
	if series != "TestSeries" {
		t.Fatalf("expected series=TestSeries, got %q", series)
	}
}

func TestPRXSeriesWithoutAccount(t *testing.T) {
	body := `{"id":"30","title":"SoloSeries"}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/30", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	_, ok := r.Info.Lookup("channel_id").StringValue()
	if ok {
		t.Fatal("expected no channel_id for series without account")
	}
}

// --- account collection ------------------------------------------------------

func TestPRXAccountCollection(t *testing.T) {
	body := `{"id":"5","name":"Acme Podcasts","description":"Our shows"}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/5", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsPlaylist() {
		t.Fatal("account should be playlist")
	}
	ch, _ := r.Info.Lookup("channel_id").StringValue()
	if ch != "5" {
		t.Fatalf("expected channel_id=5, got %q", ch)
	}
	name, _ := r.Info.Lookup("channel").StringValue()
	if name != "Acme Podcasts" {
		t.Fatalf("expected channel=Acme Podcasts, got %q", name)
	}
	curl, _ := r.Info.Lookup("channel_url").StringValue()
	if !strings.Contains(curl, "accounts/5") {
		t.Fatalf("expected channel_url to contain accounts/5, got %q", curl)
	}
}

func TestPRXAccountUsesNameOverTitle(t *testing.T) {
	body := `{"id":"6","name":"Primary","title":"Secondary"}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/6", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	title, _ := r.Info.Lookup("title").StringValue()
	if title != "Primary" {
		t.Fatalf("expected title to prefer Name, got %q", title)
	}
}

func TestPRXAccountEmptyNameFallsBackToTitle(t *testing.T) {
	body := `{"id":"7","title":"Fallback"}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/7", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	title, _ := r.Info.Lookup("title").StringValue()
	if title != "Fallback" {
		t.Fatalf("expected title=Fallback, got %q", title)
	}
}

// --- account endpoints fetch both series and stories -------------------------

func TestPRXAccountFetchesSeriesAndStories(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200, 200},
		[]string{
			`{"id":"5","name":"Acme"}`,
			`{"count":1,"total":1,"_embedded":{"prx:items":[{"id":"11","title":"Show A"}]}}`,
			`{"count":1,"total":1,"_embedded":{"prx:items":[{"id":"12","title":"Ep 1"}]}}`,
		},
	)
	r, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/5", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "11" || entries[0].ExtractorKey != "prx_series" || entries[1].ID != "12" || entries[1].ExtractorKey != "prx_story" {
		t.Fatalf("unexpected account entries: %#v", entries)
	}
	if len(tx.requests) != 3 || !strings.Contains(tx.requests[1], "/accounts/5/series") || !strings.Contains(tx.requests[2], "/accounts/5/stories") {
		t.Fatalf("wrong request order: %v", tx.requests)
	}
}

// --- series multipage pagination ---------------------------------------------

func TestPRXSeriesPagination(t *testing.T) {
	page1 := `{"id":"10","title":"Series","count":2,"total":3,"_embedded":{"prx:items":[{"id":"101","title":"Ep1"},{"id":"102","title":"Ep2"}]}}`
	page2 := `{"id":"10","title":"Series","count":1,"total":3,"_embedded":{"prx:items":[{"id":"103","title":"Ep3"}]}}`

	tx := newPrxTransportSequence(
		[]int{200, 200, 200},
		[]string{
			`{"id":"10","title":"Series"}`,
			page1,
			page2,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].ID != "101" || entries[1].ID != "102" || entries[2].ID != "103" {
		t.Fatalf("unexpected entry IDs: %v", []string{entries[0].ID, entries[1].ID, entries[2].ID})
	}
}

func TestPRXSeriesEmptyPageAdvancesEndpoint(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200},
		[]string{
			`{"id":"10","title":"Series"}`,
			`{"id":"10","title":"Series","count":0,"total":0}`,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries from empty page, got %d", len(entries))
	}
}

// --- account multi-endpoint pagination ---------------------------------------

func TestPRXAccountPagination(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200, 200},
		[]string{
			`{"id":"5","name":"Acme"}`,
			`{"id":"5","name":"Acme","count":1,"total":1,"_embedded":{"prx:items":[{"id":"51","title":"Series1"}]}}`,
			`{"id":"5","name":"Acme","count":1,"total":1,"_embedded":{"prx:items":[{"id":"52","title":"Ep1"}]}}`,
		},
	)
	r, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/5", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (series + story), got %d", len(entries))
	}
	if entries[0].ExtractorKey != "prx_series" {
		t.Fatalf("first entry should be series, got %q", entries[0].ExtractorKey)
	}
	if entries[1].ExtractorKey != "prx_story" {
		t.Fatalf("second entry should be story, got %q", entries[1].ExtractorKey)
	}
}

func TestPRXAccountEndpointTransition(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200, 200},
		[]string{
			`{"id":"5","name":"Acme"}`,
			`{"id":"5","count":0,"total":0}`,
			`{"id":"5","count":1,"total":1,"_embedded":{"prx:items":[{"id":"52","title":"Ep1"}]}}`,
		},
	)
	r, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/5", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after endpoint transition, got %d", len(entries))
	}
}

// --- final page boundary detection -------------------------------------------

func TestPRXFinalPageBoundary(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200},
		[]string{
			`{"id":"10","title":"Series"}`,
			`{"id":"10","count":2,"total":2,"_embedded":{"prx:items":[{"id":"101","title":"E1"},{"id":"102","title":"E2"}]}}`,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries at final boundary, got %d", len(entries))
	}
	if tx.requestCount() != 2 {
		t.Fatalf("expected 2 requests (resource + 1 page), got %d", tx.requestCount())
	}
}

// --- iterator reuse ----------------------------------------------------------

func TestPRXIteratorReuse(t *testing.T) {
	body := `{"id":"10","count":1,"total":1,"_embedded":{"prx:items":[{"id":"101","title":"E1"}]}}`
	tx := newPrxTransport(200, body)

	entries := prxEntries{transport: tx, endpoints: []string{"series/10/stories"}}

	a1, err := CollectEntries(context.Background(), entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := CollectEntries(context.Background(), entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(a1) != len(a2) || (len(a1) > 0 && a1[0].ID != a2[0].ID) {
		t.Fatalf("iterator reuse produced different results: %v vs %v", a1, a2)
	}
}

// --- cancelled context -------------------------------------------------------

func TestPRXCancelledContext(t *testing.T) {
	body := `{"id":"1","title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewPRXStory().Extract(ctx, Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestPRXPaginationCancelledContext(t *testing.T) {
	body := `{"id":"10","title":"Series","count":5,"total":10,"_embedded":{"prx:items":[{"id":"e1","title":"E1"}]}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = CollectEntries(ctx, r.Entries, 10)
	if err == nil {
		t.Fatal("expected error for cancelled context in pagination")
	}
}

// --- malformed pagination metadata -------------------------------------------

func TestPRXMalformedPaginationMetadata(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200},
		[]string{
			`{"id":"10","title":"Series"}`,
			`{"id":"10","count":"not_a_number","total":"also_not","_embedded":{"prx:items":[{"id":"e1","title":"E1"}]}}`,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), r.Entries, 10)
	if err == nil {
		t.Fatal("expected error for malformed pagination metadata")
	}
}

func TestPRXCountExceedsTotal(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200},
		[]string{
			`{"id":"10","title":"Series"}`,
			`{"id":"10","count":5,"total":2,"_embedded":{"prx:items":[{"id":"e1","title":"E1"}]}}`,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), r.Entries, 10)
	if err == nil {
		t.Fatal("expected error when count > total")
	}
}

func TestPRXCountExceeds100(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200},
		[]string{
			`{"id":"10","title":"Series"}`,
			`{"id":"10","count":101,"total":200,"_embedded":{"prx:items":[]}}`,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), r.Entries, 10)
	if err == nil {
		t.Fatal("expected error when count > 100")
	}
}

// --- skippable empty-ID items in iterator ------------------------------------

func TestPRXSkipsEmptyIDsInIterator(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200},
		[]string{
			`{"id":"10","title":"Series"}`,
			`{"id":"10","count":2,"total":2,"_embedded":{"prx:items":[{"id":"","title":"Skip"},{"id":"101","title":"Keep"}]}}`,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "101" {
		t.Fatalf("expected only entry e1, got %v", entries)
	}
}

// --- description / tags / timestamps -----------------------------------------

func TestPRXInfoFields(t *testing.T) {
	body := `{"id":"1","title":"T","description":"<p>Desc</p>","shortDescription":"Short","releasedAt":"2024-01-15T10:00:00Z","createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-06-15T12:00:00Z","duration":120,"episodeIdentifier":3,"seasonIdentifier":1,"tags":["tag1","tag2"],"_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/aac","_links":{"enclosure":{"href":"https://media.example/a.aac"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	desc, _ := r.Info.Lookup("description").StringValue()
	if !strings.Contains(desc, "Desc") || strings.Contains(desc, "<p>") {
		t.Fatalf("description not stripped: %q", desc)
	}
	dur, _ := r.Info.Lookup("duration").Int()
	if dur != 120 {
		t.Fatalf("expected duration=120, got %d", dur)
	}
	ep, _ := r.Info.Lookup("episode_number").Int()
	if ep != 3 {
		t.Fatalf("expected episode_number=3, got %d", ep)
	}
	sn, _ := r.Info.Lookup("season_number").Int()
	if sn != 1 {
		t.Fatalf("expected season_number=1, got %d", sn)
	}
	ext, _ := r.Info.Lookup("ext").StringValue()
	if ext != "aac" {
		t.Fatalf("expected ext=aac, got %q", ext)
	}
}

func TestPRXStorySeriesRelation(t *testing.T) {
	body := `{"id":"1","title":"Ep","_embedded":{"prx:series":{"id":"10","title":"Show"},"prx:account":{"id":"5","name":"Channel"},"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := r.Info.Lookup("series_id").StringValue()
	if sid != "10" {
		t.Fatalf("expected series_id=10, got %q", sid)
	}
	cid, _ := r.Info.Lookup("channel_id").StringValue()
	if cid != "5" {
		t.Fatalf("expected channel_id=5, got %q", cid)
	}
}

func TestPRXStoryWithoutSeries(t *testing.T) {
	body := `{"id":"1","title":"Standalone","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	_, ok := r.Info.Lookup("series_id").StringValue()
	if ok {
		t.Fatal("expected no series_id for standalone story")
	}
}

// --- format fields -----------------------------------------------------------

func TestPRXFormatFields(t *testing.T) {
	body := `{"id":"1","title":"T","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"p1","label":"Main","size":1048576,"duration":60,"position":1,"contentType":"audio/mpeg","frequency":44100,"bitRate":192,"_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsPlaylist() {
		t.Fatal("expected media")
	}
	formats, ok := r.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("expected 1 format, got %v", formats)
	}
	f, _ := formats[0].Object()
	v, _ := f.Lookup("format_id").StringValue()
	if v != "p1" {
		t.Fatalf("format_id=%q", v)
	}
	v, _ = f.Lookup("format_note").StringValue()
	if v != "Main" {
		t.Fatalf("format_note=%q", v)
	}
	v, _ = f.Lookup("vcodec").StringValue()
	if v != "none" {
		t.Fatalf("vcodec=%q", v)
	}
	v, _ = f.Lookup("ext").StringValue()
	if v != "mp3" {
		t.Fatalf("ext=%q", v)
	}
}

// --- prxExt coverage ---------------------------------------------------------

func TestPRXExtCoverage(t *testing.T) {
	for _, tc := range []struct {
		ct   string
		want string
	}{
		{"audio/mpeg", "mp3"},
		{"audio/aac", "aac"},
		{"audio/ogg", "ogg"},
		{"audio/flac", "flac"},
		{"audio/x-m4a", "m4a"},
		{"", ""},
	} {
		got := prxExt(tc.ct)
		if got != tc.want {
			t.Fatalf("prxExt(%q) = %q, want %q", tc.ct, got, tc.want)
		}
	}
}

// --- prxTime / prxNumber -----------------------------------------------------

func TestPRXTimeParsing(t *testing.T) {
	if v := prxTime("2024-01-15T10:00:00Z"); v <= 0 {
		t.Fatalf("prxTime failed for RFC3339: %d", v)
	}
	if v := prxTime("2024-01-15T10:00:00.123456789Z"); v <= 0 {
		t.Fatalf("prxTime failed for RFC3339Nano: %d", v)
	}
	if v := prxTime("not-a-date"); v != 0 {
		t.Fatalf("prxTime should return 0 for invalid: %d", v)
	}
	if v := prxTime(""); v != 0 {
		t.Fatalf("prxTime should return 0 for empty: %d", v)
	}
}

func TestPRXNumberParsing(t *testing.T) {
	v, ok := prxNumber("42")
	if !ok || v != 42 {
		t.Fatalf("prxNumber('42') = %d, %v", v, ok)
	}
	v, ok = prxNumber("-1")
	if ok {
		t.Fatalf("prxNumber('-1') should reject negative")
	}
	v, ok = prxNumber("")
	if ok {
		t.Fatalf("prxNumber('') should reject empty")
	}
	v, ok = prxNumber("abc")
	if ok {
		t.Fatalf("prxNumber('abc') should reject")
	}
}

func TestPRXURLValidation(t *testing.T) {
	if !prxURL("https://media.example/a.mp3") {
		t.Fatal("valid URL rejected")
	}
	if prxURL("http://media.example/a.mp3") {
		t.Fatal("http URL accepted")
	}
	if prxURL("ftp://media.example/a.mp3") {
		t.Fatal("ftp URL accepted")
	}
	if prxURL("") {
		t.Fatal("empty URL accepted")
	}
}

// --- PRX part re-entry format ------------------------------------------------

func TestPRXPartReentrySetsExt(t *testing.T) {
	body := `{"id":"1","title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/aac","_links":{"enclosure":{"href":"https://media.example/a.aac"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1?prx_part=1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	ext, _ := r.Info.Lookup("ext").StringValue()
	if ext != "aac" {
		t.Fatalf("expected ext=aac for re-entry, got %q", ext)
	}
}

// --- story with all hostnames -----------------------------------------------

func TestPRXAllHostnames(t *testing.T) {
	body := `{"id":"1","title":"T","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	for _, host := range []string{"prx.org", "beta.prx.org", "listen.prx.org"} {
		u := fmt.Sprintf("https://%s/stories/1", host)
		r, err := NewPRXStory().Extract(context.Background(), Request{URL: u, Transport: tx})
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if r.IsPlaylist() {
			t.Fatalf("%s: expected media", host)
		}
	}
}

// --- description fallback: shortDescription when description empty -----------

func TestPRXDescriptionFallback(t *testing.T) {
	body := `{"id":"1","title":"T","shortDescription":"Short only","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	desc, _ := r.Info.Lookup("description").StringValue()
	if desc != "Short only" {
		t.Fatalf("expected Short only, got %q", desc)
	}
}

func TestPRXEmptyTitleFallsBackToID(t *testing.T) {
	body := `{"id":"42","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/42", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	title, _ := r.Info.Lookup("title").StringValue()
	if title != "42" {
		t.Fatalf("expected title to fall back to ID, got %q", title)
	}
}

// --- series entry titles propagate -------------------------------------------

func TestPRXSeriesEntryTitles(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200},
		[]string{
			`{"id":"10","title":"MySeries"}`,
			`{"id":"10","count":1,"total":1,"_embedded":{"prx:items":[{"id":"101","title":"Episode One"}]}}`,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Title != "Episode One" {
		t.Fatalf("expected title 'Episode One', got %q", entries[0].Title)
	}
	if entries[0].URL != "https://beta.prx.org/stories/101" {
		t.Fatalf("unexpected URL: %s", entries[0].URL)
	}
}

// --- registry integration ----------------------------------------------------

func TestPRXRegistryIntegration(t *testing.T) {
	reg := NewRegistry(
		NewPRXStory(),
		NewPRXSeries(),
		NewPRXAccount(),
	)
	names := reg.Names()
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	for _, want := range []string{"prx_story", "prx_series", "prx_account"} {
		if !found[want] {
			t.Fatalf("registry missing %q", want)
		}
	}
}

func TestPRXRegistrySelect(t *testing.T) {
	reg := NewRegistry(NewPRXStory(), NewPRXSeries(), NewPRXAccount())
	ex, err := reg.Select("https://prx.org/stories/1")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Name() != "prx_story" {
		t.Fatalf("expected prx_story, got %q", ex.Name())
	}
	ex, err = reg.Select("https://prx.org/series/2")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Name() != "prx_series" {
		t.Fatalf("expected prx_series, got %q", ex.Name())
	}
	ex, err = reg.Select("https://prx.org/accounts/3")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Name() != "prx_account" {
		t.Fatalf("expected prx_account, got %q", ex.Name())
	}
}

func TestPRXRegistrySelectFor(t *testing.T) {
	reg := NewRegistry(NewPRXStory(), NewPRXSeries(), NewPRXAccount())
	ex, err := reg.SelectFor("https://prx.org/stories/1", "prx_story")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Name() != "prx_story" {
		t.Fatalf("expected prx_story, got %q", ex.Name())
	}
}

func TestPRXRegistryRejectsNonPRX(t *testing.T) {
	reg := NewRegistry(NewPRXStory(), NewPRXSeries(), NewPRXAccount())
	_, err := reg.Select("https://example.com/stories/1")
	if err == nil {
		t.Fatal("expected error for non-PRX URL")
	}
}

// --- fuzz targets ------------------------------------------------------------

func FuzzPRXTarget(f *testing.F) {
	f.Add("https://prx.org/stories/1")
	f.Add("https://beta.prx.org/series/2")
	f.Add("https://listen.prx.org/accounts/3")
	f.Add("http://prx.org/stories/1")
	f.Add("https://evilprx.org/stories/1")
	f.Add("https://prx.org/stories/1#x")
	f.Add("https://prx.org:443/stories/1")
	f.Add("https://prx.org/stories/1%2f2")
	f.Add("https://prx.org/stories/abc")
	f.Add("")
	f.Fuzz(func(t *testing.T, raw string) {
		u, e := url.Parse(raw)
		if e == nil {
			_, _, _ = prxTarget(u)
		}
	})
}

func FuzzPRXID(f *testing.F) {
	f.Add(`"hello"`)
	f.Add(`123`)
	f.Add(`null`)
	f.Add(`true`)
	f.Add(`[]`)
	f.Add(`{}`)
	f.Add(`"42"`)
	f.Add(`9999999999`)
	f.Fuzz(func(t *testing.T, data string) {
		var id prxID
		_ = json.Unmarshal([]byte(data), &id)
	})
}

func FuzzPRXExt(f *testing.F) {
	f.Add("audio/mpeg")
	f.Add("audio/aac")
	f.Add("audio/ogg")
	f.Add("audio/flac")
	f.Add("")
	f.Add("AUDIO/MPEG")
	f.Add("text/plain")
	f.Add("video/mp4")
	f.Fuzz(func(t *testing.T, ct string) {
		r := prxExt(ct)
		if r == "" {
			t.Fatal("prxExt returned empty string")
		}
	})
}

func FuzzPRXStripHTML(f *testing.F) {
	f.Add("<p>hello</p>")
	f.Add("<script>alert(1)</script>")
	f.Add("no tags")
	f.Add("<>")
	f.Add("<img src=x>")
	f.Add("")
	f.Add("<script>")
	f.Add("</script>")
	f.Fuzz(func(t *testing.T, s string) {
		result := stripPRXHTML(s)
		if strings.Contains(result, "<script>") || strings.Contains(result, "</script>") {
			t.Fatal("stripPRXHTML left complete script tags")
		}
	})
}

func FuzzPRXURL(f *testing.F) {
	f.Add("https://example.com/a.mp3")
	f.Add("http://example.com/a.mp3")
	f.Add("ftp://example.com/a.mp3")
	f.Add("")
	f.Add("not-a-url")
	f.Fuzz(func(t *testing.T, s string) {
		_ = prxURL(s)
	})
}

func FuzzPRXPartQueryOK(f *testing.F) {
	f.Add("https://prx.org/stories/1")
	f.Add("https://prx.org/stories/1?prx_part=1")
	f.Add("https://prx.org/stories/1?prx_part=abc")
	f.Add("https://prx.org/stories/1?other=1")
	f.Fuzz(func(t *testing.T, raw string) {
		u, e := url.Parse(raw)
		if e == nil {
			_ = prxPartQueryOK(u)
		}
	})
}

// --- concurrency safety (race detector) --------------------------------------

func TestPRXConcurrentExtraction(t *testing.T) {
	body := `{"id":"1","title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
			if err != nil {
				t.Errorf("concurrent extract: %v", err)
			}
		}()
	}
	wg.Wait()
}

// --- part boundary: part=0 returns playlist, part=1 returns media -----------

func TestPRXPartZeroVsPartOne(t *testing.T) {
	body := `{"id":"1","title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}},{"id":"b","position":2,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/b.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)

	// part=0 should be playlist
	r0, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if !r0.IsPlaylist() {
		t.Fatal("part=0 should be playlist")
	}

	// part=1 should be media
	r1, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1?prx_part=1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if r1.IsPlaylist() {
		t.Fatal("part=1 should be media")
	}
}

// --- story without image, without account, without series --------------------

func TestPRXMinimalStory(t *testing.T) {
	body := `{"id":"1","title":"Minimal","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if r.IsPlaylist() {
		t.Fatal("expected media")
	}
}

// --- story without description at all ----------------------------------------

func TestPRXStoryNoDescription(t *testing.T) {
	body := `{"id":"1","title":"NoDesc","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	_, ok := r.Info.Lookup("description").StringValue()
	if ok {
		t.Fatal("expected no description field")
	}
}

// --- piece sorting by position -----------------------------------------------

func TestPRXPieceSorting(t *testing.T) {
	body := `{"id":"1","title":"T","_embedded":{"prx:audio":{"_embedded":{"prx:items":[
{"id":"c","position":3,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/c.mp3"}}},
{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}},
{"id":"b","position":2,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/b.mp3"}}}
]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].ID != "1_part1" || entries[1].ID != "1_part2" || entries[2].ID != "1_part3" {
		t.Fatalf("entries not sorted: %v", []string{entries[0].ID, entries[1].ID, entries[2].ID})
	}
}

// --- story with unsafe image but valid audio ---------------------------------

func TestPRXUnsafeImageValidAudio(t *testing.T) {
	body := `{"id":"1","title":"T","_embedded":{"prx:image":{"_links":{"enclosure":{"href":"javascript:alert(1)"}}},"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	_, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata for javascript: image URL, got %v", err)
	}
}

// --- series with embedded items directly (no pagination needed) --------------

func TestPRXSeriesWithEmbeddedItems(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200},
		[]string{
			`{"id":"10","title":"Series"}`,
			`{"id":"10","count":1,"total":1,"_embedded":{"prx:items":[{"id":"101","title":"Ep1"}]}}`,
		},
	)
	r, err := NewPRXSeries().Extract(context.Background(), Request{URL: "https://prx.org/series/10", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ID != "101" || entries[0].Title != "Ep1" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

// --- account endpoint fetches series entries with correct extractor key ------

func TestPRXAccountSeriesEntriesKey(t *testing.T) {
	tx := newPrxTransportSequence(
		[]int{200, 200, 200},
		[]string{
			`{"id":"5","name":"Acme"}`,
			`{"id":"5","count":1,"total":1,"_embedded":{"prx:items":[{"id":"51","title":"Show","name":"Show"}]}}`,
			`{"id":"5","count":0,"total":0}`,
		},
	)
	r, err := NewPRXAccount().Extract(context.Background(), Request{URL: "https://prx.org/accounts/5", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), r.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ExtractorKey != "prx_series" {
		t.Fatalf("expected prx_series key, got %q", entries[0].ExtractorKey)
	}
	if entries[0].URL != "https://beta.prx.org/series/51" {
		t.Fatalf("unexpected URL: %s", entries[0].URL)
	}
}

// --- prxPart edge cases ------------------------------------------------------

func TestPRXPartWithNilURL(t *testing.T) {
	// prxPart should handle nil gracefully
	if v := prxPart(nil); v != 0 {
		t.Fatalf("prxPart(nil) = %d, want 0", v)
	}
}

// --- image with valid HTTPS URL ----------------------------------------------

func TestPRXValidImageURL(t *testing.T) {
	body := `{"id":"1","title":"T","_embedded":{"prx:image":{"_links":{"enclosure":{"href":"https://cdn.prx.org/image.jpg"}}},"prx:audio":{"_embedded":{"prx:items":[{"id":"a","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`
	tx := newPrxTransport(200, body)
	r, err := NewPRXStory().Extract(context.Background(), Request{URL: "https://prx.org/stories/1", Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	thumb, _ := r.Info.Lookup("thumbnail").StringValue()
	if thumb != "https://cdn.prx.org/image.jpg" {
		t.Fatalf("expected thumbnail, got %q", thumb)
	}
}
