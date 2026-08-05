package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/network"
)

type soundCloudProfileFixtureTransport struct {
	inner              *soundCloudFixtureTransport
	unavailableProfile bool

	mu              sync.Mutex
	profileRequests []soundCloudProfileRequest
}

type soundCloudProfileRequest struct {
	profile string
	path    string
}

func newSoundCloudProfileFixtureTransport(t *testing.T) *soundCloudProfileFixtureTransport {
	t.Helper()
	return &soundCloudProfileFixtureTransport{inner: newSoundCloudFixtureTransport(t)}
}

func (transport *soundCloudProfileFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.inner.Do(ctx, request)
}

func (transport *soundCloudProfileFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	return transport.inner.ReadPage(ctx, rawURL)
}

func (transport *soundCloudProfileFixtureTransport) DoProfile(ctx context.Context, request *http.Request, profile string) (*http.Response, error) {
	transport.mu.Lock()
	transport.profileRequests = append(transport.profileRequests, soundCloudProfileRequest{
		profile: profile,
		path:    request.URL.Path,
	})
	unavailable := transport.unavailableProfile
	transport.mu.Unlock()
	if unavailable {
		return nil, fmt.Errorf("%w: %s", network.ErrImpersonationUnavailable, profile)
	}
	return transport.inner.Do(ctx, request)
}

func (transport *soundCloudProfileFixtureTransport) ReadPageProfile(context.Context, string, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPageProfile call")
}

func (transport *soundCloudProfileFixtureTransport) profilePaths() []string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	paths := make([]string, len(transport.profileRequests))
	for i, request := range transport.profileRequests {
		paths[i] = request.path
	}
	return paths
}

func (transport *soundCloudProfileFixtureTransport) profilesUsed() []string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	profiles := make([]string, len(transport.profileRequests))
	for i, request := range transport.profileRequests {
		profiles[i] = request.profile
	}
	return profiles
}

func soundCloudIsCollectionPath(path string) bool {
	switch path {
	case "/users/7/tracks", "/users/7/likes",
		"/stations/soundcloud:track-stations:5000/tracks",
		"/tracks/8000/related", "/tracks/8000/albums", "/tracks/8000/playlists_without_albums":
		return true
	default:
		return false
	}
}

func soundCloudExtractFirstCollectionEntry(t *testing.T, rawURL string, transport Transport) {
	t.Helper()
	result, err := NewSoundCloud().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := result.Entries.Iterator().Next(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSoundCloudPagedCollectionProfileSelection(t *testing.T) {
	cases := []struct {
		name         string
		url          string
		wantProfiles []string
	}{
		{name: "profile tab tracks", url: "https://soundcloud.com/fixture-artist/tracks", wantProfiles: []string{"/users/7/tracks"}},
		{name: "legacy api user", url: "https://api.soundcloud.com/users/7", wantProfiles: []string{"/users/7/tracks"}},
		{name: "station", url: "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", wantProfiles: []string{"/stations/soundcloud:track-stations:5000/tracks"}},
		{name: "recommended", url: "https://soundcloud.com/fixture-artist/related-signal/recommended", wantProfiles: []string{"/tracks/8000/related"}},
		{name: "albums", url: "https://soundcloud.com/fixture-artist/related-signal/albums", wantProfiles: []string{"/tracks/8000/albums"}},
		{name: "sets", url: "https://soundcloud.com/fixture-artist/related-signal/sets", wantProfiles: []string{"/tracks/8000/playlists_without_albums"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudProfileFixtureTransport(t)
			soundCloudExtractFirstCollectionEntry(t, test.url, transport)
			profiles := transport.profilesUsed()
			if len(profiles) != len(test.wantProfiles) {
				t.Fatalf("profile calls = %d, want %d (%v)", len(profiles), len(test.wantProfiles), profiles)
			}
			for i, want := range test.wantProfiles {
				if profiles[i] != soundCloudCollectionProfile {
					t.Fatalf("profile[%d] = %q, want %q", i, profiles[i], soundCloudCollectionProfile)
				}
				if got := transport.profilePaths()[i]; got != want {
					t.Fatalf("profile path[%d] = %q, want %q", i, got, want)
				}
			}
		})
	}

	t.Run("resolve and client-id discovery remain native", func(t *testing.T) {
		transport := newSoundCloudProfileFixtureTransport(t)
		_, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://soundcloud.com/artist/track", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(transport.profileRequests) != 0 {
			t.Fatalf("non-collection extract used profile: %#v", transport.profileRequests)
		}
		for _, rawURL := range transport.inner.snapshotRequests() {
			if strings.Contains(rawURL, "soundcloud.com/") && !strings.Contains(rawURL, "api-v2.soundcloud.com") {
				continue
			}
			if strings.Contains(rawURL, "api-v2.soundcloud.com/resolve") ||
				strings.Contains(rawURL, "api-v2.soundcloud.com/tracks/") ||
				strings.Contains(rawURL, "api-v2.soundcloud.com/media/") {
				continue
			}
			if soundCloudIsCollectionPath(requestPathFromURL(rawURL)) {
				t.Fatalf("unexpected collection request without profile: %s", rawURL)
			}
		}
	})
}

func TestSoundCloudPagedCollectionMissingProfileCapability(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	soundCloudExtractFirstCollectionEntry(t, "https://soundcloud.com/fixture-artist/tracks", transport)
	if transport.requestCount("/users/7/tracks") != 1 {
		t.Fatalf("native collection requests = %d, want 1", transport.requestCount("/users/7/tracks"))
	}
}

func TestSoundCloudPagedCollectionUnavailableProfileFallsBackOnce(t *testing.T) {
	transport := newSoundCloudProfileFixtureTransport(t)
	transport.unavailableProfile = true
	soundCloudExtractFirstCollectionEntry(t, "https://soundcloud.com/fixture-artist/tracks", transport)
	if transport.inner.requestCount("/users/7/tracks") != 1 {
		t.Fatalf("native fallback requests = %d, want 1", transport.inner.requestCount("/users/7/tracks"))
	}
	if len(transport.profileRequests) != 1 || transport.profileRequests[0].profile != soundCloudCollectionProfile {
		t.Fatalf("profile attempts = %#v", transport.profileRequests)
	}
}

func TestSoundCloudPagedCollection502Recovery(t *testing.T) {
	t.Run("success after one 502", func(t *testing.T) {
		attempts := 0
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/users/7/tracks" {
				attempts++
				if attempts == 1 {
					return http.StatusBadGateway, nil, true
				}
			}
			return 0, nil, false
		}
		soundCloudExtractFirstCollectionEntry(t, "https://soundcloud.com/fixture-artist/tracks", transport)
		if attempts != 2 {
			t.Fatalf("page attempts = %d, want 2", attempts)
		}
	})
	t.Run("success after three 502 responses", func(t *testing.T) {
		attempts := 0
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/users/7/tracks" {
				attempts++
				if attempts <= 3 {
					return http.StatusBadGateway, nil, true
				}
			}
			return 0, nil, false
		}
		soundCloudExtractFirstCollectionEntry(t, "https://soundcloud.com/fixture-artist/tracks", transport)
		if attempts != 4 {
			t.Fatalf("page attempts = %d, want 4", attempts)
		}
	})
}

func TestSoundCloudPagedCollection502RetriesExhausted(t *testing.T) {
	attempts := 0
	transport := newSoundCloudFixtureTransport(t)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/users/7/tracks" {
			attempts++
			return http.StatusBadGateway, nil, true
		}
		return 0, nil, false
	}
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = result.Entries.Iterator().Next(context.Background())
	var status *HTTPStatusError
	if !errors.As(err, &status) || status.Code != http.StatusBadGateway {
		t.Fatalf("Next() error = %v, want HTTP 502", err)
	}
	if attempts != soundCloudCollection502MaxAttempts {
		t.Fatalf("page attempts = %d, want %d", attempts, soundCloudCollection502MaxAttempts)
	}
}

func TestSoundCloudPagedCollectionNonRetryFailures(t *testing.T) {
	cases := []struct {
		name                string
		status              int
		body                []byte
		want                error
		attempt             int
		oversizedCollection bool
	}{
		{name: "http 500", status: http.StatusInternalServerError, want: &HTTPStatusError{Code: http.StatusInternalServerError}, attempt: 1},
		{name: "http 503", status: http.StatusServiceUnavailable, want: &HTTPStatusError{Code: http.StatusServiceUnavailable}, attempt: 1},
		{name: "http 429", status: http.StatusTooManyRequests, want: &HTTPStatusError{Code: http.StatusTooManyRequests}, attempt: 1},
		{name: "malformed json", status: http.StatusOK, body: []byte(`{"collection":[`), want: ErrInvalidMetadata, attempt: 1},
		{name: "oversized collection page", status: http.StatusOK, body: nil, want: ErrInvalidPlaylist, attempt: 1, oversizedCollection: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			transport := newSoundCloudFixtureTransport(t)
			transport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path == "/users/7/tracks" {
					attempts++
					if test.oversizedCollection {
						collection := make([]map[string]any, soundCloudMaxPageEntries+1)
						for i := range collection {
							collection[i] = map[string]any{"id": i, "title": "x"}
						}
						body, marshalErr := json.Marshal(map[string]any{"collection": collection})
						if marshalErr != nil {
							t.Fatal(marshalErr)
						}
						return test.status, body, true
					}
					return test.status, test.body, true
				}
				return 0, nil, false
			}
			result, err := NewSoundCloud().Extract(context.Background(), Request{
				URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = result.Entries.Iterator().Next(context.Background())
			switch want := test.want.(type) {
			case *HTTPStatusError:
				var status *HTTPStatusError
				if !errors.As(err, &status) || status.Code != want.Code {
					t.Fatalf("Next() error = %v, want HTTP %d", err, want.Code)
				}
			default:
				if !errors.Is(err, want) {
					t.Fatalf("Next() error = %v, want %v", err, want)
				}
			}
			if attempts != test.attempt {
				t.Fatalf("page attempts = %d, want %d", attempts, test.attempt)
			}
		})
	}
	t.Run("generic transport error", func(t *testing.T) {
		transport := &soundCloudFailingCollectionTransport{inner: newSoundCloudFixtureTransport(t)}
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, errSoundCloudFixtureTransportFailure) {
			t.Fatalf("Next() error = %v", err)
		}
		if transport.attempts != 1 {
			t.Fatalf("transport attempts = %d, want 1", transport.attempts)
		}
	})
}

var errSoundCloudFixtureTransportFailure = errors.New("soundcloud fixture transport failure")

type soundCloudFailingCollectionTransport struct {
	inner    *soundCloudFixtureTransport
	attempts int
}

func (transport *soundCloudFailingCollectionTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if soundCloudIsCollectionPath(request.URL.Path) {
		transport.attempts++
		return nil, errSoundCloudFixtureTransportFailure
	}
	return transport.inner.Do(ctx, request)
}

func (transport *soundCloudFailingCollectionTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	return transport.inner.ReadPage(ctx, rawURL)
}

func (transport *soundCloudFailingCollectionTransport) DoProfile(ctx context.Context, request *http.Request, profile string) (*http.Response, error) {
	return transport.Do(ctx, request)
}

func (transport *soundCloudFailingCollectionTransport) ReadPageProfile(context.Context, string, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPageProfile call")
}

func TestSoundCloudPagedCollectionCancellation(t *testing.T) {
	t.Run("pre-cancelled context performs no collection request", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err = result.Entries.Iterator().Next(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if transport.requestCount("/users/7/tracks") != 0 {
			t.Fatal("canceled iterator still fetched collection page")
		}
	})
	t.Run("cancellation during 502 retries stops further attempts", func(t *testing.T) {
		attempts := 0
		transport := newSoundCloudFixtureTransport(t)
		transport.blockUserPage = true
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/users/7/tracks" && attempts == 0 {
				attempts++
				return http.StatusBadGateway, nil, true
			}
			return 0, nil, false
		}
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
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
			t.Fatal("collection retry did not start")
		}
		select {
		case nextErr := <-done:
			if !errors.Is(nextErr, context.Canceled) {
				t.Fatalf("Next() error = %v", nextErr)
			}
		case <-time.After(time.Second):
			t.Fatal("collection retry did not cancel")
		}
		if attempts != 1 {
			t.Fatalf("retries continued after cancellation: attempts=%d", attempts)
		}
	})
}

func TestSoundCloudPagedCollectionIteratorSafety(t *testing.T) {
	t.Run("retry state is not shared between iterators", func(t *testing.T) {
		for range 2 {
			attempts := 0
			transport := newSoundCloudFixtureTransport(t)
			transport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path == "/users/7/tracks" {
					attempts++
					if attempts == 1 {
						return http.StatusBadGateway, nil, true
					}
				}
				return 0, nil, false
			}
			result, err := NewSoundCloud().Extract(context.Background(), Request{
				URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := result.Entries.Iterator().Next(context.Background()); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 {
				t.Fatalf("page attempts = %d, want 2", attempts)
			}
		}
	})
	t.Run("reused playlist preserves ordering", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		first := collectSoundCloudEntries(t, result.Entries.Iterator())
		second := collectSoundCloudEntries(t, result.Entries.Iterator())
		if len(first) == 0 || len(first) != len(second) {
			t.Fatalf("entry counts = %d and %d", len(first), len(second))
		}
		for i := range first {
			if first[i].URL != second[i].URL || first[i].ID != second[i].ID {
				t.Fatalf("entry[%d] changed between iterators: %#v vs %#v", i, first[i], second[i])
			}
		}
	})
}

func TestSoundCloudPagedCollectionRegression(t *testing.T) {
	transport := newSoundCloudProfileFixtureTransport(t)
	_, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/artist/track", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rawURL := range transport.inner.snapshotRequests() {
		path := requestPathFromURL(rawURL)
		if path == "/resolve" || path == "/tracks/4242" || strings.HasPrefix(path, "/media/") {
			for _, profile := range transport.profileRequests {
				if profile.path == path {
					t.Fatalf("non-collection path %q used profile", path)
				}
			}
		}
	}
}

func collectSoundCloudEntries(t *testing.T, iterator EntryIterator) []Entry {
	t.Helper()
	entries := make([]Entry, 0, 4)
	for {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return entries
		}
		entries = append(entries, entry)
	}
}

func requestPathFromURL(rawURL string) string {
	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return ""
	}
	return request.URL.Path
}
