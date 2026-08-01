package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

type ardRiskFixtureTransport struct{ *riskFixtureTransport }

func (transport *ardRiskFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	cloned := request.Clone(ctx)
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		cloned.Header.Del(header)
	}
	return transport.Do(ctx, cloned)
}

func TestARDRoutingItemFormatsAndLiveState(t *testing.T) {
	itemID := "Y3JpZDovL2ZpeHR1cmU"
	for _, rawURL := range []string{
		"https://www.ardmediathek.de/video/title/channel/" + itemID,
		"https://ardmediathek.de/player/" + itemID,
		"https://beta.ardmediathek.de/live/" + itemID,
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewARD().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://example.com/video/id",
		"https://ardmediathek.de/other/id",
		"ftp://ardmediathek.de/video/id",
		"https://www.ardmediathek.de/sendung/title/" + itemID,
	} {
		parsed, _ := url.Parse(rawURL)
		if NewARD().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
	pageURL := "https://www.ardmediathek.de/video/title/channel/" + itemID
	endpoint := ardPageGatewayBase + "pages/ard/item/" + itemID + "?embedded=false&mcV6=true"
	transport := &ardRiskFixtureTransport{riskFixtureTransport: &riskFixtureTransport{responses: map[string]riskFixtureResponse{
		"GET " + endpoint: {body: readRiskFixture(t, "ard", "item.json")},
	}}}
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

func TestARDMediathekCollectionRoutingAndNonOverlap(t *testing.T) {
	itemID := "Y3JpZDovL2ZpeHR1cmU"
	for _, rawURL := range []string{
		"https://www.ardmediathek.de/sendung/title/" + itemID,
		"https://www.ardmediathek.de/serie/title/staffel-1/" + itemID + "/1/OV",
		"https://www.ardmediathek.de/sammlung/title/" + itemID,
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewARDMediathekCollection().Suitable(parsed) {
			t.Fatalf("collection Suitable(%q) = false", rawURL)
		}
		if NewARD().Suitable(parsed) {
			t.Fatalf("item extractor overlaps collection URL %q", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://www.ardmediathek.de/video/title/channel/" + itemID,
		"https://www.ardmediathek.de/player/" + itemID,
	} {
		parsed, _ := url.Parse(rawURL)
		if NewARDMediathekCollection().Suitable(parsed) {
			t.Fatalf("collection extractor overlaps item URL %q", rawURL)
		}
	}
}

func TestARDPlaylistIsLazy(t *testing.T) {
	itemID := "Y3JpZDovL2ZpeHR1cmU"
	transport := &ardRiskFixtureTransport{riskFixtureTransport: &riskFixtureTransport{handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("pageSize") == "1" {
			return riskHTTPResponse(http.StatusOK, []byte(`{"title":"Fixture Collection","synopsis":"Synthetic collection","teasers":[]}`)), nil
		}
		if request.URL.Query().Get("pageNumber") != "0" || request.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("playlist query = %s", request.URL.RawQuery)
		}
		return riskHTTPResponse(http.StatusOK, []byte(`{"teasers":[{"id":"asset-1","type":"video","longTitle":"Episode One","links":{"target":{"urlId":"EpisodeAsset1"}}},{"id":"collection-2","type":"compilation","longTitle":"Nested","links":{"target":{"urlId":"CollectionAsset2"}}}]}`)), nil
	}}}
	result, err := NewARDMediathekCollection().Extract(context.Background(), Request{URL: "https://www.ardmediathek.de/sendung/fixture/" + itemID, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || len(transport.requests) != 1 {
		t.Fatalf("playlist=%t requests=%d", result.IsPlaylist(), len(transport.requests))
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "ard" || entries[1].ExtractorKey != "ard_mediathek_collection" {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests after iteration = %d", len(transport.requests))
	}
}

func TestARDPlaylistContinuesAfterFilteredFullPage(t *testing.T) {
	const itemID = "FixtureCollection"
	const pageSize = ardPlaylistPageSize

	teaser := func(id, targetID string) map[string]any {
		return map[string]any{
			"id":        id,
			"type":      "video",
			"longTitle": id,
			"links": map[string]any{
				"target": map[string]any{"urlId": targetID},
			},
		}
	}
	page := func(teasers []map[string]any) []byte {
		body, err := json.Marshal(map[string]any{"title": "Fixture Collection", "teasers": teasers})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	firstPage := make([]map[string]any, pageSize)
	firstPage[0] = teaser("self", itemID)
	for index := 1; index < pageSize; index++ {
		firstPage[index] = teaser("asset-"+strconv.Itoa(index), "Asset"+strconv.Itoa(index))
	}
	secondPage := []map[string]any{teaser("asset-100", "Asset100")}

	transport := &ardRiskFixtureTransport{riskFixtureTransport: &riskFixtureTransport{handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		switch query.Get("pageSize") {
		case "1":
			return riskHTTPResponse(http.StatusOK, page(nil)), nil
		case "100":
			switch query.Get("pageNumber") {
			case "0":
				return riskHTTPResponse(http.StatusOK, page(firstPage)), nil
			case "1":
				return riskHTTPResponse(http.StatusOK, page(secondPage)), nil
			default:
				t.Fatalf("unexpected page number %q", query.Get("pageNumber"))
				return nil, nil
			}
		default:
			t.Fatalf("unexpected page size %q", query.Get("pageSize"))
			return nil, nil
		}
	}}}
	result, err := NewARDMediathekCollection().Extract(context.Background(), Request{
		URL:       "https://www.ardmediathek.de/sendung/fixture/" + itemID,
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(transport.requests); got != 1 {
		t.Fatalf("requests before iteration = %d, want 1", got)
	}

	entries, err := CollectEntries(context.Background(), result.Entries, pageSize)
	if err != nil || len(entries) != pageSize || entries[len(entries)-1].ID != "asset-100" {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
	if got := len(transport.requests); got != 3 {
		t.Fatalf("requests after first iteration = %d, want 3", got)
	}

	entries, err = CollectEntries(context.Background(), result.Entries, pageSize)
	if err != nil || len(entries) != pageSize || entries[len(entries)-1].ID != "asset-100" {
		t.Fatalf("reused entries=%#v error=%v", entries, err)
	}
	if got := len(transport.requests); got != 5 {
		t.Fatalf("requests after reused iteration = %d, want 5", got)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := result.Entries.Iterator().Next(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled iteration error = %v", err)
	}
	if got := len(transport.requests); got != 5 {
		t.Fatalf("requests after cancelled iteration = %d, want 5", got)
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
			transport := &ardRiskFixtureTransport{riskFixtureTransport: &riskFixtureTransport{responses: map[string]riskFixtureResponse{
				"GET " + endpoint: {status: test.status, body: []byte(test.body)},
			}}}
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
	if _, err := NewARD().Extract(ctx, Request{URL: pageURL, Transport: &ardRiskFixtureTransport{riskFixtureTransport: &riskFixtureTransport{wait: true}}}); !errors.Is(err, context.Canceled) {
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
