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
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

type ardAudiothekFixtureTransport struct {
	mu          sync.Mutex
	responses   map[string]riskFixtureResponse
	handler     func(context.Context, *http.Request) (*http.Response, error)
	requests    []string
	noRedirects int
	wait        bool
}

func (transport *ardAudiothekFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *ardAudiothekFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page read")
}

func (transport *ardAudiothekFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(header) != "" {
			return nil, errors.New("credential leak")
		}
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	transport.noRedirects++
	handler := transport.handler
	response, ok := transport.responses[request.Method+" "+request.URL.String()]
	transport.mu.Unlock()
	if transport.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if handler != nil {
		return handler(ctx, request)
	}
	if !ok {
		return riskHTTPResponse(http.StatusNotFound, nil), nil
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return riskHTTPResponse(status, response.body), nil
}

func ardAudiothekGraphQLHandler(t *testing.T, fixtureName string) func(context.Context, *http.Request) (*http.Response, error) {
	t.Helper()
	body := readRiskFixture(t, "ard_audiothek", fixtureName)
	return func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != ardAudiothekGraphQLURL {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q", request.Header.Get("Content-Type"))
		}
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("payload = %s", payload)
		}
		if envelope.Variables["id"] == "" || envelope.Query == "" {
			t.Fatalf("invalid graphql payload %#v", envelope)
		}
		return riskHTTPResponse(http.StatusOK, body), nil
	}
}

func TestARDAudiothekRoutingAndNonOverlap(t *testing.T) {
	episodeURN := "urn:ard:episode:eabead1add170e93"
	showURN := "urn:ard:show:c405aa26d9a4060a"
	for _, rawURL := range []string{
		"https://www.ardaudiothek.de/episode/" + episodeURN + "/",
		"https://ardaudiothek.de/episode/urn:ard:section:855c7a53dac72e0a",
		"https://www.ardsounds.de/episode/urn:ard:extra:d2fe7303d2dcbf5d/",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewARDAudiothek().Suitable(parsed) {
			t.Fatalf("episode routing failed for %q", rawURL)
		}
		if NewARDAudiothekPlaylist().Suitable(parsed) {
			t.Fatalf("playlist claimed episode URL %q", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://www.ardaudiothek.de/sendung/mia-insomnia/" + showURN + "/",
		"https://www.ardsounds.de/sendung/100-berlin/urn:ard:show:4d248e0806ce37bc/",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewARDAudiothekPlaylist().Suitable(parsed) {
			t.Fatalf("playlist routing failed for %q", rawURL)
		}
		if NewARDAudiothek().Suitable(parsed) {
			t.Fatalf("episode claimed playlist URL %q", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://www.ardmediathek.de/player/Y3JpZDovL2ZpeHR1cmU",
		"https://example.com/episode/" + episodeURN,
		"https://www.ardaudiothek.de/sendung/only-slug",
		"https://www.ardaudiothek.de/other/" + episodeURN,
		"ftp://www.ardaudiothek.de/episode/" + episodeURN,
	} {
		parsed, _ := url.Parse(rawURL)
		if NewARDAudiothek().Suitable(parsed) || NewARDAudiothekPlaylist().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
}

func TestARDAudiothekEpisodeFormatsAndMetadata(t *testing.T) {
	episodeURN := "urn:ard:episode:eabead1add170e93"
	pageURL := "https://www.ardaudiothek.de/episode/" + episodeURN + "/"
	transport := &ardAudiothekFixtureTransport{handler: ardAudiothekGraphQLHandler(t, "episode.json")}
	result, err := NewARDAudiothek().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	assertRiskString(t, result, "id", episodeURN)
	assertRiskString(t, result, "title", "Fixture Audiothek Episode")
	assertRiskString(t, result, "series", "Fixture Audiothek Show")
	assertRiskString(t, result, "channel", "WDR")
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats = %#v", formats)
	}
	format, _ := formats[0].Object()
	isolated, ok := format.Lookup("_credential_isolated").Bool()
	if !ok || !isolated {
		t.Fatalf("credential isolation missing: %#v", format)
	}
	vcodec, _ := format.Lookup("vcodec").StringValue()
	if vcodec != "none" {
		t.Fatalf("vcodec = %q", vcodec)
	}
	if transport.noRedirects != 1 {
		t.Fatalf("no-redirect calls = %d", transport.noRedirects)
	}
}

func TestARDAudiothekPlaylistEagerReentry(t *testing.T) {
	showURN := "urn:ard:show:c405aa26d9a4060a"
	pageURL := "https://www.ardaudiothek.de/sendung/mia-insomnia/" + showURN + "/"
	transport := &ardAudiothekFixtureTransport{handler: ardAudiothekGraphQLHandler(t, "playlist.json")}
	result, err := NewARDAudiothekPlaylist().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || transport.noRedirects != 1 {
		t.Fatalf("playlist=%t redirects=%d", result.IsPlaylist(), transport.noRedirects)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if entries[0].ExtractorKey != "ard_audiothek" || !entries[0].Transparent {
		t.Fatalf("entry=%#v", entries[0])
	}
	// Upstream performs one eager GraphQL fetch; iteration must not trigger more.
	_, err = CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || transport.noRedirects != 1 {
		t.Fatalf("second iteration redirects=%d err=%v", transport.noRedirects, err)
	}
	registry := NewRegistry(NewARDAudiothekPlaylist(), NewARDAudiothek())
	selected, err := registry.SelectFor(entries[0].URL, entries[0].ExtractorKey)
	if err != nil || selected.Name() != "ard_audiothek" {
		t.Fatalf("reentry=%v err=%v", selected, err)
	}
}

func TestARDAudiothekFailureCategoriesCancellationAndSecretSafety(t *testing.T) {
	episodeURN := "urn:ard:episode:cafebabe00000001"
	pageURL := "https://www.ardaudiothek.de/episode/" + episodeURN + "/"
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"auth-status", http.StatusUnauthorized, `{}`, ErrAuthentication},
		{"geo-status", http.StatusUnavailableForLegalReasons, `{}`, ErrRegionRestricted},
		{"unavailable-status", http.StatusNotFound, `{}`, ErrUnavailable},
		{"missing-item", http.StatusOK, `{"data":{"item":null}}`, ErrUnavailable},
		{"missing-data", http.StatusOK, `{}`, ErrInvalidMetadata},
		{"null-data", http.StatusOK, `{"data":null}`, ErrInvalidMetadata},
		{"graphql-errors", http.StatusOK, `{"errors":[{"message":"ard-private-token"}]}`, ErrInvalidMetadata},
		{"no-formats", http.StatusOK, `{"data":{"item":{"title":"No audio","audioList":[]}}}`, ErrUnavailable},
		{"malformed", http.StatusOK, `{"secret":"ard-private-token"} trailing`, ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &ardAudiothekFixtureTransport{responses: map[string]riskFixtureResponse{
				"POST " + ardAudiothekGraphQLURL: {status: test.status, body: []byte(test.body)},
			}}
			_, err := NewARDAudiothek().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "ard-private-token") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewARDAudiothek().Extract(ctx, Request{URL: pageURL, Transport: &ardAudiothekFixtureTransport{wait: true}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestARDAudiothekGraphQLEnvelopePlaylist(t *testing.T) {
	showURN := "urn:ard:show:c405aa26d9a4060a"
	pageURL := "https://www.ardaudiothek.de/sendung/mia-insomnia/" + showURN + "/"
	for _, test := range []struct {
		name string
		body string
		want error
	}{
		{"missing-show", `{"data":{"show":null}}`, ErrUnavailable},
		{"missing-data", `{}`, ErrInvalidMetadata},
		{"graphql-errors", `{"errors":[{"message":"secret-graphql-token"}]}`, ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &ardAudiothekFixtureTransport{responses: map[string]riskFixtureResponse{
				"POST " + ardAudiothekGraphQLURL: {status: http.StatusOK, body: []byte(test.body)},
			}}
			_, err := NewARDAudiothekPlaylist().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "secret-graphql-token") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
}

func TestARDAudiothekPlaylistNormalization(t *testing.T) {
	valid := "https://www.ardaudiothek.de/episode/urn:ard:episode:cafebabe00000001"
	show := &ardAudiothekShow{}
	show.Items.Nodes = []struct {
		URL string `json:"url"`
	}{
		{URL: valid},
		{URL: valid},
		{URL: "https://evil-ardaudiothek.de/episode/urn:ard:episode:deadbeef00000001"},
		{URL: "https://www.ardaudiothek.de.evil.com/episode/urn:ard:episode:deadbeef00000002"},
		{URL: "https://notardaudiothek.de/episode/urn:ard:episode:deadbeef00000003"},
		{URL: "not-a-url"},
		{URL: "javascript:alert(1)"},
		{URL: "https://www.ardaudiothek.de/sendung/slug/urn:ard:show:c405aa26d9a4060a"},
		{URL: ""},
	}
	entries, err := ardAudiothekPlaylistEntries(show)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if entries[0].URL != valid+"/" {
		t.Fatalf("entry URL = %q", entries[0].URL)
	}

	atLimit := &ardAudiothekShow{}
	atLimit.Items.Nodes = make([]struct {
		URL string `json:"url"`
	}, ardAudiothekMaxPlaylistEntries)
	for i := range atLimit.Items.Nodes {
		atLimit.Items.Nodes[i].URL = fmt.Sprintf(
			"https://www.ardaudiothek.de/episode/urn:ard:episode:%016x", i+1)
	}
	entries, err = ardAudiothekPlaylistEntries(atLimit)
	if err != nil || len(entries) != ardAudiothekMaxPlaylistEntries {
		t.Fatalf("at-limit entries=%d err=%v", len(entries), err)
	}

	overLimit := &ardAudiothekShow{}
	overLimit.Items.Nodes = make([]struct {
		URL string `json:"url"`
	}, ardAudiothekMaxPlaylistEntries+1)
	for i := range overLimit.Items.Nodes {
		overLimit.Items.Nodes[i].URL = fmt.Sprintf(
			"https://www.ardaudiothek.de/episode/urn:ard:episode:%016x", i+1)
	}
	_, err = ardAudiothekPlaylistEntries(overLimit)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestARDAudiothekRequiresCredentialIsolation(t *testing.T) {
	_, err := NewARDAudiothek().Extract(context.Background(), Request{
		URL:       "https://www.ardaudiothek.de/episode/urn:ard:episode:cafebabe00000001/",
		Transport: &riskFixtureTransport{},
	})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzClassifyARDAudiothekURL(f *testing.F) {
	f.Add("https://www.ardaudiothek.de/episode/urn:ard:episode:eabead1add170e93/")
	f.Add("https://www.ardsounds.de/sendung/mia-insomnia/urn:ard:show:c405aa26d9a4060a/")
	f.Add("https://www.ardmediathek.de/player/id")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 4096 {
			t.Skip()
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		target, ok := classifyARDAudiothekURL(parsed)
		if !ok {
			return
		}
		if target.playlist && strings.Contains(parsed.Path, "/episode/") {
			t.Fatalf("playlist target from episode route: %#v", target)
		}
		if NewARDAudiothek().Suitable(parsed) && NewARDAudiothekPlaylist().Suitable(parsed) {
			t.Fatalf("both extractors claimed %q", rawURL)
		}
	})
}

func ardAudiothekItemAudioListFromGraphQL(data []byte) ([]struct {
	Href             string `json:"href"`
	DistributionType string `json:"distributionType"`
	AudioBitrate     int64  `json:"audioBitrate"`
	AudioCodec       string `json:"audioCodec"`
}, bool) {
	var envelope ardAudiothekGraphQLEnvelope
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, false
	}
	var payload ardAudiothekItemPayload
	if json.Unmarshal(envelope.Data, &payload) != nil || payload.Item == nil {
		return nil, false
	}
	return payload.Item.AudioList, true
}

func TestARDAudiothekFormatsFromFixtureSeed(t *testing.T) {
	audioList, ok := ardAudiothekItemAudioListFromGraphQL(readRiskFixture(t, "ard_audiothek", "episode.json"))
	if !ok {
		t.Fatal("fixture envelope decode failed")
	}
	formats := ardAudiothekFormats(audioList)
	if len(formats) != 2 {
		t.Fatalf("formats = %#v", formats)
	}
	for _, formatValue := range formats {
		format, _ := formatValue.Object()
		formatID, _ := format.Lookup("format_id").StringValue()
		ext, _ := format.Lookup("ext").StringValue()
		if !ardAudiothekFormatIDPattern.MatchString(formatID) || !ardAudiothekExtensionPattern.MatchString(ext) {
			t.Fatalf("unsafe metadata format_id=%q ext=%q", formatID, ext)
		}
	}
}

func TestARDAudiothekFormatsRejectMalformedMetadata(t *testing.T) {
	base := "https://cdn.example.invalid/ard/track"
	audioList := func(href, distributionType string) []struct {
		Href             string `json:"href"`
		DistributionType string `json:"distributionType"`
		AudioBitrate     int64  `json:"audioBitrate"`
		AudioCodec       string `json:"audioCodec"`
	} {
		return []struct {
			Href             string `json:"href"`
			DistributionType string `json:"distributionType"`
			AudioBitrate     int64  `json:"audioBitrate"`
			AudioCodec       string `json:"audioCodec"`
		}{{Href: href, DistributionType: distributionType}}
	}
	for _, test := range []struct {
		name      string
		audioList []struct {
			Href             string `json:"href"`
			DistributionType string `json:"distributionType"`
			AudioBitrate     int64  `json:"audioBitrate"`
			AudioCodec       string `json:"audioCodec"`
		}
		want int
	}{
		{"long-format-id", audioList(base+".mp3", strings.Repeat("a", ardAudiothekMaxFormatIDBytes+1)), 0},
		{"invalid-format-chars", audioList(base+".mp3", "bad/format"), 0},
		{"invalid-format-utf8", audioList(base+".mp3", "bad\x80id"), 0},
		{"long-extension", audioList(base+"."+strings.Repeat("a", ardAudiothekMaxExtensionBytes+1), "default"), 0},
		{"invalid-extension-chars", audioList(base+".evil-ext", "default"), 0},
		{"valid-default-ext", audioList(base, "default"), 1},
		{"valid-normalized-ext", audioList(base+".MP3", "high"), 1},
		{"reject-bad-keep-good", append(
			audioList(base+".mp3", strings.Repeat("a", ardAudiothekMaxFormatIDBytes+1)),
			audioList(base+".mp3", "default")[0],
		), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			formats := ardAudiothekFormats(test.audioList)
			if len(formats) != test.want {
				t.Fatalf("formats = %d, want %d", len(formats), test.want)
			}
		})
	}
}

func FuzzARDAudiothekFormats(f *testing.F) {
	f.Add(readRiskFixture(f, "ard_audiothek", "episode.json"))
	f.Add([]byte(`{"audioList":[{"href":"https://cdn.example.invalid/ard/direct.mp3","distributionType":"default","audioBitrate":128,"audioCodec":"mp3"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		audioList, ok := ardAudiothekItemAudioListFromGraphQL(data)
		if !ok {
			var item ardAudiothekItem
			if json.Unmarshal(data, &item) != nil || len(item.AudioList) == 0 {
				return
			}
			audioList = item.AudioList
		}
		formats := ardAudiothekFormats(audioList)
		for _, formatValue := range formats {
			format, ok := formatValue.Object()
			if !ok {
				continue
			}
			formatID, _ := format.Lookup("format_id").StringValue()
			ext, _ := format.Lookup("ext").StringValue()
			if len(formatID) > ardAudiothekMaxFormatIDBytes || !utf8.ValidString(formatID) || !ardAudiothekFormatIDPattern.MatchString(formatID) {
				t.Fatalf("unsafe format_id %q", formatID)
			}
			if len(ext) > ardAudiothekMaxExtensionBytes || !utf8.ValidString(ext) || !ardAudiothekExtensionPattern.MatchString(ext) {
				t.Fatalf("unsafe ext %q", ext)
			}
		}
	})
}

func TestARDAudiothekGraphQLRequestShape(t *testing.T) {
	var captured []byte
	transport := &ardAudiothekFixtureTransport{handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		captured = append([]byte(nil), body...)
		return riskHTTPResponse(http.StatusOK, readRiskFixture(t, "ard_audiothek", "episode.json")), nil
	}}
	_, err := NewARDAudiothek().Extract(context.Background(), Request{
		URL:       "https://www.ardaudiothek.de/episode/urn:ard:episode:cafebabe00000001/",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(captured, []byte(`"id":"urn:ard:episode:cafebabe00000001"`)) {
		t.Fatalf("payload = %s", captured)
	}
}
