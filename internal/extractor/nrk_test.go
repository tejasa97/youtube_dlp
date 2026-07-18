package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNRKRoutingProgrammeFormatsAndLive(t *testing.T) {
	for _, rawURL := range []string{
		"https://tv.nrk.no/program/MDDP12000117",
		"https://radio.nrk.no/serie/dagsnytt/NPUB21019315/12-07-2015",
		"https://tv.nrk.no/direkte/nrk1",
		"https://tv.nrk.no/serie/fixture",
		"https://tv.nrk.no/serie/fixture/sesong/1",
		"nrk:MDDP12000117",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewNRK().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{"https://example.com/program/MDDP12000117", "https://tv.nrk.no/unknown", "ftp://tv.nrk.no/program/MDDP12000117"} {
		parsed, _ := url.Parse(rawURL)
		if NewNRK().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
	manifestURL := nrkAPIBase + "playback/manifest/program/MDDP12000117?preferredCdn=akamai"
	metadataURL := nrkAPIBase + "playback/metadata/program/MDDP12000117"
	transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
		"GET " + manifestURL: {body: readRiskFixture(t, "nrk", "manifest.json")},
		"GET " + metadataURL: {body: readRiskFixture(t, "nrk", "metadata.json")},
	}}
	result, err := NewNRK().Extract(context.Background(), Request{URL: "https://tv.nrk.no/program/MDDP12000117", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	assertRiskString(t, result, "id", "MDDP12000117")
	assertRiskString(t, result, "title", "Fixture NRK Programme")
	formats, _ := result.Info.Formats()
	if len(formats) != 3 {
		t.Fatalf("formats = %#v", formats)
	}
	protocols := map[string]bool{}
	for _, formatValue := range formats {
		format, _ := formatValue.Object()
		protocol, _ := format.Lookup("protocol").StringValue()
		protocols[protocol] = true
	}
	for _, protocol := range []string{"m3u8_native", "http_dash_segments", "https"} {
		if !protocols[protocol] {
			t.Fatalf("missing protocol %q: %#v", protocol, protocols)
		}
	}
	if duration, ok := result.Info.Lookup("duration").Float(); !ok || duration != 2223.44 {
		t.Fatalf("duration = %v, %t", duration, ok)
	}
	channelManifestURL := nrkAPIBase + "playback/manifest/channel/nrk1?preferredCdn=akamai"
	channelMetadataURL := nrkAPIBase + "playback/metadata/channel/nrk1"
	liveManifest := strings.Replace(string(readRiskFixture(t, "nrk", "manifest.json")), `"isLive":false`, `"isLive":true`, 1)
	transport.responses["GET "+channelManifestURL] = riskFixtureResponse{body: []byte(liveManifest)}
	transport.responses["GET "+channelMetadataURL] = riskFixtureResponse{body: readRiskFixture(t, "nrk", "metadata.json")}
	live, err := NewNRK().Extract(context.Background(), Request{URL: "https://tv.nrk.no/direkte/nrk1", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if isLive, ok := live.Info.Lookup("is_live").Bool(); !ok || !isLive {
		t.Fatalf("is_live = %t, %t", isLive, ok)
	}
}

func TestNRKPlaylistIsLazyAndCursorRestricted(t *testing.T) {
	firstURL := nrkAPIBase + "tv/catalog/series/fixture?embeddedInstalmentsPageSize=50"
	secondURL := nrkAPIBase + "tv/catalog/series/fixture?page=2"
	transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
		"GET " + firstURL:  {body: []byte(`{"titles":{"title":"Fixture Series","subtitle":"Synthetic series"},"_embedded":{"instalments":[{"prfId":"MDDP12000117","title":"Episode One"}]},"_links":{"next":{"href":"/tv/catalog/series/fixture?page=2"}}}`)},
		"GET " + secondURL: {body: []byte(`{"_embedded":{"instalments":[{"episodeId":"MDDP12000217","title":"Episode Two"}]}}`)},
	}}
	result, err := NewNRK().Extract(context.Background(), Request{URL: "https://tv.nrk.no/serie/fixture", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || len(transport.requests) != 1 {
		t.Fatalf("playlist=%t requests=%d", result.IsPlaylist(), len(transport.requests))
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 || entries[1].ID != "MDDP12000217" {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests after iteration = %d", len(transport.requests))
	}
	if _, _, _, _, err := parseNRKCatalog(map[string]any{"_links": map[string]any{"next": map[string]any{"href": "https://evil.example.test/steal"}}}); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("foreign cursor error = %v", err)
	}
}

func TestNRKFailureCategoriesCancellationAndSecretSafety(t *testing.T) {
	manifestURL := nrkAPIBase + "playback/manifest/program/MDDP12000117?preferredCdn=akamai"
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"auth-status", http.StatusUnauthorized, `{}`, ErrAuthentication},
		{"geo-status", http.StatusForbidden, `{}`, ErrRegionRestricted},
		{"unavailable-status", http.StatusNotFound, `{}`, ErrUnavailable},
		{"geo-body", http.StatusOK, `{"playability":"nonPlayable","nonPlayable":{"messageType":"ProgramIsGeoBlocked","endUserMessage":"utenfor Norge"}}`, ErrRegionRestricted},
		{"auth-body", http.StatusOK, `{"playability":"nonPlayable","nonPlayable":{"messageType":"LoginRequired"}}`, ErrAuthentication},
		{"expired-body", http.StatusOK, `{"playability":"nonPlayable","nonPlayable":{"messageType":"ProgramRightsHasExpired"}}`, ErrUnavailable},
		{"no-assets", http.StatusOK, `{"playability":"playable","playable":{"assets":[]}}`, ErrUnavailable},
		{"malformed", http.StatusOK, `{"secret":"nrk-private-token"} trailing`, ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
				"GET " + manifestURL: {status: test.status, body: []byte(test.body)},
			}}
			_, err := NewNRK().Extract(context.Background(), Request{URL: "https://tv.nrk.no/program/MDDP12000117", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "nrk-private-token") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewNRK().Extract(ctx, Request{URL: "https://tv.nrk.no/program/MDDP12000117", Transport: &riskFixtureTransport{wait: true}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func FuzzParseNRKCatalog(f *testing.F) {
	f.Add([]byte(`{"titles":{"title":"Fixture"},"_embedded":{"instalments":[{"prfId":"MDDP12000117"}]}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var root any
		if json.Unmarshal(data, &root) == nil {
			_, _, _, _, _ = parseNRKCatalog(root)
		}
	})
}

func FuzzParseNRKDuration(f *testing.F) {
	f.Add("PT37M3.44S")
	f.Add("2223.44")
	f.Add("not-duration")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) <= 1024 {
			_ = parseNRKDuration(input)
		}
	})
}
