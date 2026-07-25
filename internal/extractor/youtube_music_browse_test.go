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

type musicBrowseTransport struct {
	page, continuation []byte
	resolve, browse    []byte
	readErr            error
	status, calls      int
	body, raw, method  string
	path, query        string
	headers            http.Header
	isolated           bool
	sawCookieHeader    bool
}

func readYouTubeMusicBrowseFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "youtube_music_browse", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (m *musicBrowseTransport) ReadPage(_ context.Context, raw string) ([]byte, http.Header, error) {
	m.raw = raw
	if m.readErr != nil {
		return nil, nil, m.readErr
	}
	return m.page, make(http.Header), nil
}

func (m *musicBrowseTransport) Do(_ context.Context, r *http.Request) (*http.Response, error) {
	return m.do(r, false)
}

func (m *musicBrowseTransport) DoWithoutCookies(_ context.Context, r *http.Request) (*http.Response, error) {
	if r.Header.Get("Cookie") != "" {
		m.sawCookieHeader = true
		return nil, errors.New("cookie-isolated Music browse request contains Cookie header")
	}
	return m.do(r, true)
}

func (m *musicBrowseTransport) do(r *http.Request, isolated bool) (*http.Response, error) {
	m.calls++
	m.isolated = isolated
	m.method, m.path, m.query = r.Method, r.URL.Path, r.URL.RawQuery
	m.headers = r.Header.Clone()
	b, _ := io.ReadAll(r.Body)
	m.body = string(b)
	payload := m.continuation
	switch {
	case strings.Contains(r.URL.Path, "resolve_url") && len(m.resolve) > 0:
		payload = m.resolve
	case strings.Contains(r.URL.Path, "/browse") && len(m.browse) > 0 && !strings.Contains(m.body, `"continuation"`):
		payload = m.browse
	}
	s := m.status
	if s == 0 {
		s = http.StatusOK
	}
	return &http.Response{StatusCode: s, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload))}, nil
}

func TestYouTubeMusicBrowseAlbumPagingAndIsolation(t *testing.T) {
	m := &musicBrowseTransport{
		page:         readYouTubeMusicBrowseFixture(t, "album_initial.html"),
		continuation: readYouTubeMusicBrowseFixture(t, "continuation.json"),
	}
	out, err := NewYouTubeMusicBrowse().Extract(context.Background(), Request{
		URL: "https://music.youtube.com/browse/MPREbfixture0001", Transport: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	infoID, _ := out.Info.Lookup("id").StringValue()
	title, _ := out.Info.Lookup("title").StringValue()
	family, _ := out.Info.Lookup("music_browse_family").StringValue()
	if infoID != "OLAK5uy_fixtureAlbum01" || title != "Fixture Album" || family != "album" {
		t.Fatalf("info id=%q title=%q family=%q", infoID, title, family)
	}
	got, err := CollectEntries(context.Background(), out.Entries, 4)
	if err != nil || len(got) != 3 || got[0].ID != "aaaaaaaaaaa" || got[1].ID != "bbbbbbbbbbb" || got[2].ID != "ccccccccccc" {
		t.Fatalf("entries=%#v err=%v", got, err)
	}
	if m.calls != 1 || !m.isolated || m.method != http.MethodPost || m.path != "/youtubei/v1/browse" ||
		!strings.Contains(m.query, "key=fixture-key") ||
		!strings.Contains(m.body, `"clientName":"WEB_REMIX"`) ||
		!strings.Contains(m.body, `"clientVersion":"fixture-music-version"`) ||
		!strings.Contains(m.body, `"visitorData":"fixture-visitor"`) ||
		m.headers.Get("Origin") != "https://music.youtube.com" ||
		m.headers.Get("X-Youtube-Client-Name") != "67" ||
		m.sawCookieHeader {
		t.Fatalf("method=%s path=%s query=%s calls=%d isolated=%v body=%s headers=%v cookie=%v",
			m.method, m.path, m.query, m.calls, m.isolated, m.body, m.headers, m.sawCookieHeader)
	}
	if !strings.Contains(m.body, "WEB") || strings.Contains(m.body, `"clientName":"WEB"`) {
		t.Fatalf("WEB client leaked into Music browse body %s", m.body)
	}
}

func TestYouTubeMusicBrowseFamiliesLazyReusableAndCancellation(t *testing.T) {
	cases := []struct {
		file, raw, id, title, family string
	}{
		{"artist_initial.html", "https://music.youtube.com/browse/UCabcdefghijklmnopqrstuv", "UCabcdefghijklmnopqrstuv", "Fixture Artist", "artist"},
		{"playlist_initial.html", "https://music.youtube.com/browse/VLPLFixturePlay01", "PLFixturePlay01", "Fixture Playlist", "playlist"},
		{"podcast_initial.html", "https://music.youtube.com/browse/MPSPPLFixturePod01", "PLFixturePod01", "Fixture Podcast", "podcast"},
	}
	for _, test := range cases {
		m := &musicBrowseTransport{page: readYouTubeMusicBrowseFixture(t, test.file)}
		out, err := NewYouTubeMusicBrowse().Extract(context.Background(), Request{URL: test.raw, Transport: m})
		if err != nil {
			t.Fatalf("%s: %v", test.family, err)
		}
		infoID, _ := out.Info.Lookup("id").StringValue()
		title, _ := out.Info.Lookup("title").StringValue()
		family, _ := out.Info.Lookup("music_browse_family").StringValue()
		if infoID != test.id || title != test.title || family != test.family {
			t.Fatalf("%s info id=%q title=%q family=%q", test.family, infoID, title, family)
		}
		first, ok, err := out.Entries.Iterator().Next(context.Background())
		if err != nil || !ok || first.ExtractorKey != "youtube" {
			t.Fatalf("%s first=%#v ok=%v err=%v", test.family, first, ok, err)
		}
		for i := 0; i < 2; i++ {
			got, err := CollectEntries(context.Background(), out.Entries, 4)
			if err != nil || len(got) != 1 {
				t.Fatalf("%s iteration=%d entries=%v err=%v", test.family, i, got, err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, ok, err = out.Entries.Iterator().Next(ctx)
		if ok || !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancel ok=%v err=%v", test.family, ok, err)
		}
	}
}

func TestYouTubeMusicBrowseTargetPolicy(t *testing.T) {
	good := []string{
		"https://music.youtube.com/browse/MPREbfixture0001",
		"http://music.youtube.com/browse/MPSPpodcast00001",
		"https://music.youtube.com/browse/VLPLFixtureAlbum1",
		"https://music.youtube.com/browse/UCabcdefghijklmnopqrstuv",
		"https://music.youtube.com/browse/MPLAUCabcdefghijklmnopqrstuv",
	}
	bad := []string{
		"https://www.music.youtube.com/browse/MPREbfixture0001",
		"https://music.youtube.com:443/browse/MPREbfixture0001",
		"https://u@music.youtube.com/browse/MPREbfixture0001",
		"https://music.youtube.com/browse/MPADnotregistered1",
		"https://music.youtube.com/browse/RDAMVMaaaaaaaaaaa",
		"https://music.youtube.com/browse/MPREbfixture0001/extra",
		"https://music.youtube.com/browse/MPREbfixture0001?x=1",
		"https://www.youtube.com/browse/MPREbfixture0001",
		"https://music.youtube.com/browse/MPREbfixture%2f0001",
	}
	for _, raw := range good {
		u, _ := url.Parse(raw)
		if !NewYouTubeMusicBrowse().Suitable(u) {
			t.Error(raw)
		}
	}
	for _, raw := range bad {
		u, _ := url.Parse(raw)
		if NewYouTubeMusicBrowse().Suitable(u) {
			t.Error(raw)
		}
	}
}

func TestYouTubeMusicBrowseErrorsAlertsAndAuthFailClosed(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{401, ErrAuthentication}, {404, ErrUnavailable}, {429, ErrYouTubeMusicBrowseRateLimited}, {500, ErrYouTubeMusicBrowseNetwork}} {
		m := &musicBrowseTransport{
			page:         readYouTubeMusicBrowseFixture(t, "album_initial.html"),
			continuation: readYouTubeMusicBrowseFixture(t, "continuation.json"),
			status:       test.status,
		}
		out, err := NewYouTubeMusicBrowse().Extract(context.Background(), Request{
			URL: "https://music.youtube.com/browse/MPREbfixture0001", Transport: m,
		})
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
	}{
		{`ytcfg.set({"LOGGED_IN":true,"INNERTUBE_CONTEXT":{"client":{"clientName":"WEB_REMIX"}}});ytInitialData={"contents":{}};`, ErrAuthentication},
		{`ytcfg.set({"LOGGED_IN":false,"INNERTUBE_CONTEXT":{"client":{"clientName":"WEB"}}});ytInitialData={"contents":{}};`, ErrAuthentication},
		{`ytcfg.set({"LOGGED_IN":false,"INNERTUBE_CONTEXT":{"client":{"clientName":"WEB_REMIX"}}});ytInitialData={"alerts":[{"alertRenderer":{"text":{"simpleText":"Sign in to confirm"}}}]};`, ErrAuthentication},
		{`ytcfg.set({"LOGGED_IN":false,"INNERTUBE_CONTEXT":{"client":{"clientName":"WEB_REMIX"}}});ytInitialData={"alerts":[{"alertRenderer":{"text":{"simpleText":"Premium only"}}}]};`, ErrAuthentication},
		{`ytcfg.set({"LOGGED_IN":false,"INNERTUBE_CONTEXT":{"client":{"clientName":"WEB_REMIX"}}});ytInitialData={broken};`, ErrInvalidMetadata},
	} {
		m := &musicBrowseTransport{page: []byte(test.page)}
		_, err := NewYouTubeMusicBrowse().Extract(context.Background(), Request{
			URL: "https://music.youtube.com/browse/MPREbfixture0001", Transport: m,
		})
		if !errors.Is(err, test.want) {
			t.Errorf("page err=%v want=%v", err, test.want)
		}
	}
	m := &musicBrowseTransport{readErr: errors.New("offline")}
	_, err := NewYouTubeMusicBrowse().Extract(context.Background(), Request{
		URL: "https://music.youtube.com/browse/MPREbfixture0001", Transport: m,
	})
	if !errors.Is(err, ErrYouTubeMusicBrowseNetwork) {
		t.Fatal(err)
	}
	// Continuations require cookie isolation; jar-capable Do alone is insufficient.
	out, err := NewYouTubeMusicBrowse().Extract(context.Background(), Request{
		URL: "https://music.youtube.com/browse/MPREbfixture0001",
		Transport: struct {
			Transport
		}{Transport: &musicBrowseTransport{
			page:         readYouTubeMusicBrowseFixture(t, "album_initial.html"),
			continuation: readYouTubeMusicBrowseFixture(t, "continuation.json"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), out.Entries, 4)
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeMusicBrowseAlbumResolveFallback(t *testing.T) {
	page := []byte(`ytcfg.set({"INNERTUBE_API_KEY":"fixture-key","INNERTUBE_CLIENT_VERSION":"fixture-music-version","VISITOR_DATA":"fixture-visitor","LOGGED_IN":false,"INNERTUBE_CONTEXT":{"client":{"clientName":"WEB_REMIX"}}});ytInitialData={"contents":{"sectionListRenderer":{"contents":[]}}};`)
	resolve := []byte(`{"endpoint":{"browseEndpoint":{"browseId":"MPREbfixture0001"}}}`)
	browse := []byte(`{"microformat":{"microformatDataRenderer":{"urlCanonical":"https://music.youtube.com/playlist?list=OLAK5uy_resolvedAlbum1","title":"Resolved Album"}},"contents":{"sectionListRenderer":{"contents":[{"musicShelfRenderer":{"contents":[{"musicResponsiveListItemRenderer":{"playlistItemData":{"videoId":"aaaaaaaaaaa"},"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"simpleText":"resolved"}}}]}}]}}]}}}`)
	m := &musicBrowseTransport{page: page, resolve: resolve, browse: browse}
	out, err := NewYouTubeMusicBrowse().Extract(context.Background(), Request{
		URL: "https://music.youtube.com/browse/MPREbfixture0001", Transport: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	infoID, _ := out.Info.Lookup("id").StringValue()
	title, _ := out.Info.Lookup("title").StringValue()
	if infoID != "OLAK5uy_resolvedAlbum1" || title != "Resolved Album" {
		t.Fatalf("id=%q title=%q", infoID, title)
	}
	got, err := CollectEntries(context.Background(), out.Entries, 2)
	if err != nil || len(got) != 1 || got[0].ID != "aaaaaaaaaaa" {
		t.Fatalf("entries=%#v err=%v", got, err)
	}
	if m.calls < 2 || !m.isolated || !strings.Contains(m.body, `"clientName":"WEB_REMIX"`) {
		t.Fatalf("calls=%d isolated=%v body=%s", m.calls, m.isolated, m.body)
	}
}

func FuzzParseYouTubeMusicBrowseData(f *testing.F) {
	f.Add([]byte(`{"contents":{}}`))
	f.Add([]byte(`{"continuationContents":{"musicPlaylistShelfContinuation":{"contents":[{"musicResponsiveListItemRenderer":{"playlistItemData":{"videoId":"aaaaaaaaaaa"}}}]}}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		p, e := parseYouTubeMusicBrowseData(b)
		if e != nil {
			return
		}
		if p.continuation != "" && validYouTubeContinuationToken(p.continuation) == "" {
			t.Fatalf("unsafe continuation %q", p.continuation)
		}
		for _, x := range p.entries {
			if x.URL == "" || strings.ContainsAny(x.URL, "\x00\r\n") {
				t.Fatalf("unsafe entry %#v", x)
			}
			if x.ExtractorKey == "youtube" && (!youtubeIDPattern.MatchString(x.ID) || !strings.HasPrefix(x.URL, "https://www.youtube.com/")) {
				t.Fatalf("unsafe video entry %#v", x)
			}
		}
	})
}

func FuzzYouTubeMusicBrowseTarget(f *testing.F) {
	f.Add("https://music.youtube.com/browse/MPREbfixture0001")
	f.Add("https://music.youtube.com/browse/UCabcdefghijklmnopqrstuv")
	f.Fuzz(func(t *testing.T, raw string) {
		u, e := url.Parse(raw)
		if e != nil {
			return
		}
		id, family, c, ok := youtubeMusicBrowseTarget(u)
		if !ok {
			return
		}
		cu, e := url.Parse(c)
		if e != nil || id == "" || family == "" || cu.Scheme != "https" || cu.Host != "music.youtube.com" || !strings.HasPrefix(cu.Path, "/browse/") {
			t.Fatalf("unsafe %q", raw)
		}
		if _, ok := youtubeMusicBrowseFamily(id); !ok {
			t.Fatalf("unregistered family accepted %q", id)
		}
	})
}
