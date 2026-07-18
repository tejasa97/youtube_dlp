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

func TestARDRoutingItemFormatsAndLiveState(t *testing.T) {
	itemID := "Y3JpZDovL2ZpeHR1cmU"
	for _, rawURL := range []string{
		"https://www.ardmediathek.de/video/title/channel/" + itemID,
		"https://ardmediathek.de/player/" + itemID,
		"https://beta.ardmediathek.de/live/" + itemID,
		"https://www.ardmediathek.de/sendung/title/" + itemID,
		"https://www.ardmediathek.de/serie/title/staffel-1/" + itemID + "/1/OV",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewARD().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{"https://example.com/video/id", "https://ardmediathek.de/other/id", "ftp://ardmediathek.de/video/id"} {
		parsed, _ := url.Parse(rawURL)
		if NewARD().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
	pageURL := "https://www.ardmediathek.de/video/title/channel/" + itemID
	endpoint := ardPageGatewayBase + "pages/ard/item/" + itemID + "?embedded=false&mcV6=true"
	transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
		"GET " + endpoint: {body: readRiskFixture(t, "ard", "item.json")},
	}}
	result, err := NewARD().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	assertRiskString(t, result, "id", "12939099")
	assertRiskString(t, result, "title", "Fixture ARD Item")
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
	liveBody := strings.Replace(string(readRiskFixture(t, "ard", "item.json")), "player_ondemand", "player_live", 1)
	transport.responses["GET "+endpoint] = riskFixtureResponse{body: []byte(liveBody)}
	live, err := NewARD().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if isLive, ok := live.Info.Lookup("is_live").Bool(); !ok || !isLive {
		t.Fatalf("is_live = %t, %t", isLive, ok)
	}
}

func TestARDPlaylistIsLazy(t *testing.T) {
	itemID := "Y3JpZDovL2ZpeHR1cmU"
	transport := &riskFixtureTransport{handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("pageSize") == "1" {
			return riskHTTPResponse(http.StatusOK, []byte(`{"title":"Fixture Collection","synopsis":"Synthetic collection","teasers":[]}`)), nil
		}
		if request.URL.Query().Get("pageNumber") != "0" || request.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("playlist query = %s", request.URL.RawQuery)
		}
		return riskHTTPResponse(http.StatusOK, []byte(`{"teasers":[{"id":"asset-1","type":"video","longTitle":"Episode One","links":{"target":{"urlId":"EpisodeAsset1"}}},{"id":"collection-2","type":"compilation","longTitle":"Nested","links":{"target":{"urlId":"CollectionAsset2"}}}]}`)), nil
	}}
	result, err := NewARD().Extract(context.Background(), Request{URL: "https://www.ardmediathek.de/sendung/fixture/" + itemID, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || len(transport.requests) != 1 {
		t.Fatalf("playlist=%t requests=%d", result.IsPlaylist(), len(transport.requests))
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "ard" {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests after iteration = %d", len(transport.requests))
	}
}

func TestARDFailureCategoriesCancellationAndSecretSafety(t *testing.T) {
	itemID := "FixtureItem"
	pageURL := "https://www.ardmediathek.de/video/" + itemID
	endpoint := ardPageGatewayBase + "pages/ard/item/" + itemID + "?embedded=false&mcV6=true"
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"auth-status", http.StatusUnauthorized, `{}`, ErrAuthentication},
		{"geo-status", http.StatusUnavailableForLegalReasons, `{}`, ErrRegionRestricted},
		{"unavailable-status", http.StatusNotFound, `{}`, ErrUnavailable},
		{"geo-body", http.StatusOK, `{"geoBlocked":true}`, ErrRegionRestricted},
		{"age-auth", http.StatusOK, `{"widgets":[{"type":"player_ondemand","blockedByFsk":true}]}`, ErrAuthentication},
		{"no-player", http.StatusOK, `{"title":"No player"}`, ErrUnavailable},
		{"malformed", http.StatusOK, `{"secret":"ard-private-token"} trailing`, ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &riskFixtureTransport{responses: map[string]riskFixtureResponse{
				"GET " + endpoint: {status: test.status, body: []byte(test.body)},
			}}
			_, err := NewARD().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
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
	if _, err := NewARD().Extract(ctx, Request{URL: pageURL, Transport: &riskFixtureTransport{wait: true}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func FuzzNormalizeARDMedia(f *testing.F) {
	f.Add(readRiskFixture(f, "ard", "item.json"))
	f.Add([]byte(`{"widgets":[]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var page ardPageData
		if json.Unmarshal(data, &page) != nil {
			return
		}
		for _, widget := range page.Widgets {
			_, _ = normalizeARDMedia(widget.MediaCollection.Embedded)
		}
	})
}
