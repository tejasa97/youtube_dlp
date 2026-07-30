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
	"strconv"
	"strings"
	"sync"
	"testing"
)

type bandcampFixtureTransport struct {
	mu       sync.Mutex
	pages    map[string][]byte
	api      func(context.Context, *http.Request) (int, []byte, error)
	requests []*http.Request
}

func readBandcampFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../../conformance/extractors/bandcamp/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (transport *bandcampFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if transport.api == nil {
		return publicExtractorResponse(http.StatusNotFound, nil), nil
	}
	status, body, err := transport.api(ctx, request)
	if err != nil {
		return nil, err
	}
	return publicExtractorResponse(status, body), nil
}

func (transport *bandcampFixtureTransport) ReadPage(ctx context.Context, raw string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	body, ok := transport.pages[raw]
	if !ok {
		return nil, nil, errors.New("unexpected Bandcamp page request")
	}
	return append([]byte(nil), body...), make(http.Header), nil
}

func (transport *bandcampFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(header) != "" {
			return nil, errors.New("credential leak")
		}
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(ctx))
	transport.mu.Unlock()
	if transport.api != nil {
		status, body, err := transport.api(ctx, request)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	}
	body, ok := transport.pages[request.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

func TestBandcampUserRoutingAndHardening(t *testing.T) {
	accepted := []string{
		"https://fixture.bandcamp.com",
		"https://fixture.bandcamp.com/",
		"https://fixture.bandcamp.com/music",
		"https://fixture.bandcamp.com/music/",
		"https://fixture.bandcamp.com?ref=home",
		"https://fixture.bandcamp.com/music#discography",
		"http://fixture.bandcamp.com/music",
	}
	for _, raw := range accepted {
		parsed, err := url.Parse(raw)
		if err != nil || !NewBandcampUser().Suitable(parsed) {
			t.Errorf("Suitable(%q) = false (%v)", raw, err)
		}
	}
	rejected := []string{
		"https://www.bandcamp.com",
		"https://bandcamp.com/radio?show=1",
		"https://fixture.bandcamp.com/track/a",
		"https://fixture.bandcamp.com/album/a",
		"https://fixture.bandcamp.com/music/extra",
		"https://www.fixture.bandcamp.com",
		"https://fixture.bandcamp.com:443/music",
		"https://user@fixture.bandcamp.com/music",
		"https://fixture.bandcamp.com/encoded%2fpath",
		"https://fixture.bandcamp.com/encoded%5cpath",
		"https://fixture.bandcamp.com/nul%00path",
		"https://fixture.notbandcamp.com",
		"https://fixture.bandcamp.com.evil.invalid",
	}
	for _, raw := range rejected {
		parsed, err := url.Parse(raw)
		if err == nil && NewBandcampUser().Suitable(parsed) {
			t.Errorf("Suitable(%q) = true", raw)
		}
	}
}

func TestBandcampWeeklyRouting(t *testing.T) {
	accepted := []string{
		"https://bandcamp.com/radio?show=224",
		"https://bandcamp.com/radio/?foo=bar&show=224",
		"https://www.bandcamp.com/radio?show=42",
		"https://bandcamp.com/radio?foo=bar&show=224",
	}
	for _, raw := range accepted {
		parsed, err := url.Parse(raw)
		if err != nil || !NewBandcampWeekly().Suitable(parsed) {
			t.Errorf("Suitable(%q) = false (%v)", raw, err)
		}
	}
	rejected := []string{
		"http://bandcamp.com/radio?show=224",
		"https://bandcamp.com/radio",
		"https://bandcamp.com/radio?show=",
		"https://bandcamp.com/radio?show=0",
		"https://bandcamp.com/radio?show=01",
		"https://bandcamp.com/radio?show=-1",
		"https://bandcamp.com/radio?show=abc",
		"https://bandcamp.com/radio?show=224abc",
		"https://bandcamp.com/radio?show=224#fragment",
		"https://bandcamp.com/radio?show=1&show=2",
		"https://bandcamp.com/radio?show=224&&foo=bar",
		"https://bandcamp.com/radio?show=224%ZZ",
		"https://bandcamp.com/radio?show",
		"https://fixture.bandcamp.com/radio?show=1",
		"https://bandcamp.com/radio/show=1",
	}
	for _, raw := range rejected {
		parsed, err := url.Parse(raw)
		if err == nil && NewBandcampWeekly().Suitable(parsed) {
			t.Errorf("Suitable(%q) = true", raw)
		}
	}
}

func TestBandcampUserType1Discovery(t *testing.T) {
	raw := "https://fixture.bandcamp.com"
	transport := &bandcampFixtureTransport{pages: map[string][]byte{raw: readBandcampFixture(t, "user_type1.html")}}
	result, err := NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://fixture.bandcamp.com/album/first-album",
		"https://fixture.bandcamp.com/album/second-album",
	}
	if len(entries) != len(want) {
		t.Fatalf("entries=%#v", entries)
	}
	for index, rawURL := range want {
		if entries[index].URL != rawURL || entries[index].ExtractorKey != "bandcamp" || !entries[index].Transparent {
			t.Fatalf("entry %d = %#v", index, entries[index])
		}
	}
	if title, _ := result.Info.Title(); title != "Discography of fixture" {
		t.Fatalf("title=%q", title)
	}
}

func TestBandcampUserType2AndType3Discovery(t *testing.T) {
	for _, test := range []struct {
		name  string
		wantN int
	}{
		{"user_type2.html", 2},
		{"user_type3.html", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := "https://fixture.bandcamp.com/music"
			transport := &bandcampFixtureTransport{pages: map[string][]byte{raw: readBandcampFixture(t, test.name)}}
			result, err := NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			entries, err := CollectEntries(context.Background(), result.Entries, 10)
			if err != nil || len(entries) != test.wantN {
				t.Fatalf("entries=%d err=%v", len(entries), err)
			}
		})
	}
}

func TestBandcampUserCombinedOrderingAndDuplicates(t *testing.T) {
	raw := "https://fixture.bandcamp.com"
	transport := &bandcampFixtureTransport{pages: map[string][]byte{raw: readBandcampFixture(t, "user_combined.html")}}
	result, err := NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://fixture.bandcamp.com/album/alpha",
		"https://fixture.bandcamp.com/track/beta",
	}
	if len(entries) != len(want) {
		t.Fatalf("entries=%#v", entries)
	}
	for index, rawURL := range want {
		if entries[index].URL != rawURL {
			t.Fatalf("entry %d = %q", index, entries[index].URL)
		}
	}
}

func TestBandcampUserCredentialIsolationAndCancellation(t *testing.T) {
	raw := "https://fixture.bandcamp.com"
	transport := &bandcampFixtureTransport{pages: map[string][]byte{raw: readBandcampFixture(t, "user_type1.html")}}
	_, err := NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: &publicExtractorTransport{}})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("isolation=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewBandcampUser().Extract(ctx, Request{URL: raw, Transport: transport})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	result, err := NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 || transport.requests[0].Method != http.MethodGet {
		t.Fatalf("requests=%#v", transport.requests)
	}
	if transport.requests[0].Header.Get("Authorization") != "" || transport.requests[0].Header.Get("Cookie") != "" {
		t.Fatal("credential headers leaked")
	}
	_ = result
}

func TestBandcampUserMalformedAndOversized(t *testing.T) {
	raw := "https://fixture.bandcamp.com"
	transport := &bandcampFixtureTransport{pages: map[string][]byte{
		raw: []byte(`<div id="music-grid" data-client-items='[{"page_url":""}'></div>`),
	}}
	_, err := NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed=%v", err)
	}
	oversized := &bandcampFixtureTransport{pages: map[string][]byte{raw: make([]byte, bandcampMaxHTMLBytes+1)}}
	_, err = NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: oversized})
	if !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversized=%v", err)
	}
}

func TestBandcampWeeklyStringDateMetadata(t *testing.T) {
	payload := []byte(`{"tracklist":{"title":"Episode","subtitle":"Bandcamp Weekly","date":"2017-04-04","imageId":"9982549","compiledTrack":{"streamUrl":"https://stream.bcbits.com/stream/fixture?enc=mp3-128","duration":1}}}`)
	transport := &bandcampFixtureTransport{api: func(context.Context, *http.Request) (int, []byte, error) {
		return http.StatusOK, payload, nil
	}}
	result, err := NewBandcampWeekly().Extract(context.Background(), Request{
		URL: "https://bandcamp.com/radio?show=224", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if title, _ := result.Info.Title(); title != "Bandcamp Weekly, 2017-04-04" {
		t.Fatalf("title=%q", title)
	}
	if ts, _ := result.Info.Lookup("release_timestamp").Int(); ts != 1491264000 {
		t.Fatalf("release_timestamp=%d", ts)
	}
}

func TestBandcampWeeklySuccessRequestAndMetadata(t *testing.T) {
	fixture := readBandcampFixture(t, "weekly_player_response.json")
	transport := &bandcampFixtureTransport{api: func(_ context.Context, request *http.Request) (int, []byte, error) {
		if request.Method != http.MethodPost || request.URL.String() != bandcampWeeklyAPIURL {
			t.Fatalf("endpoint=%s method=%s", request.URL, request.Method)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type=%q", request.Header.Get("Content-Type"))
		}
		if request.Header.Get("Origin") != "" {
			t.Fatalf("origin=%q", request.Header.Get("Origin"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["item_id"] != float64(224) || payload["item_type"] != "radio" {
			t.Fatalf("payload=%v", payload)
		}
		return http.StatusOK, fixture, nil
	}}
	result, err := NewBandcampWeekly().Extract(context.Background(), Request{
		URL:       "https://bandcamp.com/radio?show=224",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() {
		t.Fatal("weekly result is playlist")
	}
	if id, _ := result.Info.ID(); id != "224" {
		t.Fatalf("id=%q", id)
	}
	if title, _ := result.Info.Title(); title != "Bandcamp Weekly, 2017-04-04" {
		t.Fatalf("title=%q", title)
	}
	if episode, _ := result.Info.Lookup("episode").StringValue(); episode != "Magic Moments" {
		t.Fatalf("episode=%q", episode)
	}
	if thumb, _ := result.Info.Lookup("thumbnail").StringValue(); thumb != "https://f4.bcbits.com/img/9982549_0.jpg" {
		t.Fatalf("thumbnail=%q", thumb)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats=%v", formats)
	}
	format, ok := formats[0].Object()
	if !ok || format == nil {
		t.Fatal("missing format object")
	}
	if formatID, _ := format.Lookup("format_id").StringValue(); formatID != "mp3-128" {
		t.Fatalf("format_id=%q", formatID)
	}
	streamURL, _ := format.Lookup("url").StringValue()
	if !strings.Contains(streamURL, "enc=mp3-128") {
		t.Fatalf("stream=%q", streamURL)
	}
	if isolated, ok := format.Lookup("_credential_isolated").Bool(); !ok || !isolated {
		t.Fatalf("credential isolation marker missing: %v", isolated)
	}
}

func TestBandcampWeeklyMalformedDateIgnored(t *testing.T) {
	payload := []byte(`{"tracklist":{"title":"Episode","subtitle":"Bandcamp Weekly","date":"not-a-real-date","imageId":"9982549","compiledTrack":{"streamUrl":"https://stream.bcbits.com/stream/fixture?enc=mp3-128","duration":1}}}`)
	transport := &bandcampFixtureTransport{api: func(context.Context, *http.Request) (int, []byte, error) {
		return http.StatusOK, payload, nil
	}}
	result, err := NewBandcampWeekly().Extract(context.Background(), Request{
		URL: "https://bandcamp.com/radio?show=224", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Info.Lookup("release_timestamp").Int(); ok {
		t.Fatal("malformed date produced release_timestamp")
	}
}

func TestBandcampWeeklyErrorSecretSafety(t *testing.T) {
	secret := "supersecret-token-value"
	transport := &bandcampFixtureTransport{api: func(context.Context, *http.Request) (int, []byte, error) {
		return http.StatusOK, []byte(`{"tracklist":{"compiledTrack":{"streamUrl":"https://evil.invalid/x?token=` + secret + `"}}}`), nil
	}}
	_, err := NewBandcampWeekly().Extract(context.Background(), Request{
		URL: "https://bandcamp.com/radio?show=224", Transport: transport,
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("err=%v", err)
	}
}

func TestBandcampWeeklyFailuresIsolationAndCancel(t *testing.T) {
	transport := &bandcampFixtureTransport{api: func(context.Context, *http.Request) (int, []byte, error) {
		return http.StatusOK, []byte(`{"tracklist":{"compiledTrack":{"streamUrl":"https://evil.invalid/stream"}}}`), nil
	}}
	_, err := NewBandcampWeekly().Extract(context.Background(), Request{
		URL: "https://bandcamp.com/radio?show=224", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile stream=%v", err)
	}
	_, err = NewBandcampWeekly().Extract(context.Background(), Request{
		URL: "https://bandcamp.com/radio?show=224", Transport: &publicExtractorTransport{},
	})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("isolation=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewBandcampWeekly().Extract(ctx, Request{
		URL: "https://bandcamp.com/radio?show=224", Transport: transport,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestBandcampUserPageReadFailureIsTyped(t *testing.T) {
	raw := "https://fixture.bandcamp.com"
	transport := &bandcampBrokenPageTransport{}
	_, err := NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if err == nil || !errors.Is(err, ErrBandcampPageNetwork) {
		t.Fatalf("err=%v", err)
	}
}

type bandcampBrokenPageTransport struct{}

func (bandcampBrokenPageTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page transport")
}

func (bandcampBrokenPageTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected ambient transport")
}

func (bandcampBrokenPageTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bandcampErrReader{}),
		Request:    request,
	}, nil
}

type bandcampErrReader struct{}

func (bandcampErrReader) Read([]byte) (int, error) { return 0, errors.New("broken body") }

func TestBandcampUserEntryOverflow(t *testing.T) {
	var items strings.Builder
	items.WriteByte('[')
	for i := 0; i <= bandcampDiscographyLimit; i++ {
		if i > 0 {
			items.WriteByte(',')
		}
		items.WriteString(`{"page_url":"/album/a`)
		items.WriteString(strconv.Itoa(i))
		items.WriteString(`"}`)
	}
	items.WriteByte(']')
	page := `<div id="music-grid" data-client-items='` + items.String() + `'></div>`
	raw := "https://fixture.bandcamp.com"
	transport := &bandcampFixtureTransport{pages: map[string][]byte{raw: []byte(page)}}
	_, err := NewBandcampUser().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("overflow=%v", err)
	}
}

func TestBandcampMusicGridAttributeBoundaries(t *testing.T) {
	cases := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "reversed attribute order",
			html: `<div data-client-items='[{"page_url":"/album/reversed"}]' id="music-grid"></div>`,
			want: []string{"/album/reversed"},
		},
		{
			name: "double quoted attribute",
			html: `<div id="music-grid" data-client-items="[{&quot;page_url&quot;:&quot;/album/quoted&quot;}]"></div>`,
			want: []string{"/album/quoted"},
		},
		{
			name: "cross tag ignored",
			html: `<div id="music-grid"></div><div data-client-items='[{"page_url":"/album/wrong"}]'></div>`,
			want: nil,
		},
		{
			name: "missing close quote",
			html: `<div id="music-grid" data-client-items='[{"page_url":"/album/open"></div>`,
			want: nil,
		},
		{
			name: "xid marker ignored",
			html: `<div xid="music-grid" data-client-items='[{"page_url":"/album/attack"}]'></div>`,
			want: nil,
		},
		{
			name: "xdata-client-items prefix ignored",
			html: `<div id="music-grid" xdata-client-items='[{"page_url":"/album/attack"}]' data-client-items='[{"page_url":"/album/legit"}]'></div>`,
			want: []string{"/album/legit"},
		},
		{
			name: "reversed order with xdata prefix ignored",
			html: `<div xdata-client-items='[{"page_url":"/album/attack"}]' data-client-items='[{"page_url":"/album/reversed"}]' id="music-grid"></div>`,
			want: []string{"/album/reversed"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			links, err := parseBandcampMusicGridLinks(test.html)
			if err != nil {
				t.Fatal(err)
			}
			if len(links) != len(test.want) {
				t.Fatalf("links=%v want=%v", links, test.want)
			}
			for i := range test.want {
				if links[i] != test.want[i] {
					t.Fatalf("link %d=%q want %q", i, links[i], test.want[i])
				}
			}
		})
	}
}

func TestBandcampSafeStreamURLRejectsFragments(t *testing.T) {
	signed := "https://stream.bcbits.com/stream/fixture?enc=mp3-128&sig=fixture-secret"
	if got, ok := bandcampSafeStreamURL(signed + "#evil"); ok || got != "" {
		t.Fatalf("fragment accepted: got=%q ok=%v", got, ok)
	}
	if got, ok := bandcampSafeStreamURL(signed); !ok || got != signed {
		t.Fatalf("signed query rejected: got=%q ok=%v", got, ok)
	}
}

func FuzzClassifyBandcampArtistURL(f *testing.F) {
	for _, seed := range []string{
		"https://fixture.bandcamp.com",
		"https://fixture.bandcamp.com/music",
		"https://www.bandcamp.com",
		"https://fixture.bandcamp.com/track/a",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		if NewBandcampUser().Suitable(parsed) {
			if _, ok := classifyBandcampArtistURL(parsed); !ok {
				t.Fatalf("Suitable true but classify false for %q", rawURL)
			}
		}
	})
}

func FuzzClassifyBandcampWeeklyURL(f *testing.F) {
	for _, seed := range []string{
		"https://bandcamp.com/radio?show=224",
		"https://bandcamp.com/radio?show=0",
		"https://fixture.bandcamp.com/radio?show=1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		if NewBandcampWeekly().Suitable(parsed) {
			if _, ok := classifyBandcampWeeklyURL(parsed); !ok {
				t.Fatalf("Suitable true but classify false for %q", rawURL)
			}
		}
	})
}

func FuzzParseBandcampMusicGrid(f *testing.F) {
	f.Add(`[{"page_url":"/album/a"}]`)
	f.Add(`not-json`)
	f.Fuzz(func(t *testing.T, payload string) {
		page := `<div id="music-grid" data-client-items='` + payload + `'></div>`
		links, err := parseBandcampMusicGridLinks(page)
		if err != nil && !errors.Is(err, ErrInvalidMetadata) && !errors.Is(err, ErrJSONResponseTooLarge) {
			t.Fatalf("unexpected err=%v payload=%q", err, payload)
		}
		_ = links
	})
}
