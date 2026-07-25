package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
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

func TestSoundCloudAPIUserPermalinkIsLazyOrderedAndReusable(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://api.soundcloud.com/users/7", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSoundCloudString(t, result, "id", "7")
	assertSoundCloudString(t, result, "title", "Fixture Artist")
	assertSoundCloudString(t, result, "uploader", "Fixture Artist")
	assertSoundCloudString(t, result, "webpage_url", "https://api.soundcloud.com/users/7")
	if transport.requestCount("/users/7/tracks") != 0 {
		t.Fatal("API user tracks fetched eagerly")
	}
	var foundExactResolve bool
	for _, rawURL := range transport.snapshotRequests() {
		parsed, parseErr := url.Parse(rawURL)
		if parseErr == nil && parsed.Path == "/resolve" &&
			parsed.Query().Get("url") == "https://api.soundcloud.com/users/7" {
			foundExactResolve = true
		}
	}
	if !foundExactResolve {
		t.Fatalf("exact API user resolve missing: %v", transport.snapshotRequests())
	}
	for iteration := 0; iteration < 2; iteration++ {
		entries, collectErr := CollectEntries(context.Background(), result.Entries, 10)
		if collectErr != nil {
			t.Fatal(collectErr)
		}
		if len(entries) != 3 || entries[0].ID != "100" || entries[1].ID != "101" || entries[2].ID != "102" {
			t.Fatalf("iteration %d entries = %#v", iteration, entries)
		}
	}
	if got := transport.requestCount("/users/7/tracks"); got != 4 {
		t.Fatalf("track page requests = %d, want 4", got)
	}
}

func TestSoundCloudAPIUserPermalinkRejectsUnsafeRoutesWithoutRequests(t *testing.T) {
	for _, rawURL := range []string{
		"http://api.soundcloud.com/users/7",
		"https://user:secret@api.soundcloud.com/users/7",
		"https://api.soundcloud.com:443/users/7",
		"https://api.soundcloud.com.evil.example/users/7",
		"https://api-v2.soundcloud.com/users/7",
		"https://api.soundcloud.com/users/7/",
		"https://api.soundcloud.com/users/7/extra",
		"https://api.soundcloud.com/users/0",
		"https://api.soundcloud.com/users/not-numeric",
		"https://api.soundcloud.com/users/%37",
		"https://api.soundcloud.com/users/soundcloud%3Ausers%3A7",
		"https://api.soundcloud.com/users/18446744073709551616",
		"https://api.soundcloud.com/users/7?client_id=secret",
		"https://api.soundcloud.com/users/7?",
		"https://api.soundcloud.com/users/7#fragment",
		"https://api.soundcloud.com/users%2f7",
	} {
		t.Run(rawURL, func(t *testing.T) {
			parsed, _ := url.Parse(rawURL)
			if NewSoundCloud().Suitable(parsed) {
				t.Fatal("Suitable = true")
			}
			transport := newSoundCloudFixtureTransport(t)
			_, err := NewSoundCloud().Extract(context.Background(), Request{
				URL: rawURL, Transport: transport,
			})
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Extract error = %v, want ErrUnsupported", err)
			}
			if requests := transport.snapshotRequests(); len(requests) != 0 {
				t.Fatalf("rejected route made requests: %v", requests)
			}
		})
	}
}

func TestSoundCloudAPIUserPermalinkRejectsMismatchedResolve(t *testing.T) {
	for _, body := range []string{
		`{"id":8,"username":"Other User"}`,
		`{"id":7,"username":""}`,
		`{"id":0,"username":"Fixture Artist"}`,
	} {
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/resolve" {
				return http.StatusOK, []byte(body), true
			}
			return 0, nil, false
		}
		_, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://api.soundcloud.com/users/7", Transport: transport,
		})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("body %s error = %v, want ErrInvalidMetadata", body, err)
		}
		if transport.requestCount("/users/7/tracks") != 0 {
			t.Fatalf("body %s fetched tracks", body)
		}
	}
}

func TestSoundCloudAPIUserPermalinkComparesNumericIdentity(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://api.soundcloud.com/users/007", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSoundCloudString(t, result, "id", "7")
	assertSoundCloudString(t, result, "webpage_url", "https://api.soundcloud.com/users/007")
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries = %#v, error = %v", entries, err)
	}
	if transport.requestCount("/users/7/tracks") != 2 {
		t.Fatalf("resolved track path requests = %d", transport.requestCount("/users/7/tracks"))
	}
}

func TestSoundCloudAPIUserPermalinkCancellationAndContinuationIsolation(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		transport.blockUserPage = true
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://api.soundcloud.com/users/7", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, _, nextErr := result.Entries.Iterator().Next(ctx)
			done <- nextErr
		}()
		select {
		case <-transport.pageStarted:
			cancel()
		case <-time.After(time.Second):
			t.Fatal("track page request did not start")
		}
		select {
		case nextErr := <-done:
			if !errors.Is(nextErr, context.Canceled) {
				t.Fatalf("Next error = %v", nextErr)
			}
		case <-time.After(time.Second):
			t.Fatal("track page request did not cancel")
		}
	})

	t.Run("continuation pivot", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/users/7/tracks" {
				return http.StatusOK, []byte(`{"collection":[],"next_href":"https://api-v2.soundcloud.com/users/8/tracks?cursor=secret"}`), true
			}
			return 0, nil, false
		}
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://api.soundcloud.com/users/7", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CollectEntries(context.Background(), result.Entries, 4); !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("pivot error = %v, want ErrInvalidPlaylist", err)
		}
		if transport.requestCount("/users/8/tracks") != 0 {
			t.Fatal("cross-user continuation was requested")
		}
	})
}
