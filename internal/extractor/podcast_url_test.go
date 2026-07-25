package extractor

import "testing"

func TestCleanPodcastMediaURLPinnedCorpus(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ok   bool
	}{
		{
			"https://www.podtrac.com/pts/redirect.mp3/chtbl.com/track/5899E/traffic.megaphone.fm/HSW7835899191.mp3",
			"https://traffic.megaphone.fm/HSW7835899191.mp3",
			true,
		},
		{
			"https://play.podtrac.com/npr-344098539/edge1.pod.npr.org/anon/podcast.mp3",
			"https://edge1.pod.npr.org/anon/podcast.mp3",
			true,
		},
		{
			"https://pdst.fm/e/2.gum.fm/chtbl.com/track/chrt.fm/track/34D33/pscrb.fm/rss/p/traffic.megaphone.fm/episode.mp3?updated=1",
			"https://traffic.megaphone.fm/episode.mp3?updated=1",
			true,
		},
		{
			"https://pdst.fm/e/https://mgln.ai/e/441/www.buzzsprout.com/episode.mp3",
			"https://www.buzzsprout.com/episode.mp3",
			true,
		},
		{"https://https://cdn.example.test/a.m4a#transport", "https://cdn.example.test/a.m4a", true},
		{"javascript:alert(1)", "", false},
		{"https://user:pass@cdn.example.test/a.mp3", "", false},
		{"https://127.0.0.1/a.mp3", "", false},
		{"https://cdn.example.test:443/a.mp3", "", false},
		{"https://cdn.example.test/a%2fb.mp3", "", false},
	}
	for _, test := range tests {
		got, ok := cleanPodcastMediaURL(test.raw, sharedHostingMaxURLBytes)
		if ok != test.ok || got != test.want {
			t.Fatalf("cleanPodcastMediaURL(%q) = %q, %v; want %q, %v", test.raw, got, ok, test.want, test.ok)
		}
	}
}

func TestPodcastHTTPMediaReportsContainerAndProtocolHonestly(t *testing.T) {
	format, ok := podcastHTTPMedia("http", "http://cdn.example.test/audio.m4a")
	if !ok {
		t.Fatal("HTTP media rejected")
	}
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "http" {
		t.Fatalf("protocol = %q", protocol)
	}
	if ext, _ := format.Lookup("ext").StringValue(); ext != "m4a" {
		t.Fatalf("ext = %q", ext)
	}
	if !format.Lookup("acodec").IsMissing() {
		t.Fatalf("container extension must not be reported as acodec")
	}
}

func TestSimplecastMetadataURLGuards(t *testing.T) {
	seasonID := "e23df0da-bae4-4531-8bbf-71364a88dc13"
	if got := simplecastSeasonID("https://api.simplecast.com/seasons/" + seasonID); got != seasonID {
		t.Fatalf("season id = %q", got)
	}
	for _, raw := range []string{
		"http://api.simplecast.com/seasons/" + seasonID,
		"https://user@api.simplecast.com/seasons/" + seasonID,
		"https://api.simplecast.com:443/seasons/" + seasonID,
		"https://api.simplecast.com/seasons/" + seasonID + "?token=secret",
		"https://evil.test/seasons/" + seasonID,
	} {
		if got := simplecastSeasonID(raw); got != "" {
			t.Fatalf("hostile season URL accepted: %q -> %q", raw, got)
		}
	}
	if canonical, channel, ok := simplecastEpisodeWebpage("https://fixture.simplecast.com/episodes/episode-one"); !ok ||
		canonical != "https://fixture.simplecast.com/episodes/episode-one" ||
		channel != "https://fixture.simplecast.com" {
		t.Fatalf("webpage = %q, %q, %v", canonical, channel, ok)
	}
}

func FuzzCleanPodcastMediaURL(f *testing.F) {
	f.Add("https://pdst.fm/e/2.gum.fm/chtbl.com/track/ABCD/cdn.example.test/a.mp3")
	f.Add("https://https://cdn.example.test/a.m4a")
	f.Add("javascript:alert(1)")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 16<<10 {
			t.Skip()
		}
		first, firstOK := cleanPodcastMediaURL(raw, sharedHostingMaxURLBytes)
		second, secondOK := cleanPodcastMediaURL(raw, sharedHostingMaxURLBytes)
		if firstOK != secondOK || first != second {
			t.Fatalf("non-deterministic cleanup: %q/%v %q/%v", first, firstOK, second, secondOK)
		}
		if !firstOK {
			return
		}
		if !strictValidHostedHTTPURL(first) {
			t.Fatalf("unsafe URL accepted: %q", first)
		}
		if again, ok := cleanPodcastMediaURL(first, sharedHostingMaxURLBytes); !ok || again != first {
			t.Fatalf("cleanup is not idempotent: %q -> %q/%v", first, again, ok)
		}
	})
}
