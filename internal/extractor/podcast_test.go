package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type simplecastSearchTransport struct {
	t             *testing.T
	body          []byte
	status        int
	header        http.Header
	ambientCalls  int
	isolatedCalls int
}

func (transport *simplecastSearchTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	transport.t.Fatal("unexpected page request")
	return nil, nil, errors.New("unexpected page request")
}

func (transport *simplecastSearchTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.ambientCalls++
	return nil, errors.New("ambient transport must not be used")
}

func (transport *simplecastSearchTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.t.Helper()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.isolatedCalls++
	if request.Method != http.MethodPost || request.URL.String() != "https://api.simplecast.com/episodes/search" {
		transport.t.Fatalf("request = %s %s", request.Method, request.URL)
	}
	if got := request.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		transport.t.Fatalf("Content-Type = %q", got)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		transport.t.Fatal(err)
	}
	const want = "url=https%3A%2F%2Fthe-re-bind-io-podcast.simplecast.com%2Fepisodes%2Ferrant-signal"
	if string(body) != want {
		transport.t.Fatalf("body = %q; want %q", body, want)
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	header := transport.header
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(transport.body)),
		Request:    request,
	}, nil
}

func TestPodcastFamilySuccessAndPlaylists(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(
		NewACast(), NewACastChannel(), NewSimplecast(), NewSimplecastEpisode(), NewSimplecastPodcast(),
		NewMegaphone(), NewArt19(), NewArt19Show(), NewLibsyn(), NewSpreaker(), NewSpreakerShow(),
	)

	t.Run("acast", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://feeder.acast.com/api/v1/shows/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna?showInfo=true": {
				body: familyFixture(t, "acast", "episode.json"),
			},
		}}
		result, err := NewACast().Extract(context.Background(), Request{
			URL: "https://shows.acast.com/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		} else {
			format, _ := formats[0].Object()
			if mediaURL, _ := format.Lookup("url").StringValue(); mediaURL != "https://media.example.invalid/acast/episode.mp3" {
				t.Fatalf("cleaned Acast URL = %q", mediaURL)
			}
		}
		for key, want := range map[string]int64{
			"duration": 120, "timestamp": 1580990400, "filesize": 12345,
			"season_number": 4, "episode_number": 2,
		} {
			if got, ok := result.Info.Lookup(key).Int(); !ok || got != want {
				t.Fatalf("Acast %s = %d, %v; want %d", key, got, ok, want)
			}
		}
		if creator, _ := result.Info.Lookup("creator").StringValue(); creator != "Author" {
			t.Fatalf("Acast creator = %q", creator)
		}
	})

	t.Run("acast_channel", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://feeder.acast.com/api/v1/shows/todayinfocus": {body: familyFixture(t, "acast_channel", "show.json")},
		}}
		result, err := NewACastChannel().Extract(context.Background(), Request{
			URL: "https://www.acast.com/todayinfocus", Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, podcastMaxEpisodes)
		if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "acast" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})

	t.Run("simplecast", func(t *testing.T) {
		id := "b6dc49a2-9404-4853-9aa9-9cfc097be876"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.simplecast.com/episodes/" + id: {body: familyFixture(t, "simplecast", "episode.json")},
		}}
		result, err := NewSimplecast().Extract(context.Background(), Request{
			URL: "https://player.simplecast.com/" + id, Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		} else {
			format, _ := formats[0].Object()
			if mediaURL, _ := format.Lookup("url").StringValue(); mediaURL != "https://media.example.invalid/sc.mp3?updated=1" {
				t.Fatalf("cleaned Simplecast URL = %q", mediaURL)
			}
		}
		for key, want := range map[string]int64{
			"duration": 100, "timestamp": 1580990400, "filesize": 23456,
			"season_number": 1, "episode_number": 1,
		} {
			if got, ok := result.Info.Lookup(key).Int(); !ok || got != want {
				t.Fatalf("Simplecast %s = %d, %v; want %d", key, got, ok, want)
			}
		}
		if seasonID, _ := result.Info.Lookup("season_id").StringValue(); seasonID != "e23df0da-bae4-4531-8bbf-71364a88dc13" {
			t.Fatalf("Simplecast season_id = %q", seasonID)
		}
		if webpage, _ := result.Info.Lookup("webpage_url").StringValue(); webpage != "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal" {
			t.Fatalf("Simplecast webpage_url = %q", webpage)
		}
	})

	t.Run("simplecast_episode", func(t *testing.T) {
		transport := &simplecastSearchTransport{t: t, body: familyFixture(t, "simplecast_episode", "search.json")}
		result, err := NewSimplecastEpisode().Extract(context.Background(), Request{
			URL: "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal", Transport: transport,
		})
		if err != nil || result.IsURL() || result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.ambientCalls != 0 || transport.isolatedCalls != 1 {
			t.Fatalf("ambient=%d isolated=%d; want 0/1", transport.ambientCalls, transport.isolatedCalls)
		}
		if id, _ := result.Info.Lookup("id").StringValue(); id != "b6dc49a2-9404-4853-9aa9-9cfc097be876" {
			t.Fatalf("id = %q", id)
		}
		if episodeID, _ := result.Info.Lookup("episode_id").StringValue(); episodeID != "b6dc49a2-9404-4853-9aa9-9cfc097be876" {
			t.Fatalf("episode_id = %q", episodeID)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) != 1 {
			t.Fatal("missing inline format")
		} else {
			format, _ := formats[0].Object()
			if mediaURL, _ := format.Lookup("url").StringValue(); mediaURL != "https://media.example.invalid/sc.mp3?updated=1" {
				t.Fatalf("cleaned media URL = %q", mediaURL)
			}
		}
		if series, _ := result.Info.Lookup("series").StringValue(); series != "RE:BIND" {
			t.Fatalf("series = %q", series)
		}
		if duration, ok := result.Info.Lookup("duration").Int(); !ok || duration != 100 {
			t.Fatalf("duration = %d, %v", duration, ok)
		}
	})

	t.Run("simplecast_podcast", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.simplecast.com/sites/search": {body: familyFixture(t, "simplecast_podcast", "search.json")},
			"https://api.simplecast.com/podcasts/e23df0da-bae4-4531-8bbf-71364a88dc13/episodes": {
				body: familyFixture(t, "simplecast_podcast", "episodes.json"),
			},
		}}
		result, err := NewSimplecastPodcast().Extract(context.Background(), Request{
			URL: "https://the-re-bind-io-podcast.simplecast.com/", Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("lazy fetch before iterate: %d", transport.requestCount())
		}
		entries, err := CollectEntries(context.Background(), result.Entries, podcastMaxEpisodes)
		if err != nil || len(entries) == 0 || entries[0].ExtractorKey != "simplecast" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})

	t.Run("megaphone", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://player.megaphone.fm/GLT9749789991": familyFixture(t, "megaphone", "page.html"),
		}}
		result, err := NewMegaphone().Extract(context.Background(), Request{
			URL: "https://player.megaphone.fm/GLT9749789991", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})

	t.Run("art19", func(t *testing.T) {
		id := "5ba1413c-48b8-472b-9cc3-cfd952340bdb"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://art19.com/episodes/" + id: {body: familyFixture(t, "art19", "episode.json")},
		}}
		result, err := NewArt19().Extract(context.Background(), Request{
			URL: "https://rss.art19.com/episodes/" + id + ".mp3", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})

	t.Run("art19_show", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://art19.com/shows/scamfluencers": {body: familyFixture(t, "art19_show", "show.json")},
		}}
		result, err := NewArt19Show().Extract(context.Background(), Request{
			URL: "https://art19.com/shows/scamfluencers", Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("lazy fetch before iterate: %d", transport.requestCount())
		}
		entries, err := CollectEntries(context.Background(), result.Entries, podcastMaxEpisodes)
		if err != nil || len(entries) == 0 {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})

	t.Run("libsyn", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://html5-player.libsyn.com/embed/episode/id/6385796": familyFixture(t, "libsyn", "page.html"),
		}}
		result, err := NewLibsyn().Extract(context.Background(), Request{
			URL: "https://html5-player.libsyn.com/embed/episode/id/6385796/", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})

	t.Run("spreaker", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.spreaker.com/v2/episodes/12534508": {body: familyFixture(t, "spreaker", "episode.json")},
		}}
		result, err := NewSpreaker().Extract(context.Background(), Request{
			URL: "https://api.spreaker.com/episode/12534508", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})

	t.Run("spreaker_show", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.spreaker.com/show/4652058/episodes?page=1&max_per_page=100": {
				body: familyFixture(t, "spreaker_show", "episodes.json"),
			},
		}}
		result, err := NewSpreakerShow().Extract(context.Background(), Request{
			URL: "https://api.spreaker.com/show/4652058", Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("lazy fetch before iterate: %d", transport.requestCount())
		}
		entries, err := CollectEntries(context.Background(), result.Entries, podcastMaxEpisodes)
		if err != nil || len(entries) == 0 {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
		again, err := CollectEntries(context.Background(), result.Entries, podcastMaxEpisodes)
		if err != nil || len(again) != len(entries) {
			t.Fatalf("reusable=%v err=%v", again, err)
		}
	})

	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://shows.acast.com/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna", "acast"},
		{"https://play.acast.com/s/sparpodcast/2a92b283-1a75-4ad8-8396-499c641de0d9", "acast"},
		{"https://www.acast.com/todayinfocus", "acast_channel"},
		{"https://player.simplecast.com/b6dc49a2-9404-4853-9aa9-9cfc097be876", "simplecast"},
		{"https://api.simplecast.com/episodes/b6dc49a2-9404-4853-9aa9-9cfc097be876", "simplecast"},
		{"https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal", "simplecast_episode"},
		{"https://the-re-bind-io-podcast.simplecast.com/", "simplecast_podcast"},
		{"https://the-re-bind-io-podcast.simplecast.com/episodes", "simplecast_podcast"},
		{"https://player.megaphone.fm/GLT9749789991", "megaphone"},
		{"https://rss.art19.com/episodes/5ba1413c-48b8-472b-9cc3-cfd952340bdb.mp3", "art19"},
		{"https://art19.com/shows/scamfluencers/episodes/8319b776-4153-4d22-8630-631f204a03dd", "art19"},
		{"https://art19.com/shows/scamfluencers", "art19_show"},
		{"https://html5-player.libsyn.com/embed/episode/id/6385796", "libsyn"},
		{"https://api.spreaker.com/episode/12534508", "spreaker"},
		{"https://www.spreaker.com/episode/60269615", "spreaker"},
		{"https://api.spreaker.com/show/4652058", "spreaker_show"},
		{"https://www.spreaker.com/podcast/health-wealth--5918323", "spreaker_show"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestPodcastFamilyNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewACast().Extract(canceled, Request{
		URL: "https://shows.acast.com/sparpodcast/episodes/x", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	auth := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://api.simplecast.com/episodes/b6dc49a2-9404-4853-9aa9-9cfc097be876": {
			status: http.StatusUnauthorized, body: []byte("token=must-not-leak"),
		},
	}}
	if _, err := NewSimplecast().Extract(context.Background(), Request{
		URL: "https://player.simplecast.com/b6dc49a2-9404-4853-9aa9-9cfc097be876", Transport: auth,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("auth=%v", err)
	}
	parsed, _ := url.Parse("http://user:pass@player.megaphone.fm/GLT9749789991")
	if NewMegaphone().Suitable(parsed) {
		t.Fatal("userinfo must be rejected")
	}
}

func TestSimplecastEpisodeInlineHydrationValidation(t *testing.T) {
	t.Parallel()
	const (
		id        = "b6dc49a2-9404-4853-9aa9-9cfc097be876"
		canonical = "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal"
	)
	valid := simplecastEpisodePayload{
		ID:           id,
		Title:        "  Errant Signal  ",
		EnclosureURL: "https://media.example.invalid/episode.mp3",
		EpisodeURL:   canonical,
		Description:  "  description  ",
	}
	result, err := simplecastEpisodeExtraction(valid, "", canonical, true)
	if err != nil {
		t.Fatal(err)
	}
	if title, _ := result.Info.Lookup("title").StringValue(); title != "Errant Signal" {
		t.Fatalf("title = %q", title)
	}
	if episode, _ := result.Info.Lookup("episode").StringValue(); episode != "Errant Signal" {
		t.Fatalf("episode = %q", episode)
	}
	if description, _ := result.Info.Lookup("description").StringValue(); description != "description" {
		t.Fatalf("description = %q", description)
	}

	tests := []struct {
		name     string
		mutate   func(*simplecastEpisodePayload)
		expected string
	}{
		{"missing id", func(payload *simplecastEpisodePayload) { payload.ID = "" }, ""},
		{"invalid id", func(payload *simplecastEpisodePayload) { payload.ID = "not-a-uuid" }, ""},
		{"blank title", func(payload *simplecastEpisodePayload) { payload.Title = " \t" }, ""},
		{"missing media", func(payload *simplecastEpisodePayload) { payload.EnclosureURL = "" }, ""},
		{"unsafe media", func(payload *simplecastEpisodePayload) { payload.EnclosureURL = "file:///secret" }, ""},
		{"mismatched requested id", func(*simplecastEpisodePayload) {}, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := valid
			test.mutate(&payload)
			if _, err := simplecastEpisodeExtraction(payload, test.expected, canonical, true); !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("error = %v; want invalid metadata", err)
			}
		})
	}

	hostile := valid
	hostile.EpisodeURL = "https://other.simplecast.com/episodes/different"
	result, err = simplecastEpisodeExtraction(hostile, "", canonical, true)
	if err != nil {
		t.Fatal(err)
	}
	if webpage, _ := result.Info.Lookup("webpage_url").StringValue(); webpage != canonical {
		t.Fatalf("identity-swapped webpage URL accepted: %q", webpage)
	}
	if _, ok := result.Info.Lookup("channel_url").StringValue(); ok {
		t.Fatal("identity-swapped channel URL accepted")
	}

	longTitle := valid
	longTitle.Title = strings.Repeat("x", podcastMaxTitle+100)
	result, err = simplecastEpisodeExtraction(longTitle, "", canonical, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"title", "episode"} {
		got, _ := result.Info.Lookup(key).StringValue()
		if len(got) != podcastMaxTitle {
			t.Fatalf("%s length = %d; want %d", key, len(got), podcastMaxTitle)
		}
	}
}

func TestSimplecastEpisodeSearchTransportAndFailures(t *testing.T) {
	t.Parallel()
	const canonical = "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal"
	extract := func(transport Transport) error {
		_, err := NewSimplecastEpisode().Extract(context.Background(), Request{URL: canonical, Transport: transport})
		return err
	}
	if err := extract(&sharedFixtureTransport{}); !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("missing isolation error = %v", err)
	}
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, []byte("token=must-not-leak"), ErrAuthentication},
		{"forbidden", http.StatusForbidden, []byte("secret=must-not-leak"), ErrAuthentication},
		{"geo forbidden", http.StatusForbidden, []byte(`{"reason":"geo","token":"must-not-leak"}`), ErrRegionRestricted},
		{"not found", http.StatusNotFound, nil, ErrUnavailable},
		{"gone", http.StatusGone, nil, ErrUnavailable},
		{"legal", http.StatusUnavailableForLegalReasons, nil, ErrRegionRestricted},
		{"redirect", http.StatusFound, []byte("signed=must-not-leak"), nil},
		{"malformed", http.StatusOK, []byte(`{"id":`), ErrInvalidMetadata},
		{"trailing", http.StatusOK, []byte(`{} {}`), ErrInvalidMetadata},
		{"oversize", http.StatusOK, bytes.Repeat([]byte("x"), int(maxExtractorJSONBytes)+1), ErrJSONResponseTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &simplecastSearchTransport{
				t:      t,
				status: test.status,
				body:   test.body,
				header: http.Header{"Location": []string{"https://evil.example/?token=must-not-leak"}},
			}
			err := extract(transport)
			if test.name == "redirect" {
				var statusErr string
				if err != nil {
					statusErr = err.Error()
				}
				if err == nil || !strings.Contains(statusErr, "HTTP status 302") {
					t.Fatalf("redirect error = %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("error = %v; want %v", err, test.want)
			}
			if transport.ambientCalls != 0 || transport.isolatedCalls != 1 {
				t.Fatalf("ambient=%d isolated=%d", transport.ambientCalls, transport.isolatedCalls)
			}
			if err != nil && strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &simplecastSearchTransport{t: t}
	_, err := NewSimplecastEpisode().Extract(ctx, Request{URL: canonical, Transport: transport})
	if !errors.Is(err, context.Canceled) || transport.isolatedCalls != 0 || transport.ambientCalls != 0 {
		t.Fatalf("cancellation error=%v ambient=%d isolated=%d", err, transport.ambientCalls, transport.isolatedCalls)
	}
}

func FuzzParseACastEpisodeURL(f *testing.F) {
	f.Add("https://shows.acast.com/sparpodcast/episodes/2.raggarmordet")
	f.Add("https://play.acast.com/s/rattegangspodden/s04e09")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _, _ = parseACastEpisodeURL(parsed)
	})
}

func FuzzParseSimplecastEpisodeURL(f *testing.F) {
	f.Add("https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal")
	f.Add("https://api.simplecast.com/episodes/b6dc49a2-9404-4853-9aa9-9cfc097be876")
	f.Add("https://evil.example/simplecast.com/episodes/x")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		host, slug, ok := parseSimplecastEpisodeURL(parsed)
		if !ok {
			return
		}
		switch host {
		case "api.simplecast.com", "player.simplecast.com", "cdn.simplecast.com", "embed.simplecast.com", "feeds.simplecast.com":
			t.Fatalf("reserved host accepted: %q", host)
		}
		if !strings.HasSuffix(host, ".simplecast.com") || !podcastSlug.MatchString(slug) ||
			parsed.User != nil || parsed.Port() != "" {
			t.Fatalf("unsafe accepted route: host=%q slug=%q", host, slug)
		}
		roundTrip, err := url.Parse("https://" + host + "/episodes/" + slug)
		if err != nil {
			t.Fatal(err)
		}
		gotHost, gotSlug, gotOK := parseSimplecastEpisodeURL(roundTrip)
		if !gotOK || gotHost != host || gotSlug != slug {
			t.Fatalf("round trip = %q/%q/%v; want %q/%q/true", gotHost, gotSlug, gotOK, host, slug)
		}
	})
}
