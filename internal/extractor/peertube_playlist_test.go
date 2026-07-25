package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestPeerTubePlaylistRoutingAndRegistrySeparation(t *testing.T) {
	accepted := []string{
		"https://peertube.example.test/a/fixture-account/videos",
		"https://peertube.example.test/c/fixture-channel/videos",
		"https://peertube.example.test/c/remote@video.blender.org/videos",
		"https://peertube.example.test/w/p/AaBbCcDdEeFfGgHhIiJjKk",
	}
	registry := NewRegistry(NewPeerTubePlaylist(), NewPeerTube())
	for _, rawURL := range accepted {
		parsed, err := url.Parse(rawURL)
		if err != nil || !NewPeerTubePlaylist().Suitable(parsed) {
			t.Errorf("Suitable(%q) = false (parse=%v)", rawURL, err)
			continue
		}
		selected, err := registry.Select(rawURL)
		if err != nil || selected.Name() != "peertube_playlist" {
			t.Errorf("Select(%q) = %v, %v", rawURL, selected, err)
		}
	}
	rejected := []string{
		"https://video.unrecognized.example/a/account/videos",
		"https://peertube.example.test/a/account",
		"https://peertube.example.test//a/account/videos",
		"https://peertube.example.test/a/account/videos/",
		"https://peertube.example.test/c/channel/videos/extra",
		"https://peertube.example.test/w/p/",
		"https://peertube.example.test/w/p/id?token=secret",
		"https://peertube.example.test/w/p/id#fragment",
		"https://user@peertube.example.test/w/p/id",
		"https://peertube.example.test:443/w/p/id",
		"https://peertube.example.test/w/p/encoded%2fid",
		"https://peertube.example.test/w/" + peerTubeFixtureID,
		"peertube:peertube.example.test:" + peerTubeFixtureID,
	}
	for _, rawURL := range rejected {
		parsed, err := url.Parse(rawURL)
		if err == nil && NewPeerTubePlaylist().Suitable(parsed) {
			t.Errorf("Suitable(%q) = true", rawURL)
		}
	}
}

func TestPeerTubePlaylistMetadataLazyPaginationOrderingAndReuse(t *testing.T) {
	targetURL := "https://peertube.example.test/w/p/fixture-playlist"
	base := "https://peertube.example.test/api/v1/video-playlists/fixture-playlist"
	firstPage := makePeerTubePlaylistPage(t, 1, peerTubePlaylistPageSize)
	transport := &peerTubeFixtureTransport{responses: map[string]peerTubeFixtureResponse{
		base: {body: peerTubeFixture(t, "playlist_info.json")},
		base + "/videos?sort=-createdAt&start=0&count=30&nsfw=both": {
			body: firstPage,
		},
		base + "/videos?sort=-createdAt&start=30&count=30&nsfw=both": {
			body: peerTubeFixture(t, "playlist_page.json"),
		},
	}}
	result, err := NewPeerTubePlaylist().Extract(context.Background(), Request{URL: targetURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if got := append([]string(nil), transport.requests...); !reflect.DeepEqual(got, []string{"GET " + base}) {
		t.Fatalf("playlist was not lazy: requests = %#v", got)
	}
	assertPeerTubePlaylistInfo(t, result, "fixture-playlist")

	for iteration := 0; iteration < 2; iteration++ {
		entries, err := CollectEntries(context.Background(), result.Entries, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 32 {
			t.Fatalf("iteration %d entries = %d", iteration, len(entries))
		}
		for index, entry := range entries {
			if entry.ExtractorKey != "peertube" || !peerTubeShortID.MatchString(entry.ID) {
				t.Fatalf("entry %d = %#v", index, entry)
			}
		}
		if entries[0].ID != fmt.Sprintf("%022d", 1) ||
			entries[29].ID != fmt.Sprintf("%022d", 30) ||
			entries[30].ID != "AaBbCcDdEeFfGgHhIiJjKk" ||
			entries[31].ID != "LlMmNnOoPpQqRrSsTtUuVv" {
			t.Fatalf("iteration %d order = %#v", iteration, entries)
		}
	}
	wantRequests := []string{
		"GET " + base,
		"GET " + base + "/videos?sort=-createdAt&start=0&count=30&nsfw=both",
		"GET " + base + "/videos?sort=-createdAt&start=30&count=30&nsfw=both",
		"GET " + base + "/videos?sort=-createdAt&start=0&count=30&nsfw=both",
		"GET " + base + "/videos?sort=-createdAt&start=30&count=30&nsfw=both",
	}
	if !reflect.DeepEqual(transport.requests, wantRequests) {
		t.Fatalf("requests = %#v, want %#v", transport.requests, wantRequests)
	}
}

func TestPeerTubePlaylistRouteAPIsAndNonFatalMetadata(t *testing.T) {
	for _, test := range []struct {
		name     string
		url      string
		resource string
		id       string
	}{
		{"account", "https://peertube.example.test/a/account/videos", "accounts", "account"},
		{"channel", "https://peertube.example.test/c/channel/videos", "video-channels", "channel"},
		{"playlist", "https://peertube.example.test/w/p/playlist", "video-playlists", "playlist"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := "https://peertube.example.test/api/v1/" + test.resource + "/" + test.id
			transport := &peerTubeFixtureTransport{responses: map[string]peerTubeFixtureResponse{
				base: {status: http.StatusInternalServerError, body: []byte(`{"error":"secret=metadata"}`)},
				base + "/videos?sort=-createdAt&start=0&count=30&nsfw=both": {
					body: peerTubeFixture(t, "playlist_page.json"),
				},
			}}
			result, err := NewPeerTubePlaylist().Extract(context.Background(), Request{URL: test.url, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			title, _ := result.Info.Title()
			if title != test.id {
				t.Fatalf("fallback title = %q", title)
			}
			entries, err := CollectEntries(context.Background(), result.Entries, 10)
			if err != nil || len(entries) != 2 {
				t.Fatalf("entries=%#v err=%v", entries, err)
			}
		})
	}
}

func TestPeerTubePlaylistFailuresBoundsAndCancellation(t *testing.T) {
	base := "https://peertube.example.test/api/v1/accounts/account"
	pageURL := base + "/videos?sort=-createdAt&start=0&count=30&nsfw=both"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewPeerTubePlaylist().Extract(ctx, Request{
		URL: "https://peertube.example.test/a/account/videos", Transport: &peerTubeFixtureTransport{},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	tests := []struct {
		name string
		page peerTubeFixtureResponse
		want error
	}{
		{"auth", peerTubeFixtureResponse{status: http.StatusUnauthorized, body: []byte(`{"error":"token=must-not-leak"}`)}, ErrAuthentication},
		{"network", peerTubeFixtureResponse{err: errors.New("dial token=must-not-leak")}, ErrPeerTubeNetwork},
		{"malformed", peerTubeFixtureResponse{body: []byte(`{"data":`)}, ErrInvalidMetadata},
		{"overflow", peerTubeFixtureResponse{body: makePeerTubePlaylistPage(t, 1, peerTubePlaylistPageSize+1)}, ErrInvalidMetadata},
		{"invalid video id", peerTubeFixtureResponse{body: []byte(`{"data":[{"shortUUID":"bad"}]}`)}, ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &peerTubeFixtureTransport{responses: map[string]peerTubeFixtureResponse{
				base:    {body: peerTubeFixture(t, "playlist_info.json")},
				pageURL: test.page,
			}}
			result, err := NewPeerTubePlaylist().Extract(context.Background(), Request{
				URL: "https://peertube.example.test/a/account/videos", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = CollectEntries(context.Background(), result.Entries, 100)
			if !errors.Is(err, test.want) || strings.Contains(fmt.Sprint(err), "must-not-leak") {
				t.Fatalf("error = %v, want %v without secret", err, test.want)
			}
		})
	}

	transport := &peerTubeFixtureTransport{responses: map[string]peerTubeFixtureResponse{
		base:    {body: peerTubeFixture(t, "playlist_info.json")},
		pageURL: {body: peerTubeFixture(t, "playlist_page.json")},
	}}
	result, err := NewPeerTubePlaylist().Extract(context.Background(), Request{
		URL: "https://peertube.example.test/a/account/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	lazyCtx, lazyCancel := context.WithCancel(context.Background())
	lazyCancel()
	if _, err := CollectEntries(lazyCtx, result.Entries, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("lazy cancellation error = %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("canceled iterator made requests: %#v", transport.requests)
	}
}

func FuzzPeerTubePlaylistRouting(f *testing.F) {
	for _, seed := range []string{
		"https://peertube.example.test/a/account/videos",
		"https://peertube.example.test/c/channel@remote.example/videos",
		"https://peertube.example.test/w/p/AaBbCcDdEeFfGgHhIiJjKk",
		"https://evil.example/w/p/id",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 1<<20 {
			t.Skip()
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		target, ok := parsePeerTubePlaylistURL(parsed)
		if !ok {
			return
		}
		expectedResource := map[string]string{
			"a":   "accounts",
			"c":   "video-channels",
			"w/p": "video-playlists",
		}[target.pathType]
		if expectedResource == "" || target.resource != expectedResource {
			t.Fatalf("invalid route mapping: %#v", target)
		}
		if !validPeerTubeHost(target.host) || !recognizedPeerTubeHost(target.host) || !peerTubeListID.MatchString(target.id) {
			t.Fatalf("unsafe accepted target: %#v", target)
		}
		for _, canonical := range []string{target.metadataURL(), target.webpageURL()} {
			canonicalURL, err := url.Parse(canonical)
			if err != nil || canonicalURL.Scheme != "https" || canonicalURL.User != nil ||
				canonicalURL.Port() != "" || canonicalURL.Fragment != "" ||
				!strings.EqualFold(canonicalURL.Hostname(), target.host) {
				t.Fatalf("unsafe canonical URL %q for %#v: %v", canonical, target, err)
			}
		}
		webpageURL, err := url.Parse(target.webpageURL())
		if err != nil {
			t.Fatalf("canonical webpage URL does not parse: %v", err)
		}
		roundTrip, ok := parsePeerTubePlaylistURL(webpageURL)
		if !ok || roundTrip != target {
			t.Fatalf("canonical round trip = %#v, %v; want %#v", roundTrip, ok, target)
		}
	})
}

func makePeerTubePlaylistPage(t *testing.T, start, count int) []byte {
	t.Helper()
	type item struct {
		ShortUUID string `json:"shortUUID"`
		Name      string `json:"name"`
	}
	items := make([]item, count)
	for index := range items {
		number := start + index
		items[index] = item{ShortUUID: fmt.Sprintf("%022d", number), Name: fmt.Sprintf("Video %d", number)}
	}
	body, err := json.Marshal(struct {
		Data []item `json:"data"`
	}{Data: items})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertPeerTubePlaylistInfo(t *testing.T, result Extraction, wantID string) {
	t.Helper()
	id, _ := result.Info.ID()
	title, _ := result.Info.Title()
	description, _ := result.Info.Lookup("description").StringValue()
	channel, _ := result.Info.Lookup("channel").StringValue()
	channelID, _ := result.Info.Lookup("channel_id").StringValue()
	thumbnail, _ := result.Info.Lookup("thumbnail").StringValue()
	timestamp, _ := result.Info.Lookup("timestamp").Int()
	if id != wantID || title != "Fixture PeerTube playlist" ||
		description != "Deterministic playlist metadata." ||
		channel != "fixture-owner" || channelID != "9" ||
		thumbnail != "https://peertube.example.test/lazy-static/thumbnails/playlist.jpg" ||
		timestamp != 1752842096 {
		t.Fatalf("playlist info = %#v", result.Info.Fields())
	}
}
