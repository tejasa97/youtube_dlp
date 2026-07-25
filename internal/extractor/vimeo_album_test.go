package extractor

import (
	"context"
	"encoding/base64"
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
)

type vimeoAlbumFixtureTransport struct {
	mu sync.Mutex

	viewer   []byte
	viewers  [][]byte
	metadata []byte
	pages    map[int][]byte
	status   map[string]int
	calls    []string
	tokens   map[string]bool

	blockPage   int
	pageStarted chan struct{}
	startOnce   sync.Once
}

func newVimeoAlbumFixtureTransport(t *testing.T) *vimeoAlbumFixtureTransport {
	t.Helper()
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "vimeo", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	return &vimeoAlbumFixtureTransport{
		viewer:   read("album-viewer.json"),
		metadata: read("album-metadata.json"),
		pages:    map[int][]byte{1: read("album-videos-page1.json")},
		status:   make(map[string]int),
		tokens: map[string]bool{
			"eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjQxMDI0NDQ4MDB9.c3ludGhldGlj": true,
		},
		pageStarted: make(chan struct{}),
	}
}

func (*vimeoAlbumFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("ambient transport must not be used")
}

func (*vimeoAlbumFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("ambient page transport must not be used")
}

func (transport *vimeoAlbumFixtureTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.record(request)
	if request.Method != http.MethodGet || request.URL.String() != "https://vimeo.com/_next/viewer" ||
		request.Header.Get("Accept") != "application/json" || request.Header.Get("Authorization") != "" ||
		request.Header.Get("Cookie") != "" || request.Header.Get("Proxy-Authorization") != "" {
		return nil, fmt.Errorf("unexpected viewer request: %s %s %#v", request.Method, request.URL, request.Header)
	}
	return vimeoAlbumResponse(transport.status["viewer"], transport.nextViewer()), nil
}

func (transport *vimeoAlbumFixtureTransport) DoWithScopedAuthorizationNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.record(request)
	authorization := request.Header.Get("Authorization")
	if request.Method != http.MethodGet || request.URL.Scheme != "https" ||
		request.URL.Host != "api.vimeo.com" || request.Header.Get("Accept") != "application/json" ||
		!strings.HasPrefix(authorization, "jwt ") || !transport.tokenAllowed(strings.TrimPrefix(authorization, "jwt ")) ||
		request.Header.Get("Cookie") != "" || request.Header.Get("Proxy-Authorization") != "" {
		return nil, fmt.Errorf("unexpected scoped request: %s %s %#v", request.Method, request.URL, request.Header)
	}
	query := request.URL.Query()
	if query.Get("is_embed") != "false" || query.Get("referrer") != "" {
		return nil, fmt.Errorf("unexpected album scope query: %s", request.URL.RawQuery)
	}
	switch request.URL.Path {
	case "/albums/7":
		if query.Get("fields") != "description,name,privacy" || len(query) != 3 {
			return nil, fmt.Errorf("unexpected metadata query: %s", request.URL.RawQuery)
		}
		return vimeoAlbumResponse(transport.status["metadata"], transport.metadata), nil
	case "/albums/7/videos":
		if query.Get("fields") != "link,uri" || query.Get("per_page") != "100" || len(query) != 5 {
			return nil, fmt.Errorf("unexpected videos query: %s", request.URL.RawQuery)
		}
		page, err := strconv.Atoi(query.Get("page"))
		if err != nil || page < 1 {
			return nil, fmt.Errorf("invalid page: %s", query.Get("page"))
		}
		if transport.blockPage == page {
			transport.startOnce.Do(func() { close(transport.pageStarted) })
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return vimeoAlbumResponse(transport.status["page:"+strconv.Itoa(page)], transport.pages[page]), nil
	default:
		return nil, fmt.Errorf("unexpected API path: %s", request.URL.Path)
	}
}

func (transport *vimeoAlbumFixtureTransport) nextViewer() []byte {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.viewers) == 0 {
		return append([]byte(nil), transport.viewer...)
	}
	next := append([]byte(nil), transport.viewers[0]...)
	transport.viewers = transport.viewers[1:]
	return next
}

func (transport *vimeoAlbumFixtureTransport) tokenAllowed(token string) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.tokens[token]
}

func (transport *vimeoAlbumFixtureTransport) record(request *http.Request) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.calls = append(transport.calls, request.URL.String())
}

func (transport *vimeoAlbumFixtureTransport) countPath(path string) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	count := 0
	for _, raw := range transport.calls {
		parsed, _ := url.Parse(raw)
		if parsed.Path == path {
			count++
		}
	}
	return count
}

func vimeoAlbumResponse(status int, body []byte) *http.Response {
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func TestVimeoAlbumRoutesAndUnsafeRejection(t *testing.T) {
	for _, rawURL := range []string{
		"https://vimeo.com/album/7",
		"https://www.vimeo.com/showcase/7",
	} {
		if !NewVimeo().Suitable(mustParseURL(t, rawURL)) {
			t.Errorf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{
		"http://vimeo.com/album/7",
		"https://user:secret@vimeo.com/album/7",
		"https://vimeo.com:443/album/7",
		"https://evil.example/album/7",
		"https://vimeo.com/album/0",
		"https://vimeo.com/album/not-numeric",
		"https://vimeo.com/album/7/",
		"https://vimeo.com/album/%37",
		"https://vimeo.com/album/7/extra",
		"https://vimeo.com/albums/7",
		"https://vimeo.com/showcase/7?token=secret",
		"https://vimeo.com/showcase/7?",
		"https://vimeo.com/showcase/7#fragment",
		"https://vimeo.com/showcase%2f7",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if NewVimeo().Suitable(mustParseURL(t, rawURL)) {
				t.Fatal("Suitable = true")
			}
			transport := newVimeoAlbumFixtureTransport(t)
			_, err := NewVimeo().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Extract error = %v, want ErrUnsupported", err)
			}
			if len(transport.calls) != 0 {
				t.Fatalf("rejected URL made calls: %v", transport.calls)
			}
		})
	}
}

func TestVimeoAlbumPlaylistIsLazyReusableAndFiltersHostileRows(t *testing.T) {
	transport := newVimeoAlbumFixtureTransport(t)
	result, err := NewVimeo().Extract(context.Background(), Request{
		URL: "https://vimeo.com/showcase/7", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"id": "7", "title": "Synthetic Public Showcase",
		"description": "Deterministic public album metadata.",
		"webpage_url": "https://vimeo.com/showcase/7",
	} {
		got, _ := result.Info.Lookup(key).StringValue()
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if transport.countPath("/albums/7/videos") != 0 {
		t.Fatal("videos fetched eagerly")
	}
	for iteration := 0; iteration < 2; iteration++ {
		entries, collectErr := CollectEntries(context.Background(), result.Entries, 10)
		if collectErr != nil {
			t.Fatal(collectErr)
		}
		if len(entries) != 2 || entries[0].ID != "123456789" || entries[1].ID != "234567890" ||
			entries[0].URL != "https://vimeo.com/123456789" || !entries[0].Transparent {
			t.Fatalf("iteration %d entries = %#v", iteration, entries)
		}
	}
	if transport.countPath("/albums/7/videos") != 2 {
		t.Fatalf("video page calls = %d", transport.countPath("/albums/7/videos"))
	}
}

func TestVimeoAlbumMultiPageOrderDedupeAndHTTP400End(t *testing.T) {
	transport := newVimeoAlbumFixtureTransport(t)
	first := vimeoAlbumVideoPage{}
	for index := 1; index <= vimeoAlbumPageSize; index++ {
		id := fmt.Sprintf("%09d", index)
		first.Data = append(first.Data, struct {
			Link string `json:"link"`
			URI  string `json:"uri"`
		}{Link: "https://vimeo.com/" + id, URI: "/videos/" + id})
	}
	second := vimeoAlbumVideoPage{}
	for _, id := range []string{"000000100", "000000101"} {
		second.Data = append(second.Data, struct {
			Link string `json:"link"`
			URI  string `json:"uri"`
		}{Link: "https://vimeo.com/" + id, URI: "/videos/" + id})
	}
	transport.pages[1], _ = json.Marshal(first)
	transport.pages[2], _ = json.Marshal(second)
	result, err := NewVimeo().Extract(context.Background(), Request{
		URL: "https://vimeo.com/album/7", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 101 || entries[0].ID != "000000001" || entries[100].ID != "000000101" {
		t.Fatalf("entries len=%d first=%#v last=%#v", len(entries), entries[0], entries[len(entries)-1])
	}

	transport400 := newVimeoAlbumFixtureTransport(t)
	transport400.pages[1] = transport.pages[1]
	transport400.status["page:2"] = http.StatusBadRequest
	result, err = NewVimeo().Extract(context.Background(), Request{
		URL: "https://vimeo.com/album/7", Transport: transport400,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err = CollectEntries(context.Background(), result.Entries, 200)
	if err != nil || len(entries) != 100 {
		t.Fatalf("HTTP 400 termination entries=%d error=%v", len(entries), err)
	}
}

func TestVimeoAlbumRefreshesJWTForDelayedReusableIteration(t *testing.T) {
	transport := newVimeoAlbumFixtureTransport(t)
	base := time.Now().Truncate(time.Second)
	firstToken := vimeoAlbumTestJWT(base.Add(10*time.Minute).Unix(), "first")
	secondToken := vimeoAlbumTestJWT(base.Add(2*time.Hour).Unix(), "second")
	transport.viewers = [][]byte{
		[]byte(`{"jwt":"` + firstToken + `"}`),
		[]byte(`{"jwt":"` + secondToken + `"}`),
	}
	transport.tokens = map[string]bool{firstToken: true, secondToken: true}
	result, err := NewVimeo().Extract(context.Background(), Request{
		URL: "https://vimeo.com/album/7", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	sequence, ok := result.Entries.(vimeoAlbumEntries)
	if !ok {
		t.Fatalf("sequence type = %T", result.Entries)
	}
	sequence.provider.now = func() time.Time { return base.Add(9 * time.Minute) }
	for iteration := 0; iteration < 2; iteration++ {
		entries, collectErr := CollectEntries(context.Background(), result.Entries, 10)
		if collectErr != nil || len(entries) != 2 {
			t.Fatalf("iteration %d entries=%#v error=%v", iteration, entries, collectErr)
		}
	}
	if calls := transport.countPath("/_next/viewer"); calls != 2 {
		t.Fatalf("viewer calls = %d, want initial plus refresh", calls)
	}
}

func TestVimeoAlbumRefreshesJWTOnceAfterAuthorizationRejection(t *testing.T) {
	transport := newVimeoAlbumFixtureTransport(t)
	base := time.Now().Truncate(time.Second)
	firstToken := vimeoAlbumTestJWT(base.Add(time.Hour).Unix(), "first-auth")
	secondToken := vimeoAlbumTestJWT(base.Add(2*time.Hour).Unix(), "second-auth")
	transport.viewers = [][]byte{
		[]byte(`{"jwt":"` + firstToken + `"}`),
		[]byte(`{"jwt":"` + secondToken + `"}`),
	}
	provider := &vimeoAlbumTokenProvider{transport: transport, now: func() time.Time { return base }}
	attempts := 0
	err := withVimeoAlbumToken(context.Background(), provider, func(token string) error {
		attempts++
		switch attempts {
		case 1:
			if token != firstToken {
				t.Fatalf("first token = %q", token)
			}
			return &HTTPStatusError{Code: http.StatusUnauthorized}
		case 2:
			if token != secondToken {
				t.Fatalf("second token = %q", token)
			}
			return nil
		default:
			t.Fatalf("unexpected attempt %d", attempts)
			return nil
		}
	})
	if err != nil || attempts != 2 || transport.countPath("/_next/viewer") != 2 {
		t.Fatalf("error=%v attempts=%d viewer=%d", err, attempts, transport.countPath("/_next/viewer"))
	}
}

func vimeoAlbumTestJWT(exp int64, signature string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString([]byte(signature))
}

func TestVimeoAlbumFailuresCapabilityAndCancellation(t *testing.T) {
	t.Run("missing capability", func(t *testing.T) {
		transport := &vimeoFixtureTransport{}
		_, err := NewVimeo().Extract(context.Background(), Request{
			URL: "https://vimeo.com/album/7", Transport: transport,
		})
		if !errors.Is(err, ErrTransportIsolation) || transport.pageReads != 0 || transport.configGets != 0 {
			t.Fatalf("error=%v page=%d config=%d", err, transport.pageReads, transport.configGets)
		}
	})
	t.Run("malformed viewer", func(t *testing.T) {
		transport := newVimeoAlbumFixtureTransport(t)
		transport.viewer = []byte(`{"jwt":"secret"}`)
		_, err := NewVimeo().Extract(context.Background(), Request{
			URL: "https://vimeo.com/album/7", Transport: transport,
		})
		if !errors.Is(err, ErrInvalidMetadata) || strings.Contains(fmt.Sprint(err), "secret") {
			t.Fatalf("error = %v", err)
		}
	})
	for _, privacy := range []string{"password", "nobody", "unlisted"} {
		t.Run("privacy "+privacy, func(t *testing.T) {
			transport := newVimeoAlbumFixtureTransport(t)
			transport.metadata = []byte(`{"name":"Private","privacy":{"view":"` + privacy + `"}}`)
			_, err := NewVimeo().Extract(context.Background(), Request{
				URL: "https://vimeo.com/album/7", Transport: transport,
			})
			if !errors.Is(err, ErrAuthentication) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	t.Run("first page 400", func(t *testing.T) {
		transport := newVimeoAlbumFixtureTransport(t)
		transport.status["page:1"] = http.StatusBadRequest
		result, err := NewVimeo().Extract(context.Background(), Request{
			URL: "https://vimeo.com/album/7", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrVimeoPlaylistNetwork) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("all invalid", func(t *testing.T) {
		transport := newVimeoAlbumFixtureTransport(t)
		transport.pages[1] = []byte(`{"data":[{"link":"https://evil.example/1","uri":"/videos/1"}]}`)
		result, err := NewVimeo().Extract(context.Background(), Request{
			URL: "https://vimeo.com/album/7", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("page overflow", func(t *testing.T) {
		transport := newVimeoAlbumFixtureTransport(t)
		page := vimeoAlbumVideoPage{Data: make([]struct {
			Link string `json:"link"`
			URI  string `json:"uri"`
		}, vimeoAlbumPageSize+1)}
		transport.pages[1], _ = json.Marshal(page)
		result, err := NewVimeo().Extract(context.Background(), Request{
			URL: "https://vimeo.com/album/7", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		transport := newVimeoAlbumFixtureTransport(t)
		transport.blockPage = 1
		result, err := NewVimeo().Extract(context.Background(), Request{
			URL: "https://vimeo.com/album/7", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, _, nextErr := result.Entries.Iterator().Next(ctx)
			done <- nextErr
		}()
		select {
		case <-transport.pageStarted:
			cancel()
		case <-time.After(time.Second):
			t.Fatal("page request did not start")
		}
		select {
		case nextErr := <-done:
			if !errors.Is(nextErr, context.Canceled) {
				t.Fatalf("error = %v", nextErr)
			}
		case <-time.After(time.Second):
			t.Fatal("page request did not cancel")
		}
	})
}

func FuzzClassifyVimeoAlbumURL(f *testing.F) {
	for _, seed := range []string{
		"https://vimeo.com/album/7",
		"https://vimeo.com/showcase/7",
		"https://vimeo.com/album/%37",
		"https://evil.example/album/7",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 1<<20 {
			t.Skip()
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		target, ok := classifyVimeoAlbumURL(parsed)
		if !ok {
			return
		}
		if target.kind != vimeoRouteAlbum || !validVimeoNumericVideoID(target.id) ||
			target.canonical != "https://vimeo.com/"+target.baseURL+"/"+target.id {
			t.Fatalf("unsafe target: %#v", target)
		}
		canonical, err := url.Parse(target.canonical)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, ok := classifyVimeoAlbumURL(canonical)
		if !ok || roundTrip != target {
			t.Fatalf("round trip = %#v/%v, want %#v", roundTrip, ok, target)
		}
		kind, routed := classifyVimeoURL(canonical)
		if kind != vimeoRouteAlbum || routed != target {
			t.Fatalf("top-level route = %v %#v", kind, routed)
		}
	})
}

func FuzzVimeoAlbumVideoEntry(f *testing.F) {
	f.Add("https://vimeo.com/123456789", "/videos/123456789")
	f.Add("https://evil.example/123456789", "/videos/123456789")
	f.Add("https://vimeo.com/123456789?token=secret", "/videos/123456789")
	f.Add("https://vimeo.com/123456789", "/videos/987654321")
	f.Fuzz(func(t *testing.T, link, uri string) {
		if len(link) > 1<<20 || len(uri) > 1<<20 {
			t.Skip()
		}
		entry, ok := vimeoAlbumVideoEntry(link, uri)
		if !ok {
			return
		}
		if !validVimeoNumericVideoID(entry.ID) || entry.URL != "https://vimeo.com/"+entry.ID ||
			entry.ExtractorKey != "vimeo" || !entry.Transparent {
			t.Fatalf("unsafe entry: %#v", entry)
		}
		parsed, err := url.Parse(entry.URL)
		if err != nil {
			t.Fatal(err)
		}
		kind, target := classifyVimeoURL(parsed)
		if kind != vimeoRouteVideo || target.id != entry.ID {
			t.Fatalf("entry dispatch = %v %#v", kind, target)
		}
	})
}
