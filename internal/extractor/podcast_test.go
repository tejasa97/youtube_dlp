package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

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
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.simplecast.com/episodes/search": {body: familyFixture(t, "simplecast_episode", "search.json")},
		}}
		result, err := NewSimplecastEpisode().Extract(context.Background(), Request{
			URL: "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal", Transport: transport,
		})
		if err != nil || result.Redirect.ExtractorKey != "simplecast" {
			t.Fatalf("%#v %v", result, err)
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
