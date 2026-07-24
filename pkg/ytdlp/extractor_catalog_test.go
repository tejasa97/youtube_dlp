package ytdlp

import "testing"

type extractorRiskClass string

const (
	riskSimpleDirect  extractorRiskClass = "simple/direct"
	riskSharedBackend extractorRiskClass = "shared-backend"
	riskPlaylistAPI   extractorRiskClass = "playlist/API"
	riskLive          extractorRiskClass = "live"
	riskAuthenticated extractorRiskClass = "authenticated"
	riskManifestHeavy extractorRiskClass = "manifest-heavy"
	riskAntiBot       extractorRiskClass = "anti-bot/impersonated"
	riskRegional      extractorRiskClass = "regional"
	riskJavaScript    extractorRiskClass = "javascript-challenge"
	minimumExtractors                    = 25
)

type representativeExtractor struct {
	name    string
	rawURL  string
	classes []extractorRiskClass
}

var representativeExtractorCatalog = []representativeExtractor{
	{"generic", "https://media.example.invalid/video.mp4", []extractorRiskClass{riskSimpleDirect}},
	{"youtube", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskJavaScript}},
	{"vimeo", "https://vimeo.com/123456789", []extractorRiskClass{riskManifestHeavy}},
	{"twitch", "https://www.twitch.tv/fixture_channel", []extractorRiskClass{riskLive, riskManifestHeavy}},
	{"soundcloud", "https://soundcloud.com/fixture-artist/synthetic-signal", []extractorRiskClass{riskPlaylistAPI}},
	{"streamable", "https://streamable.com/e/fixture_1", []extractorRiskClass{riskSharedBackend, riskSimpleDirect}},
	{"peertube", "peertube:peertube.example:00000000-0000-4000-8000-000000000001", []extractorRiskClass{riskSharedBackend, riskLive, riskManifestHeavy}},
	{"internetarchive", "https://archive.org/details/fixture_concert", []extractorRiskClass{riskPlaylistAPI}},
	{"tiktok", "https://www.tiktok.com/@fixture/video/1234567890123456789", []extractorRiskClass{riskAntiBot}},
	{"synthetic_auth", "https://auth-fixture.invalid/watch/fixture123", []extractorRiskClass{riskAuthenticated}},
	{"region_svt", "https://www.svtplay.se/video/fixture-program?modalId=fixture123", []extractorRiskClass{riskRegional, riskLive}},
	{"brightcove", "https://players.brightcove.net/12345/default_default/index.html?videoId=123", []extractorRiskClass{riskSharedBackend, riskManifestHeavy}},
	{"kaltura", "kaltura:123:1_abcd1234", []extractorRiskClass{riskSharedBackend}},
	{"jwplatform", "https://cdn.jwplayer.com/players/AbCd1234-ABCDEFGHI.js", []extractorRiskClass{riskSharedBackend}},
	{"wistia", "wistia:a1b2c3d4e5", []extractorRiskClass{riskSharedBackend, riskPlaylistAPI}},
	{"sproutvideo", "https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890", []extractorRiskClass{riskSharedBackend}},
	{"dailymotion", "https://www.dailymotion.com/video/x12345", []extractorRiskClass{riskPlaylistAPI}},
	{"reddit", "https://www.reddit.com/r/videos/comments/abc123/title/", []extractorRiskClass{riskPlaylistAPI}},
	{"twitter", "https://x.com/fixture/status/1234567890", []extractorRiskClass{riskPlaylistAPI}},
	{"bandcamp", "https://fixture.bandcamp.com/track/example", []extractorRiskClass{riskPlaylistAPI}},
	{"mixcloud", "https://www.mixcloud.com/fixture/example/", []extractorRiskClass{riskPlaylistAPI}},
	{"rumble", "https://rumble.com/embed/v12345/", []extractorRiskClass{riskPlaylistAPI, riskLive}},
	{"bilibili", "https://www.bilibili.com/video/BV1abcdefgh", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy}},
	{"instagram", "https://www.instagram.com/p/aye83DjauH/", []extractorRiskClass{riskPlaylistAPI, riskAntiBot}},
	{"kick", "https://kick.com/fixture-channel", []extractorRiskClass{riskLive, riskAntiBot, riskManifestHeavy}},
	{"bbciplayer", "https://www.bbc.co.uk/iplayer/episode/p0000000/title", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"ard", "https://www.ardmediathek.de/player/Y3JpZDovL2ZpeHR1cmU", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"nrk", "nrk:MDDP12000117", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"bluesky", "https://bsky.app/profile/fixture.bsky.social/post/3l4omssdl632g", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"imgur", "https://imgur.com/gallery/fixture-A61SaA1", []extractorRiskClass{riskPlaylistAPI, riskSimpleDirect}},
}

func TestRepresentativeExtractorCatalogCountRoutingAndRiskCoverage(t *testing.T) {
	if len(representativeExtractorCatalog) < minimumExtractors {
		t.Fatalf("representative extractors = %d, want at least %d", len(representativeExtractorCatalog), minimumExtractors)
	}
	registry := productRegistry()
	covered := make(map[extractorRiskClass]bool)
	seen := make(map[string]bool)
	for _, representative := range representativeExtractorCatalog {
		if seen[representative.name] {
			t.Fatalf("duplicate representative %q", representative.name)
		}
		seen[representative.name] = true
		selected, err := registry.Select(representative.rawURL)
		if err != nil || selected.Name() != representative.name {
			t.Fatalf("Select(%q) = %v, %v; want %q", representative.rawURL, selected, err, representative.name)
		}
		if len(representative.classes) == 0 {
			t.Fatalf("representative %q has no risk class", representative.name)
		}
		for _, class := range representative.classes {
			covered[class] = true
		}
	}
	for _, class := range []extractorRiskClass{riskSimpleDirect, riskSharedBackend, riskPlaylistAPI, riskLive, riskAuthenticated, riskManifestHeavy, riskAntiBot, riskRegional, riskJavaScript} {
		if !covered[class] {
			t.Fatalf("missing representative risk class %q", class)
		}
	}
}
