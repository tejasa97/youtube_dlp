package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNownessFamilySuccess(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewNowness(), NewNownessPlaylist(), NewNownessSeries(), NewBrightcove(), NewVimeo())

	t.Run("nowness_brightcove", func(t *testing.T) {
		transport := &sharedFixtureTransport{
			responses: map[string]fixtureHTTP{
				"https://api.nowness.com/api/post/getBySlug/candor-the-art-of-gesticulation": {
					body: familyFixture(t, "nowness", "post.json"),
				},
			},
			pages: map[string][]byte{
				"https://www.nowness.com/iframe?id=2520295746001": familyFixture(t, "nowness", "iframe.html"),
			},
		}
		result, err := NewNowness().Extract(context.Background(), Request{
			URL: "https://www.nowness.com/story/candor-the-art-of-gesticulation", Transport: transport,
		})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
		selected, err := registry.SelectFor(result.Redirect.URL, "brightcove")
		if err != nil {
			t.Fatal(err)
		}
		bc := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://players.brightcove.net/2385340575001/default_default/config.json": {body: sharedFixture(t, "brightcove.json")},
			"https://edge.api.brightcove.com/playback/v1/accounts/2385340575001/videos/2520295746001": {
				body: []byte(`{"id":"2520295746001","name":"Nowness","duration":1000,"sources":[{"src":"https://media.example/bc/master.m3u8","type":"application/x-mpegURL"}]}`),
			},
		}}
		media, err := selected.Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: bc})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats after re-entry")
		}
	})

	t.Run("nowness_playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.nowness.com/api/post?PlaylistId=3286": {body: familyFixture(t, "nowness_playlist", "playlist.json")},
		}}
		result, err := NewNownessPlaylist().Extract(context.Background(), Request{
			URL: "https://www.nowness.com/playlist/3286/i-guess-thats-why-they-call-it-the-blues", Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("lazy fetch before iterate: %d", transport.requestCount())
		}
		entries, err := CollectEntries(context.Background(), result.Entries, nownessMaxEntries)
		if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "nowness" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
		again, err := CollectEntries(context.Background(), result.Entries, nownessMaxEntries)
		if err != nil || len(again) != 2 {
			t.Fatalf("reusable=%v err=%v", again, err)
		}
	})

	t.Run("nowness_series", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.nowness.com/api/series/getBySlug/60-seconds": {body: familyFixture(t, "nowness_series", "series.json")},
		}}
		result, err := NewNownessSeries().Extract(context.Background(), Request{
			URL: "https://www.nowness.com/series/60-seconds", Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, nownessMaxPosts)
		if err != nil || len(entries) != 2 {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})

	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://www.nowness.com/story/candor-the-art-of-gesticulation", "nowness"},
		{"https://cn.nowness.com/story/kasper-bjorke-ft-jaakko-eino-kalevi-tnr", "nowness"},
		{"https://www.nowness.com/series/nowness-picks/jean-luc-godard-supercut", "nowness"},
		{"https://www.nowness.com/playlist/3286/blues", "nowness_playlist"},
		{"https://www.nowness.com/series/60-seconds", "nowness_series"},
		{"https://www.nowness.com/playlist/3286", "nowness_playlist"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestNownessFamilyNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewNowness().Extract(canceled, Request{
		URL: "https://www.nowness.com/story/x", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	auth := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://api.nowness.com/api/post/getBySlug/x": {status: http.StatusUnauthorized, body: []byte("token=secret")},
	}}
	if _, err := NewNowness().Extract(context.Background(), Request{
		URL: "https://www.nowness.com/story/x", Transport: auth,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("auth=%v", err)
	}
	parsed, _ := url.Parse("https://evil.example/story/x")
	if NewNowness().Suitable(parsed) {
		t.Fatal("foreign host must not match")
	}
}

func TestDacastFamilySuccess(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewDacast(), NewDacastPlaylist())
	user := "acae82153ef4d7a7344ae4eaa86af534"
	vid := "1c6143e3-5a06-371d-8695-19b96ea49090"
	t.Run("dacast", func(t *testing.T) {
		contentID := user + "-vod-" + vid
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://playback.dacast.com/content/info?contentId=" + contentID + "&provider=universe": {
				body: familyFixture(t, "dacast", "info.json"),
			},
			"https://playback.dacast.com/content/access?contentId=" + contentID + "&provider=universe": {
				body: familyFixture(t, "dacast", "access.json"),
			},
		}}
		result, err := NewDacast().Extract(context.Background(), Request{
			URL: "https://iframe.dacast.com/vod/" + user + "/" + vid, Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	t.Run("dacast_playlist", func(t *testing.T) {
		plUser := "943bb1ab3c03695ba85330d92d6d226e"
		plID := "b632eb053cac17a9c9a02bcfc827f2d8"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://playback.dacast.com/content/info?contentId=" + plUser + "-playlist-" + plID + "&provider=universe": {
				body: familyFixture(t, "dacast_playlist", "info.json"),
			},
		}}
		result, err := NewDacastPlaylist().Extract(context.Background(), Request{
			URL: "https://iframe.dacast.com/playlist/" + plUser + "/" + plID, Transport: transport,
		})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("lazy fetch before iterate: %d", transport.requestCount())
		}
		entries, err := CollectEntries(context.Background(), result.Entries, dacastMaxPlaylistEntries)
		if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "dacast" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})
	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://iframe.dacast.com/vod/" + user + "/" + vid, "dacast"},
		{"https://iframe.dacast.com/playlist/943bb1ab3c03695ba85330d92d6d226e/b632eb053cac17a9c9a02bcfc827f2d8", "dacast_playlist"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestDacastFamilyNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewDacast().Extract(canceled, Request{
		URL: "https://iframe.dacast.com/vod/u/v", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	offline := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://playback.dacast.com/content/info?contentId=u-vod-v&provider=universe": {body: []byte(`{}`)},
		"https://playback.dacast.com/content/access?contentId=u-vod-v&provider=universe": {
			body: []byte(`{"error":"Content is offline"}`),
		},
	}}
	if _, err := NewDacast().Extract(context.Background(), Request{
		URL: "https://iframe.dacast.com/vod/u/v", Transport: offline,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("offline=%v", err)
	}
}

func FuzzParseNownessURLs(f *testing.F) {
	f.Add("https://www.nowness.com/story/candor")
	f.Add("https://www.nowness.com/playlist/3286")
	f.Add("https://www.nowness.com/series/60-seconds")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseNownessStoryURL(parsed)
		_, _ = parseNownessPlaylistURL(parsed)
		_, _ = parseNownessSeriesURL(parsed)
	})
}

func FuzzParseDacastURLs(f *testing.F) {
	f.Add("https://iframe.dacast.com/vod/user/video")
	f.Add("https://iframe.dacast.com/playlist/user/pl")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _, _ = parseDacastVODURL(parsed)
		_, _, _ = parseDacastPlaylistURL(parsed)
	})
}
