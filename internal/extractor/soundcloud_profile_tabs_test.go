package extractor

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSoundCloudAllProfileTabsUsePinnedEndpoints(t *testing.T) {
	tests := []struct {
		resource string
		apiPath  string
		label    string
	}{
		{"all", "/stream/users/7", "All"},
		{"tracks", "/users/7/tracks", "Tracks"},
		{"albums", "/users/7/albums", "Albums"},
		{"sets", "/users/7/playlists", "Sets"},
		{"reposts", "/stream/users/7/reposts", "Reposts"},
		{"likes", "/users/7/likes", "Likes"},
		{"spotlight", "/users/7/spotlight", "Spotlight"},
		{"comments", "/users/7/comments", "Comments"},
	}
	for _, test := range tests {
		t.Run(test.resource, func(t *testing.T) {
			transport := newSoundCloudFixtureTransport(t)
			transport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path == test.apiPath {
					return http.StatusOK, transport.fixture["profile_tab_page.json"], true
				}
				return 0, nil, false
			}
			rawURL := "https://soundcloud.com/fixture-artist"
			if test.resource != "all" {
				rawURL += "/" + test.resource
			}
			result, err := NewSoundCloud().Extract(context.Background(), Request{
				URL:       rawURL + "?utm_source=fixture#ignored",
				Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertSoundCloudString(t, result, "id", "7")
			assertSoundCloudString(t, result, "title", "Fixture Artist ("+test.label+")")
			assertSoundCloudString(t, result, "webpage_url", rawURL)
			if transport.requestCount(test.apiPath) != 0 {
				t.Fatal("profile tab fetched eagerly")
			}
			entries, err := CollectEntries(context.Background(), result.Entries, 4)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].ID != "100" ||
				entries[0].URL != "https://soundcloud.com/fixture-artist/profile-item" {
				t.Fatalf("entries = %#v", entries)
			}
			if transport.requestCount(test.apiPath) != 1 {
				t.Fatalf("%s requests = %d", test.apiPath, transport.requestCount(test.apiPath))
			}
			for _, requestURL := range transport.snapshotRequests() {
				parsed, parseErr := url.Parse(requestURL)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				if parsed.Path == test.apiPath && (parsed.Query().Get("utm_source") != "" || parsed.Fragment != "") {
					t.Fatalf("caller query or fragment forwarded: %s", requestURL)
				}
			}
		})
	}
}

func TestSoundCloudProfileTabContinuationCannotPivot(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/users/7/albums" {
			body := `{"collection":[],"next_href":"https://api-v2.soundcloud.com/users/7/playlists?cursor=secret"}`
			return http.StatusOK, []byte(body), true
		}
		return 0, nil, false
	}
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/albums", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEntries(context.Background(), result.Entries, 4); err == nil ||
		!strings.Contains(err.Error(), "invalid SoundCloud continuation") {
		t.Fatalf("pivot error = %v", err)
	}
	if transport.requestCount("/users/7/playlists") != 0 {
		t.Fatal("cross-tab continuation was requested")
	}
}
