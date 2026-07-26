package extractor

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSoundCloudCollectionStartsWithPinnedOffsetContract(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		wantPath string
	}{
		{
			name:     "profile tab",
			rawURL:   "https://soundcloud.com/fixture-artist/tracks",
			wantPath: "/users/7/tracks",
		},
		{
			name:     "legacy API user",
			rawURL:   "https://api.soundcloud.com/users/7",
			wantPath: "/users/7/tracks",
		},
		{
			name:     "track station",
			rawURL:   "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal",
			wantPath: "/stations/soundcloud:track-stations:5000/tracks",
		},
		{
			name:     "recommended",
			rawURL:   "https://soundcloud.com/fixture-artist/related-signal/recommended",
			wantPath: "/tracks/8000/related",
		},
		{
			name:     "albums",
			rawURL:   "https://soundcloud.com/fixture-artist/related-signal/albums",
			wantPath: "/tracks/8000/albums",
		},
		{
			name:     "sets",
			rawURL:   "https://soundcloud.com/fixture-artist/related-signal/sets",
			wantPath: "/tracks/8000/playlists_without_albums",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudFixtureTransport(t)
			extractor := NewSoundCloud()
			extractor.clientID = soundCloudFixtureClientID
			result, err := extractor.Extract(context.Background(), Request{
				URL: test.rawURL, Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := result.Entries.Iterator().Next(context.Background()); err != nil || !ok {
				t.Fatalf("first entry ok=%v error=%v", ok, err)
			}
			requests := soundCloudFixtureRequests(transport, test.wantPath)
			if len(requests) != 1 {
				t.Fatalf("%s requests=%#v", test.wantPath, requests)
			}
			query := requests[0].Query()
			if len(query) != 4 || query.Get("client_id") != soundCloudFixtureClientID ||
				query.Get("limit") != "200" || query.Get("linked_partitioning") != "1" ||
				query.Get("offset") != "0" {
				t.Fatalf("initial query=%#v", query)
			}
		})
	}
}

func TestSoundCloudCollectionContinuationDoesNotReintroduceOffset(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	extractor := NewSoundCloud()
	extractor.clientID = soundCloudFixtureClientID
	result, err := extractor.Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	for index := 0; index < 3; index++ {
		if _, ok, err := iterator.Next(context.Background()); err != nil || !ok {
			t.Fatalf("entry %d ok=%v error=%v", index, ok, err)
		}
	}
	requests := soundCloudFixtureRequests(transport, "/users/7/tracks")
	if len(requests) != 2 {
		t.Fatalf("requests=%#v", requests)
	}
	initial, continuation := requests[0].Query(), requests[1].Query()
	if initial.Get("offset") != "0" {
		t.Fatalf("initial query=%#v", initial)
	}
	if _, exists := continuation["offset"]; exists || continuation.Get("cursor") != "page2" ||
		continuation.Get("limit") != "200" || continuation.Get("client_id") != soundCloudFixtureClientID {
		t.Fatalf("continuation query=%#v", continuation)
	}
}

func TestSoundCloudCollectionPreservesServerContinuationOffset(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path != "/users/7/tracks" {
			return 0, nil, false
		}
		if request.URL.Query().Get("cursor") == "server-page-2" {
			return http.StatusOK, []byte(`{
				"collection":[{"id":2,"permalink_url":"https://soundcloud.com/a/two"}],
				"next_href":null
			}`), true
		}
		return http.StatusOK, []byte(`{
			"collection":[{"id":1,"permalink_url":"https://soundcloud.com/a/one"}],
			"next_href":"https://api-v2.soundcloud.com/users/7/tracks?cursor=server-page-2&limit=200&linked_partitioning=1&offset=200"
		}`), true
	}
	extractor := NewSoundCloud()
	extractor.clientID = soundCloudFixtureClientID
	result, err := extractor.Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	for index := 0; index < 2; index++ {
		if _, ok, err := iterator.Next(context.Background()); err != nil || !ok {
			t.Fatalf("entry %d ok=%v error=%v", index, ok, err)
		}
	}
	requests := soundCloudFixtureRequests(transport, "/users/7/tracks")
	if len(requests) != 2 {
		t.Fatalf("requests=%#v", requests)
	}
	continuation := requests[1].Query()
	if continuation.Get("offset") != "200" || continuation.Get("cursor") != "server-page-2" ||
		continuation.Get("limit") != "200" || continuation.Get("linked_partitioning") != "1" {
		t.Fatalf("continuation query=%#v", continuation)
	}
}

func TestSoundCloudCollectionStartURLIsDeterministicAndBounded(t *testing.T) {
	for _, apiPath := range []string{
		"users/7/tracks",
		"stations/soundcloud:track-stations:5000/tracks",
		"tracks/8000/playlists_without_albums",
	} {
		first := soundCloudCollectionStartURL(apiPath)
		second := soundCloudCollectionStartURL(apiPath)
		parsed, err := url.Parse(first)
		if err != nil || first != second || len(first) > soundCloudMaxURLBytes ||
			parsed.Scheme != "https" || parsed.Host != "api-v2.soundcloud.com" ||
			parsed.Path != "/"+apiPath || parsed.User != nil || parsed.Port() != "" ||
			parsed.Fragment != "" {
			t.Fatalf("apiPath=%q URL=%q error=%v", apiPath, first, err)
		}
		query := parsed.Query()
		if len(query) != 3 || query.Get("limit") != "200" ||
			query.Get("linked_partitioning") != "1" || query.Get("offset") != "0" {
			t.Fatalf("apiPath=%q query=%#v", apiPath, query)
		}
	}
}

func soundCloudFixtureRequests(transport *soundCloudFixtureTransport, wantPath string) []*url.URL {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	requests := make([]*url.URL, 0, len(transport.requests))
	for _, rawURL := range transport.requests {
		parsed, err := url.Parse(rawURL)
		if err == nil && parsed.Path == wantPath && strings.EqualFold(parsed.Hostname(), "api-v2.soundcloud.com") {
			requests = append(requests, parsed)
		}
	}
	return requests
}
