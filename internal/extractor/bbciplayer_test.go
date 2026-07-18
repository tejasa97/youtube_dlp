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
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestBBCIPlayerRoutingEpisodeGeoFallbackAndFormats(t *testing.T) {
	for _, rawURL := range []string{
		"https://www.bbc.co.uk/iplayer/episode/p0000000/title",
		"https://bbc.co.uk/programmes/p0000000/player",
		"https://www.bbc.co.uk/iplayer/episodes/p0000000/title",
		"https://www.bbc.co.uk/iplayer/group/p0000000",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewBBCIPlayer().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{"https://bbc.com/iplayer/episode/p0000000", "https://bbc.co.uk/news/id", "ftp://bbc.co.uk/programmes/p0000000"} {
		parsed, _ := url.Parse(rawURL)
		if NewBBCIPlayer().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
	pageURL := "https://www.bbc.co.uk/iplayer/episode/p0000000/title"
	iptv := bbcMediaSelectorBase + "iptv-all/vpid/p0000001"
	pc := bbcMediaSelectorBase + "pc/vpid/p0000001"
	transport := &riskFixtureTransport{
		pages: map[string][]byte{pageURL: readRiskFixture(t, "bbciplayer", "episode.html")},
		responses: map[string]riskFixtureResponse{
			"GET " + iptv: {body: []byte(`{"result":"geolocation"}`)},
			"GET " + pc:   {body: readRiskFixture(t, "bbciplayer", "selector.json")},
		},
	}
	result, err := NewBBCIPlayer().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	assertRiskString(t, result, "id", "p0000001")
	assertRiskString(t, result, "title", "Fixture BBC Episode")
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 3 {
		t.Fatalf("formats = %#v", formats)
	}
	wantProtocols := map[string]bool{"m3u8_native": false, "http_dash_segments": false, "https": false}
	for _, formatValue := range formats {
		format, _ := formatValue.Object()
		protocol, _ := format.Lookup("protocol").StringValue()
		wantProtocols[protocol] = true
	}
	for protocol, found := range wantProtocols {
		if !found {
			t.Fatalf("missing protocol %q", protocol)
		}
	}
	if subtitles, ok := result.Info.Lookup("subtitles").Object(); !ok || subtitles.Lookup("en").IsMissing() {
		t.Fatalf("subtitles = %#v", result.Info.Lookup("subtitles"))
	}
}

func TestBBCIPlayerPlaylistIsLazy(t *testing.T) {
	transport := &riskFixtureTransport{}
	transport.handler = func(_ context.Context, request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		var call struct {
			Variables struct {
				Page    int `json:"page"`
				PerPage int `json:"perPage"`
			} `json:"variables"`
		}
		if err := json.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.Variables.PerPage == 1 {
			return riskHTTPResponse(http.StatusOK, []byte(`{"data":{"programme":{"title":{"default":"Fixture Series"},"synopsis":{"large":"Synthetic series"},"entities":{"results":[]}}}}`)), nil
		}
		if call.Variables.Page != 1 || call.Variables.PerPage != bbcPlaylistPageSize {
			t.Fatalf("playlist variables = %#v", call.Variables)
		}
		return riskHTTPResponse(http.StatusOK, []byte(`{"data":{"programme":{"entities":{"results":[{"episode":{"id":"p0000001","subtitle":{"default":"Episode One"}}},{"episode":{"id":"p0000002","subtitle":{"default":"Episode Two"}}}]}}}}`)), nil
	}
	result, err := NewBBCIPlayer().Extract(context.Background(), Request{URL: "https://www.bbc.co.uk/iplayer/episodes/p0000000/fixture", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || len(transport.requests) != 1 {
		t.Fatalf("playlist=%t requests=%d", result.IsPlaylist(), len(transport.requests))
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "bbciplayer" {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests after iteration = %d", len(transport.requests))
	}
}

func TestRiskFormatsIntegrateWithHLS_DASHAndDirectDownloaders(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	fixture := fmt.Sprintf(`{"media":[{"kind":"video","connection":[
		{"href":%q,"supplier":"hls","transferFormat":"hls"},
		{"href":%q,"supplier":"dash","transferFormat":"dash"},
		{"href":%q,"supplier":"http","transferFormat":"progressive"}]}]}`,
		server.URL+"/hls/master.m3u8", server.URL+"/dash/manifest.mpd", server.URL+"/media")
	var selection bbcMediaSelection
	if err := json.Unmarshal([]byte(fixture), &selection); err != nil {
		t.Fatal(err)
	}
	formats, _ := normalizeBBCMediaSelection(selection, make(map[string]bool))
	byProtocol := make(map[string]string)
	for _, formatValue := range formats {
		format, _ := formatValue.Object()
		protocol, _ := format.Lookup("protocol").StringValue()
		rawURL, _ := format.Lookup("url").StringValue()
		byProtocol[protocol] = rawURL
	}
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	hlsPath := filepath.Join(root, "fixture-hls.bin")
	if _, err := hls.NewDownloader(transport, hls.Config{}).Download(context.Background(), byProtocol["m3u8_native"], root, hlsPath, false, nil); err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(hlsPath); !bytes.Equal(contents, server.HLSMedia()) {
		t.Fatalf("HLS contents = %q", contents)
	}
	dashResult, err := dash.NewDownloader(transport, dash.Config{}).Download(context.Background(), byProtocol["http_dash_segments"], root, filepath.Join(root, "fixture-dash.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashResult.Tracks) != 1 {
		t.Fatalf("DASH tracks = %#v", dashResult.Tracks)
	}
	if contents, _ := os.ReadFile(dashResult.Tracks[0].Download.Path); !bytes.Equal(contents, server.DASHMedia()) {
		t.Fatalf("DASH contents = %q", contents)
	}
	request, _ := http.NewRequest(http.MethodGet, byProtocol["http"], nil)
	response, err := transport.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if contents, _ := io.ReadAll(response.Body); !bytes.Equal(contents, server.Media()) {
		t.Fatalf("direct contents length = %d", len(contents))
	}
}

func TestBBCIPlayerFailureCategoriesCancellationAndSecretSafety(t *testing.T) {
	pageURL := "https://www.bbc.co.uk/iplayer/episode/p0000000"
	for _, test := range []struct {
		name      string
		page      string
		selection string
		status    int
		want      error
	}{
		{"auth-page", `Sign in to watch`, ``, 0, ErrAuthentication},
		{"geo-page", `This programme is only available in the UK`, ``, 0, ErrRegionRestricted},
		{"unavailable-page", `This programme is no longer available`, ``, 0, ErrUnavailable},
		{"geo-selector", `{"vpid":"p0000001"}`, `{"result":"geolocation"}`, 0, ErrRegionRestricted},
		{"auth-selector", `{"vpid":"p0000001"}`, ``, http.StatusUnauthorized, ErrAuthentication},
		{"unavailable-selector", `{"vpid":"p0000001"}`, `{"result":"selectionunavailable"}`, 0, ErrUnavailable},
		{"malformed-selector", `{"vpid":"p0000001"}`, `{"secret":"bbc-private-token"} trailing`, 0, ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &riskFixtureTransport{pages: map[string][]byte{pageURL: []byte(test.page)}, handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
				status := test.status
				if status == 0 {
					status = http.StatusOK
				}
				return riskHTTPResponse(status, []byte(test.selection)), nil
			}}
			_, err := NewBBCIPlayer().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "bbc-private-token") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewBBCIPlayer().Extract(ctx, Request{URL: pageURL, Transport: &riskFixtureTransport{wait: true}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func FuzzParseBBCMediaSelector(f *testing.F) {
	f.Add(readRiskFixture(f, "bbciplayer", "selector.json"))
	f.Add([]byte(`{"result":"geolocation"}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var selection bbcMediaSelection
		if json.Unmarshal(data, &selection) == nil {
			_, _ = normalizeBBCMediaSelection(selection, make(map[string]bool))
		}
	})
}
