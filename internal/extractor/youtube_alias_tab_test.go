package extractor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type aliasTabFixtureTransport struct {
	mu           sync.Mutex
	page         []byte
	continuation []byte
	expectedPage string
	readErr      error
	doErr        error
	status       int
	reads        int
	requests     int
	bodies       []string
	lastRequest  *http.Request
}

func (transport *aliasTabFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.reads++
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if rawURL != transport.expectedPage {
		return nil, nil, errors.New("unexpected canonical page URL: " + rawURL)
	}
	if transport.readErr != nil {
		return nil, nil, transport.readErr
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *aliasTabFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.requests++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if transport.doErr != nil {
		return nil, transport.doErr
	}
	if request.Method != http.MethodPost || request.URL.Path != "/youtubei/v1/browse" {
		return nil, errors.New("unexpected continuation request")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.bodies = append(transport.bodies, string(body))
	transport.lastRequest = request.Clone(request.Context())
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(transport.continuation))),
	}, nil
}

func readAliasTabFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube_alias_tab/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func collectAliasEntries(t *testing.T, result Extraction) []string {
	t.Helper()
	var entries []string
	iterator := result.Entries.Iterator()
	for {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return entries
		}
		entries = append(entries, entry.ID+":"+entry.Title+":"+entry.URL)
	}
}

func TestYouTubeAliasTabUserVideosCanonicalContinuationAndUCID(t *testing.T) {
	const source = "http://youtube.com/user/MixedCase/videos?view=0&sort=dd"
	const canonical = "https://www.youtube.com/user/MixedCase/videos"
	transport := &aliasTabFixtureTransport{
		expectedPage: canonical,
		page:         readAliasTabFixture(t, "user-videos.html"),
		continuation: readAliasTabFixture(t, "user-videos-continuation.json"),
	}
	result, err := NewYouTubeAliasTab().Extract(context.Background(), Request{URL: source, Transport: transport})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("playlist=%v err=%v", result.IsPlaylist(), err)
	}
	id, _ := result.Info.ID()
	title, _ := result.Info.Title()
	webpage, _ := result.Info.WebpageURL()
	if id != "UCabcdefghijklmnopqrstuv" || title != "Synthetic Legacy User" || webpage != canonical {
		t.Fatalf("info id=%q title=%q webpage=%q", id, title, webpage)
	}
	if transport.reads != 1 || transport.requests != 0 {
		t.Fatalf("eager requests reads=%d continuation=%d", transport.reads, transport.requests)
	}
	want := []string{
		"abcdefghijk:First legacy video:https://www.youtube.com/watch?v=abcdefghijk",
		"lmnopqrstuv:Legacy short:https://www.youtube.com/watch?v=lmnopqrstuv",
		"wxyzABCDE12:Continued legacy video:https://www.youtube.com/watch?v=wxyzABCDE12",
	}
	if got := collectAliasEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries=%#v", got)
	}
	if transport.requests != 1 {
		t.Fatalf("repeated continuation token was not terminated: %d requests", transport.requests)
	}
	if transport.lastRequest.URL.Query().Get("key") != "synthetic-key" ||
		transport.lastRequest.Header.Get("X-Youtube-Client-Version") != "2.synthetic" ||
		!strings.Contains(transport.bodies[0], `"continuation":"alias-next-1"`) ||
		!strings.Contains(transport.bodies[0], `"visitorData":"initial-visitor"`) {
		t.Fatalf("continuation url=%s headers=%v body=%s", transport.lastRequest.URL, transport.lastRequest.Header, transport.bodies[0])
	}
}

func TestYouTubeAliasTabUnicodeCPlaylistsFallbackAndRenderers(t *testing.T) {
	const alias = "日本語Alias"
	const canonical = "https://www.youtube.com/c/%E6%97%A5%E6%9C%AC%E8%AA%9EAlias/playlists"
	transport := &aliasTabFixtureTransport{
		expectedPage: canonical,
		page:         readAliasTabFixture(t, "c-unicode-playlists.html"),
		continuation: readAliasTabFixture(t, "c-unicode-playlists-continuation.json"),
	}
	result, err := NewYouTubeAliasTab().Extract(context.Background(), Request{
		URL: "https://www.youtube.com/c/" + alias + "/playlists?flow=grid", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.Info.ID()
	if id != "c:"+alias {
		t.Fatalf("fallback id=%q", id)
	}
	want := []string{
		"PLlegacyAlias001:Legacy alias playlist:https://www.youtube.com/playlist?list=PLlegacyAlias001",
		"PLmodernAlias002:Modern alias playlist:https://www.youtube.com/playlist?list=PLmodernAlias002",
		"PLcontinuedAlias003:Continued alias playlist:https://www.youtube.com/playlist?list=PLcontinuedAlias003",
	}
	if got := collectAliasEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries=%#v", got)
	}
}

func TestYouTubeAliasTabTargetPolicy(t *testing.T) {
	valid := []struct {
		raw, kind, alias, tab string
	}{
		{"https://youtube.com/user/MixedCase/videos", "user", "MixedCase", "videos"},
		{"https://youtube.com/user/MixedCase", "user", "MixedCase", ""},
		{"https://youtube.com/c/日本語", "c", "日本語", ""},
		{"http://www.youtube.com/c/日本語/shorts?x=1", "c", "日本語", "shorts"},
		{"https://www.youtube.com/user/a.b-c_d/streams", "user", "a.b-c_d", "streams"},
		{"https://youtube.com/c/A/playlists", "c", "A", "playlists"},
		{"https://youtube.com/user/%252f/videos", "user", "%2f", "videos"},
		{"https://youtube.com/user/%255c/videos", "user", "%5c", "videos"},
		{"https://youtube.com/user/%2500/videos", "user", "%00", "videos"},
		{"https://youtube.com/user/%252e/videos", "user", "%2e", "videos"},
		{"https://youtube.com/c/%E6%97%A5%E6%9C%AC/playlists", "c", "日本", "playlists"},
		{"https://youtube.com/user/100%25Real/videos", "user", "100%Real", "videos"},
		{"https://youtube.com/user/name/home", "user", "name", "home"},
		{"https://youtube.com/c/name/featured", "c", "name", "featured"},
		{"https://youtube.com/user/name/community", "user", "name", "community"},
		{"https://youtube.com/c/name/releases", "c", "name", "releases"},
		{"https://youtube.com/user/name/podcasts", "user", "name", "podcasts"},
	}
	for _, test := range valid {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		kind, alias, tab, ok := youtubeAliasTabTarget(parsed)
		if !ok || kind != test.kind || alias != test.alias || tab != test.tab || !NewYouTubeAliasTab().Suitable(parsed) {
			t.Fatalf("%q => %q %q %q %v", test.raw, kind, alias, tab, ok)
		}
	}
	invalid := []string{
		"https://m.youtube.com/user/name/videos",
		"https://youtube.com./user/name/videos",
		"//youtube.com/user/name/videos",
		"https://youtube.com.evil/user/name/videos",
		"https://user@youtube.com/user/name/videos",
		"https://youtube.com:443/user/name/videos",
		"ftp://youtube.com/user/name/videos",
		"https://youtube.com/user/name/videos#fragment",
		"https://youtube.com/user/name/videos/",
		"https://youtube.com/user/name/videos/extra",
		"https://youtube.com/User/name/videos",
		"https://youtube.com/user//videos",
		"https://youtube.com/user/./videos",
		"https://youtube.com/user/../videos",
		"https://youtube.com/user/name/membership",
		"https://youtube.com/user/name/videos?next=%2fwatch",
		"https://youtube.com/user/name/videos?next=%5cwatch",
		"https://youtube.com/user/name/videos?next=%00",
		"https://youtube.com/user/name%2fother/videos",
		"https://youtube.com/c/name%5cother/videos",
		"https://youtube.com/channel/name/videos",
	}
	for _, raw := range invalid {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if _, _, _, ok := youtubeAliasTabTarget(parsed); ok || NewYouTubeAliasTab().Suitable(parsed) {
			t.Fatalf("accepted %q parsed=%#v", raw, parsed)
		}
	}
	invalidUTF8 := &url.URL{Scheme: "https", Host: "youtube.com", Path: "/user/" + string([]byte{0xff}) + "/videos"}
	if utf8.ValidString(invalidUTF8.Path) || NewYouTubeAliasTab().Suitable(invalidUTF8) {
		t.Fatal("invalid UTF-8 alias accepted")
	}
	control := &url.URL{Scheme: "https", Host: "youtube.com", Path: "/user/a\nb/videos"}
	if NewYouTubeAliasTab().Suitable(control) {
		t.Fatal("control alias accepted")
	}
	oversize := &url.URL{Scheme: "https", Host: "youtube.com", Path: "/c/" + strings.Repeat("a", youtubeAliasMaxBytes+1) + "/videos"}
	if NewYouTubeAliasTab().Suitable(oversize) {
		t.Fatal("oversize alias accepted")
	}
}

func TestYouTubeAliasTabCanonicalURLPercentSafety(t *testing.T) {
	for _, test := range []struct {
		alias string
		want  string
	}{
		{"%2f", "https://www.youtube.com/user/%252f/videos"},
		{"%5c", "https://www.youtube.com/user/%255c/videos"},
		{"%00", "https://www.youtube.com/user/%2500/videos"},
		{"%2e", "https://www.youtube.com/user/%252e/videos"},
		{"100%Real", "https://www.youtube.com/user/100%25Real/videos"},
		{"日本", "https://www.youtube.com/user/%E6%97%A5%E6%9C%AC/videos"},
	} {
		if got := youtubeAliasTabCanonicalURL("user", test.alias, "videos"); got != test.want {
			t.Fatalf("alias=%q canonical=%q want=%q", test.alias, got, test.want)
		}
		parsed, err := url.Parse(test.want)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(parsed.Path, "/")
		if len(parts) != 4 || parts[2] != test.alias {
			t.Fatalf("canonical changed semantic alias %q: path=%q", test.alias, parsed.Path)
		}
	}
}

func TestYouTubeAliasTabPercentAliasFetchIsEncodedOnce(t *testing.T) {
	const source = "https://youtube.com/user/%252f/videos?ignored=1"
	const canonical = "https://www.youtube.com/user/%252f/videos"
	page := []byte(`<script>ytInitialData={
		"metadata":{"channelMetadataRenderer":{"title":"Literal Percent Alias"}},
		"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
			{"tabRenderer":{"selected":true,"title":"Videos","content":{"richGridRenderer":{"contents":[]}}}}
		]}}
	};</script>`)
	transport := &aliasTabFixtureTransport{expectedPage: canonical, page: page}
	result, err := NewYouTubeAliasTab().Extract(context.Background(), Request{URL: source, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.Info.ID()
	webpage, _ := result.Info.WebpageURL()
	if id != "user:%2f" || webpage != canonical || transport.reads != 1 {
		t.Fatalf("id=%q webpage=%q reads=%d", id, webpage, transport.reads)
	}
}

func TestYouTubeAliasTabSelectedIdentityAndMetadataFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want error
	}{
		{"mismatch", `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"tabIdentifier":"FEshorts"}}]}}}`, ErrInvalidPlaylist},
		{"missing selected", `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":false,"tabIdentifier":"FEvideos"}}]}}}`, ErrInvalidMetadata},
		{"multiple selected", `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"tabIdentifier":"FEvideos"}},{"tabRenderer":{"selected":true,"tabIdentifier":"FEvideos"}}]}}}`, ErrInvalidMetadata},
		{"unknown selected identity", `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{}}}]}}}`, ErrInvalidMetadata},
		{"mismatch endpoint", `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/user/name/shorts"}}}}}]}}}`, ErrInvalidPlaylist},
		{"conflicting identity", `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"tabIdentifier":"FEvideos","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/user/name/shorts"}}}}}]}}}`, ErrInvalidMetadata},
		{"malformed", `{`, ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateYouTubeSelectedTab([]byte(test.data), "videos"); !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
	if err := validateYouTubeSelectedTab([]byte(`{
		"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
			{"tabRenderer":{"selected":true,"title":"Videos","content":{}}}
		]}}
	}`), "videos"); err != nil {
		t.Fatalf("recognized English title-only identity: %v", err)
	}
	// A continuation-like response has no tabs and must not be rejected.
	if err := validateYouTubeSelectedTab([]byte(`{"onResponseReceivedActions":[]}`), "videos"); err != nil {
		t.Fatalf("continuation-like response: %v", err)
	}
	const canonical = "https://www.youtube.com/user/name/videos"
	for _, page := range []string{
		`<script>ytInitialData={"contents":{"videoRenderer":{"videoId":"abcdefghijk"}}};</script>`,
		`<script>ytInitialData=[];</script>`,
	} {
		_, err := NewYouTubeAliasTab().Extract(context.Background(), Request{
			URL: canonical, Transport: &aliasTabFixtureTransport{expectedPage: canonical, page: []byte(page)},
		})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("page=%s err=%v", page, err)
		}
	}
}

func TestYouTubeAliasTabTraversalBounds(t *testing.T) {
	nested := `{"videoRenderer":{"videoId":"abcdefghijk"}}`
	for depth := 0; depth < youtubeMaxJSONDepth+2; depth++ {
		nested = `{"x":` + nested + `}`
	}
	data := `{"metadata":{"channelMetadataRenderer":{"title":"Bounded"}},"contents":` + nested + `}`
	if _, err := parseYouTubeHandleTabData([]byte(data), "videos"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("deep traversal err=%v", err)
	}
}

func TestYouTubeAliasTabCategorizedFailuresAndCancellation(t *testing.T) {
	const canonical = "https://www.youtube.com/user/name/videos"
	for _, test := range []struct {
		err  error
		want error
	}{
		{&HTTPStatusError{Code: http.StatusUnauthorized}, ErrAuthentication},
		{&HTTPStatusError{Code: http.StatusForbidden}, ErrAuthentication},
		{&HTTPStatusError{Code: http.StatusNotFound}, ErrUnavailable},
		{&HTTPStatusError{Code: http.StatusTooManyRequests}, ErrYouTubeAliasTabRateLimited},
		{errors.New("dial failed"), ErrYouTubeAliasTabNetwork},
	} {
		_, err := NewYouTubeAliasTab().Extract(context.Background(), Request{
			URL: canonical, Transport: &aliasTabFixtureTransport{expectedPage: canonical, readErr: test.err},
		})
		if !errors.Is(err, test.want) {
			t.Fatalf("input=%v err=%v want=%v", test.err, err, test.want)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewYouTubeAliasTab().Extract(cancelled, Request{
		URL: canonical, Transport: &aliasTabFixtureTransport{expectedPage: canonical},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestYouTubeAliasTabContinuationRateLimitAndReusableRace(t *testing.T) {
	const canonical = "https://www.youtube.com/user/MixedCase/videos"
	rateLimited := &aliasTabFixtureTransport{
		expectedPage: canonical,
		page:         readAliasTabFixture(t, "user-videos.html"),
		status:       http.StatusTooManyRequests,
	}
	result, err := NewYouTubeAliasTab().Extract(context.Background(), Request{URL: canonical, Transport: rateLimited})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	for index := 0; index < 2; index++ {
		if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
			t.Fatalf("initial entry %d ok=%v err=%v", index, ok, nextErr)
		}
	}
	if _, _, nextErr := iterator.Next(context.Background()); !errors.Is(nextErr, ErrYouTubeAliasTabRateLimited) {
		t.Fatalf("continuation error=%v", nextErr)
	}
	network := &aliasTabFixtureTransport{
		expectedPage: canonical,
		page:         readAliasTabFixture(t, "user-videos.html"),
		doErr:        errors.New("connection reset"),
	}
	networkResult, err := NewYouTubeAliasTab().Extract(context.Background(), Request{URL: canonical, Transport: network})
	if err != nil {
		t.Fatal(err)
	}
	networkIterator := networkResult.Entries.Iterator()
	for index := 0; index < 2; index++ {
		if _, ok, nextErr := networkIterator.Next(context.Background()); nextErr != nil || !ok {
			t.Fatalf("network initial entry %d ok=%v err=%v", index, ok, nextErr)
		}
	}
	if _, _, nextErr := networkIterator.Next(context.Background()); !errors.Is(nextErr, ErrYouTubeAliasTabNetwork) {
		t.Fatalf("continuation network error=%v", nextErr)
	}

	reusable := &aliasTabFixtureTransport{
		expectedPage: canonical,
		page:         readAliasTabFixture(t, "user-videos.html"),
		continuation: readAliasTabFixture(t, "user-videos-continuation.json"),
	}
	result, err = NewYouTubeAliasTab().Extract(context.Background(), Request{URL: canonical, Transport: reusable})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			iterator := result.Entries.Iterator()
			count := 0
			for {
				_, ok, nextErr := iterator.Next(context.Background())
				if nextErr != nil {
					errs <- nextErr
					return
				}
				if !ok {
					if count != 3 {
						errs <- errors.New("unexpected entry count")
					}
					return
				}
				count++
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if reusable.requests != 8 {
		t.Fatalf("continuation requests=%d", reusable.requests)
	}
}

func FuzzYouTubeAliasTabTarget(f *testing.F) {
	for _, seed := range []string{
		"https://youtube.com/user/MixedCase/videos",
		"https://youtube.com/user/MixedCase",
		"https://www.youtube.com/c/日本語/playlists?x=1",
		"https://youtube.com/user/a%2fb/videos",
		"https://evil.example/c/name/streams",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 8192 {
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		kind, alias, tab, ok := youtubeAliasTabTarget(parsed)
		if ok {
			if kind != "user" && kind != "c" {
				t.Fatalf("kind=%q", kind)
			}
			if alias == "" || len(alias) > youtubeAliasMaxBytes || !utf8.ValidString(alias) {
				t.Fatalf("alias=%q", alias)
			}
			if tab != "" && youtubePublicTabType(tab) == youtubeTabUnsupported {
				t.Fatalf("tab=%q", tab)
			}
			if parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" ||
				(parsed.RawPath != "" && parsed.RawPath != parsed.Path) {
				t.Fatalf("unsafe parsed URL=%#v", parsed)
			}
		}
		if NewYouTubeAliasTab().Suitable(parsed) != ok {
			t.Fatal("Suitable and target disagree")
		}
	})
}
