package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestSoundCloudPrivateSetHydratesWebAndAPIURLs(t *testing.T) {
	for _, rawURL := range []string{
		"https://soundcloud.com/fixture-artist/sets/private-set/s-private-fixture",
		"https://api.soundcloud.com/playlists/77?secret_token=s-private-fixture",
	} {
		t.Run(rawURL, func(t *testing.T) {
			transport := newSoundCloudFixtureTransport(t)
			transport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path != "/tracks" {
					return 0, nil, false
				}
				if request.Method != http.MethodGet || request.URL.Scheme != "https" ||
					request.URL.Host != "api-v2.soundcloud.com" ||
					request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
					t.Fatalf("unsafe hydration request: %s %#v", request.URL, request.Header)
				}
				query := request.URL.Query()
				if query.Get("ids") != "102,100,101" || query.Get("playlistId") != "77" ||
					query.Get("playlistSecretToken") != "s-private-fixture" ||
					query.Get("client_id") != soundCloudFixtureClientID || len(query) != 4 {
					t.Fatalf("hydration query=%#v", query)
				}
				return http.StatusOK, transport.fixture["private_tracks_batch.json"], true
			}
			result, err := NewSoundCloud().Extract(context.Background(), Request{
				URL: rawURL, Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			for iteration := 0; iteration < 2; iteration++ {
				entries, err := CollectEntries(context.Background(), result.Entries, 10)
				if err != nil {
					t.Fatal(err)
				}
				if got := soundCloudEntryIDs(entries); !reflect.DeepEqual(got, []string{"102", "100", "102", "101"}) {
					t.Fatalf("entry IDs=%#v", got)
				}
				if entries[0].URL != "https://api-v2.soundcloud.com/tracks/102?secret_token=s-private-fixture" ||
					entries[0].Title != "Hydrated Private Without Permalink" ||
					entries[1].URL != "https://soundcloud.com/fixture-artist/hydrated-public" ||
					entries[3].URL != "https://soundcloud.com/fixture-artist/hydrated-one" {
					t.Fatalf("entries=%#v", entries)
				}
				for _, entry := range entries {
					if !entry.Transparent || entry.ExtractorKey != "soundcloud" {
						t.Fatalf("non-transparent entry=%#v", entry)
					}
				}
			}
			if transport.requestCount("/tracks") != 1 {
				t.Fatalf("batch requests=%d", transport.requestCount("/tracks"))
			}
			if id, _ := result.Info.Lookup("id").StringValue(); id != "77" {
				t.Fatalf("playlist info=%#v", result.Info)
			}
		})
	}
}

func TestSoundCloudPrivateSetTriggerAndMissingRowFallback(t *testing.T) {
	t.Run("public placeholder does not hydrate", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://api.soundcloud.com/playlists/77", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil {
			t.Fatal(err)
		}
		if transport.requestCount("/tracks") != 0 ||
			entries[0].URL != "https://api-v2.soundcloud.com/tracks/102" {
			t.Fatalf("requests=%d entries=%#v", transport.requestCount("/tracks"), entries)
		}
	})
	t.Run("complete tokenized rows do not hydrate", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/playlists/77" {
				return http.StatusOK, []byte(`{"id":77,"title":"Complete","tracks":[
					{"id":1,"permalink_url":"https://soundcloud.com/a/one"},
					{"id":2,"permalink_url":"https://soundcloud.com/a/two"}]}`), true
			}
			return 0, nil, false
		}
		if _, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://api.soundcloud.com/playlists/77?secret_token=s-private-fixture", Transport: transport,
		}); err != nil {
			t.Fatal(err)
		}
		if transport.requestCount("/tracks") != 0 {
			t.Fatalf("batch requests=%d", transport.requestCount("/tracks"))
		}
	})
	t.Run("missing API rows retain tokenized fallback", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/tracks" {
				return http.StatusOK, []byte(`[{"id":100,"title":"Only returned row","permalink_url":"https://soundcloud.com/a/returned"}]`), true
			}
			return 0, nil, false
		}
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://api.soundcloud.com/playlists/77?secret_token=s-private-fixture", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil {
			t.Fatal(err)
		}
		if entries[0].URL != "https://api-v2.soundcloud.com/tracks/102?secret_token=s-private-fixture" ||
			entries[1].Title != "Only returned row" ||
			entries[3].URL != "https://api-v2.soundcloud.com/tracks/101?secret_token=s-private-fixture" {
			t.Fatalf("entries=%#v", entries)
		}
	})
}

func TestSoundCloudPrivateSetHydrationFailuresAreCategorizedAndSecretSafe(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"malformed", http.StatusOK, []byte(`{`), ErrInvalidMetadata},
		{"wrong shape", http.StatusOK, []byte(`{"id":1}`), ErrInvalidMetadata},
		{"null shape", http.StatusOK, []byte(`null`), ErrInvalidMetadata},
		{"unrequested", http.StatusOK, []byte(`[{"id":999}]`), ErrInvalidMetadata},
		{"duplicate response", http.StatusOK, []byte(`[{"id":100},{"id":100}]`), ErrInvalidMetadata},
		{"authentication", http.StatusForbidden, nil, ErrAuthentication},
		{"unavailable", http.StatusNotFound, nil, ErrUnavailable},
		{"rate limited", http.StatusTooManyRequests, nil, ErrSoundCloudPrivateSetRateLimited},
		{"server", http.StatusInternalServerError, nil, ErrSoundCloudPrivateSetNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudFixtureTransport(t)
			transport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path == "/tracks" {
					return test.status, test.body, true
				}
				return 0, nil, false
			}
			_, err := NewSoundCloud().Extract(context.Background(), Request{
				URL:       "https://api.soundcloud.com/playlists/77?secret_token=s-top-secret-fixture",
				Transport: transport,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			assertPrivateSetErrorSafe(t, err)
		})
	}

	base := newSoundCloudFixtureTransport(t)
	transport := soundCloudPrivateSetFailureTransport{base: base}
	_, err := NewSoundCloud().Extract(context.Background(), Request{
		URL:       "https://api.soundcloud.com/playlists/77?secret_token=s-top-secret-fixture",
		Transport: transport,
	})
	if !errors.Is(err, ErrSoundCloudPrivateSetNetwork) {
		t.Fatalf("network error=%v", err)
	}
	assertPrivateSetErrorSafe(t, err)
}

func TestSoundCloudPrivateSetRejectsMalformedSourceBeforeBatch(t *testing.T) {
	for _, id := range []json.Number{"", "0", "-1", "01", "18446744073709551616"} {
		t.Run(id.String(), func(t *testing.T) {
			extractor := NewSoundCloud()
			extractor.clientID = soundCloudFixtureClientID
			transport := &soundCloudPrivateSetBatchTransport{t: t}
			_, err := extractor.hydrateSoundCloudPrivateSet(context.Background(), transport, "77", "s-private", []soundCloudTrack{
				{ID: id},
			})
			if !errors.Is(err, ErrInvalidMetadata) || len(transport.requests) != 0 {
				t.Fatalf("error=%v requests=%d", err, len(transport.requests))
			}
		})
	}
	if _, err := soundCloudPrivateSetBatches("01", "s-private", []string{"1"}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("non-canonical playlist ID error=%v", err)
	}
}

func TestSoundCloudPrivateSetBatchingOrderAndCancellation(t *testing.T) {
	tracks := make([]soundCloudTrack, 201)
	for index := range tracks {
		tracks[index].ID = json.Number(strconv.Itoa(index + 1))
	}
	extractor := NewSoundCloud()
	extractor.clientID = soundCloudFixtureClientID
	transport := &soundCloudPrivateSetBatchTransport{t: t}
	hydrated, err := extractor.hydrateSoundCloudPrivateSet(
		context.Background(), transport, "77", "s-private", tracks,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 2 || len(hydrated) != len(tracks) {
		t.Fatalf("requests=%d hydrated=%d", len(transport.requests), len(hydrated))
	}
	for index := range hydrated {
		if hydrated[index].ID.String() != strconv.Itoa(index+1) ||
			hydrated[index].Title != "Hydrated "+strconv.Itoa(index+1) {
			t.Fatalf("hydrated[%d]=%#v", index, hydrated[index])
		}
	}
	for index, request := range transport.requests {
		count := len(strings.Split(request.Query().Get("ids"), ","))
		want := soundCloudPrivateSetBatchIDs
		if index == 1 {
			want = 1
		}
		if count != want || len(request.String()) > soundCloudMaxURLBytes {
			t.Fatalf("request[%d] count=%d bytes=%d", index, count, len(request.String()))
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelTransport := &soundCloudPrivateSetBatchTransport{t: t, cancel: cancel}
	_, err = extractor.hydrateSoundCloudPrivateSet(ctx, cancelTransport, "77", "s-private", tracks)
	if !errors.Is(err, context.Canceled) || len(cancelTransport.requests) != 1 {
		t.Fatalf("cancellation error=%v requests=%d", err, len(cancelTransport.requests))
	}
}

func FuzzSoundCloudPrivateSetBatchPlan(f *testing.F) {
	f.Add("77", "s-private", "1,2,3")
	f.Add("0", "invalid", "")
	f.Add("18446744073709551615", "s-token_1", "9")
	f.Fuzz(func(t *testing.T, playlistID, token, rawIDs string) {
		if len(playlistID)+len(token)+len(rawIDs) > 32<<10 {
			t.Skip()
		}
		ids := strings.Split(rawIDs, ",")
		requests, err := soundCloudPrivateSetBatches(playlistID, token, ids)
		if err != nil {
			return
		}
		again, err := soundCloudPrivateSetBatches(playlistID, token, ids)
		if err != nil || !reflect.DeepEqual(requests, again) {
			t.Fatalf("non-deterministic plan %#v %#v error=%v", requests, again, err)
		}
		var planned []string
		for _, rawURL := range requests {
			if len(rawURL) > soundCloudMaxURLBytes-soundCloudPrivateSetURLRoom {
				t.Fatalf("oversized URL")
			}
			parsed, err := url.Parse(rawURL)
			if err != nil || parsed.Scheme != "https" || parsed.Host != "api-v2.soundcloud.com" ||
				parsed.Path != "/tracks" || parsed.User != nil || parsed.Port() != "" ||
				parsed.Fragment != "" {
				t.Fatalf("unsafe URL=%q error=%v", rawURL, err)
			}
			query := parsed.Query()
			if len(query) != 3 || query.Get("playlistId") != playlistID ||
				query.Get("playlistSecretToken") != token {
				t.Fatalf("query=%#v", query)
			}
			planned = append(planned, strings.Split(query.Get("ids"), ",")...)
		}
		if !reflect.DeepEqual(planned, ids) {
			t.Fatalf("planned=%#v ids=%#v", planned, ids)
		}
	})
}

type soundCloudPrivateSetFailureTransport struct {
	base *soundCloudFixtureTransport
}

func (transport soundCloudPrivateSetFailureTransport) Do(
	ctx context.Context, request *http.Request,
) (*http.Response, error) {
	if request.URL.Path == "/tracks" {
		return nil, errors.New("offline playlistSecretToken=s-top-secret-fixture client_id=secret")
	}
	return transport.base.Do(ctx, request)
}

func (transport soundCloudPrivateSetFailureTransport) ReadPage(
	ctx context.Context, rawURL string,
) ([]byte, http.Header, error) {
	return transport.base.ReadPage(ctx, rawURL)
}

type soundCloudPrivateSetBatchTransport struct {
	t      *testing.T
	cancel context.CancelFunc

	mu       sync.Mutex
	requests []*url.URL
}

func (transport *soundCloudPrivateSetBatchTransport) Do(
	ctx context.Context, request *http.Request,
) (*http.Response, error) {
	if request.URL.Path != "/tracks" || request.URL.Host != "api-v2.soundcloud.com" ||
		request.URL.Query().Get("client_id") != soundCloudFixtureClientID {
		transport.t.Fatalf("unexpected request=%s", request.URL)
	}
	transport.mu.Lock()
	copyURL := *request.URL
	transport.requests = append(transport.requests, &copyURL)
	requestNumber := len(transport.requests)
	transport.mu.Unlock()
	ids := strings.Split(request.URL.Query().Get("ids"), ",")
	response := make([]soundCloudTrack, 0, len(ids))
	for index := len(ids) - 1; index >= 0; index-- {
		response = append(response, soundCloudTrack{
			ID:    json.Number(ids[index]),
			Title: "Hydrated " + ids[index],
		})
	}
	body, err := json.Marshal(response)
	if err != nil {
		transport.t.Fatal(err)
	}
	if transport.cancel != nil && requestNumber == 1 {
		transport.cancel()
	}
	return soundCloudResponse(http.StatusOK, body), nil
}

func (*soundCloudPrivateSetBatchTransport) ReadPage(
	context.Context, string,
) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page request")
}

func soundCloudEntryIDs(entries []Entry) []string {
	ids := make([]string, len(entries))
	for index := range entries {
		ids[index] = entries[index].ID
	}
	return ids
}

func assertPrivateSetErrorSafe(t *testing.T, err error) {
	t.Helper()
	message := fmt.Sprint(err)
	for _, secret := range []string{
		"top-secret-fixture", "playlistSecretToken", "client_id", "signed.example",
	} {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}
