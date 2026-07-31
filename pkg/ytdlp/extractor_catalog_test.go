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
	{"youtube", "https://www.youtube.com/watch?v=fixture0001", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskJavaScript}},
	{"vimeo", "https://vimeo.com/123456789", []extractorRiskClass{riskManifestHeavy}},
	{"twitch", "https://www.twitch.tv/fixture_channel", []extractorRiskClass{riskLive, riskManifestHeavy}},
	{"soundcloud", "https://soundcloud.com/fixture-artist/synthetic-signal", []extractorRiskClass{riskPlaylistAPI}},
	{"applepodcasts", "https://podcasts.apple.com/us/podcast/fixture/id123?i=456", []extractorRiskClass{riskSimpleDirect}},
	{"streamable", "https://streamable.com/e/fixture_1", []extractorRiskClass{riskSharedBackend, riskSimpleDirect}},
	{"amara", "https://amara.org/en/videos/jVx79ZKGK1ky/info/why-jury-trials/", []extractorRiskClass{riskSharedBackend, riskPlaylistAPI}},
	{"peertube_playlist", "https://peertube.example/a/fixture/videos", []extractorRiskClass{riskSharedBackend, riskPlaylistAPI}},
	{"peertube", "peertube:peertube.example:00000000-0000-4000-8000-000000000001", []extractorRiskClass{riskSharedBackend, riskLive, riskManifestHeavy}},
	{"internetarchive", "https://archive.org/details/fixture_concert", []extractorRiskClass{riskPlaylistAPI}},
	{"tiktok", "https://www.tiktok.com/@fixture/video/1234567890123456789", []extractorRiskClass{riskAntiBot}},
	{"hytale", "https://hytale.com/news/2021/07/summer-2021-development-update", []extractorRiskClass{riskSharedBackend, riskPlaylistAPI}},
	{"cloudflarestream", "https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1", []extractorRiskClass{riskSharedBackend, riskManifestHeavy}},
	{"washingtonpost", "https://www.washingtonpost.com/video/c/video/480ba4ee-1ec7-11e6-82c2-a7dcb313287d", []extractorRiskClass{riskSharedBackend}},
	{"arcpublishing", "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab", []extractorRiskClass{riskSharedBackend, riskManifestHeavy}},
	{"fox9", "https://www.fox9.com/video/314473", []extractorRiskClass{riskSharedBackend}},
	{"anvato", "anvato:anvato_epfox_app_web_prod_b3373168e12f423f41504f207000188daf88251b:8032455", []extractorRiskClass{riskSharedBackend, riskAuthenticated}},
	{"theplatform", "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT", []extractorRiskClass{riskSharedBackend, riskManifestHeavy}},
	{"theplatform_feed", "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?byGuid=n_hardball_5biden_140207", []extractorRiskClass{riskSharedBackend, riskPlaylistAPI}},
	{"weathercom", "https://weather.com/storms/hurricane/video/invest-95l-fixture", []extractorRiskClass{riskSharedBackend}},
	{"nbcolympics", "https://vplayer.nbcolympics.com/p/NnzsPC/widget/select/media/4Y0TlYUr_ZT7", []extractorRiskClass{riskSharedBackend}},
	{"synthetic_auth", "https://auth-fixture.invalid/watch/fixture123", []extractorRiskClass{riskAuthenticated}},
	{"microsoft_embed", "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy}},
	{"microsoft_medius", "https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b", []extractorRiskClass{riskManifestHeavy}},
	{"microsoft_learn_playlist", "https://learn.microsoft.com/en-us/shows/bash-for-beginners", []extractorRiskClass{riskPlaylistAPI}},
	{"microsoft_learn_episode", "https://learn.microsoft.com/en-us/shows/bash-for-beginners/what-is-the-difference", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy}},
	{"microsoft_learn_session", "https://learn.microsoft.com/en-us/events/build-2022/ts01-rapidly-code-test-ship-from-secure-cloud-developer-environments", []extractorRiskClass{riskPlaylistAPI}},
	{"microsoft_build", "https://build.microsoft.com/en-US/sessions", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy}},
	{"ted_talk", "https://www.ted.com/talks/fixture_talk", []extractorRiskClass{riskSimpleDirect, riskManifestHeavy}},
	{"ted_series", "https://www.ted.com/series/fixture_series#season_2", []extractorRiskClass{riskPlaylistAPI}},
	{"ted_playlist", "https://www.ted.com/playlists/171/fixture_playlist", []extractorRiskClass{riskPlaylistAPI}},
	{"ted_embed", "https://embed-ssl.ted.com/talks/fixture_talk", []extractorRiskClass{riskSharedBackend}},
	{"region_svt", "https://www.svtplay.se/video/fixture-program?modalId=fixture123", []extractorRiskClass{riskRegional, riskLive}},
	{"brightcove", "https://players.brightcove.net/12345/default_default/index.html?videoId=123", []extractorRiskClass{riskSharedBackend, riskManifestHeavy}},
	{"kaltura", "kaltura:123:1_abcd1234", []extractorRiskClass{riskSharedBackend}},
	{"jwplatform", "https://cdn.jwplayer.com/players/AbCd1234-ABCDEFGHI.js", []extractorRiskClass{riskSharedBackend}},
	{"wistia", "wistia:a1b2c3d4e5", []extractorRiskClass{riskSharedBackend, riskPlaylistAPI}},
	{"sproutvideo", "https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890", []extractorRiskClass{riskSharedBackend}},
	{"dailymotion", "https://www.dailymotion.com/video/x12345", []extractorRiskClass{riskPlaylistAPI}},
	{"dailymotion_search", "https://www.dailymotion.com/search/fixture/videos", []extractorRiskClass{riskPlaylistAPI}},
	{"dailymotion_user", "https://www.dailymotion.com/user/fixture", []extractorRiskClass{riskPlaylistAPI}},
	{"reddit", "https://www.reddit.com/r/videos/comments/abc123/title/", []extractorRiskClass{riskPlaylistAPI}},
	{"twitter", "https://x.com/fixture/status/1234567890", []extractorRiskClass{riskPlaylistAPI}},
	{"bandcamp", "https://fixture.bandcamp.com/track/example", []extractorRiskClass{riskPlaylistAPI}},
	{"bandcamp_user", "https://fixture.bandcamp.com/music", []extractorRiskClass{riskPlaylistAPI}},
	{"bandcamp_weekly", "https://bandcamp.com/radio?show=224", []extractorRiskClass{riskPlaylistAPI}},
	{"mixcloud", "https://www.mixcloud.com/fixture/example/", []extractorRiskClass{riskPlaylistAPI}},
	{"rumble", "https://rumble.com/embed/v12345/", []extractorRiskClass{riskPlaylistAPI, riskLive}},
	{"bilibili", "https://www.bilibili.com/video/BV1abcdefgh", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy}},
	{"bilibili_bangumi", "https://www.bilibili.com/bangumi/play/ep21495", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy}},
	{"bilibili_bangumi_media", "https://www.bilibili.com/bangumi/media/md24097891", []extractorRiskClass{riskPlaylistAPI}},
	{"bilibili_bangumi_season", "https://www.bilibili.com/bangumi/play/ss26801", []extractorRiskClass{riskPlaylistAPI}},
	{"bilibili_collection", "https://space.bilibili.com/2142762/lists/3662502", []extractorRiskClass{riskPlaylistAPI}},
	{"bilibili_series", "https://space.bilibili.com/1958703906/lists/547718?type=series", []extractorRiskClass{riskPlaylistAPI}},
	{"bilibili_category", "https://www.bilibili.com/v/kichiku/mad", []extractorRiskClass{riskPlaylistAPI}},
	{"bilibili_audio", "https://www.bilibili.com/audio/au12345", []extractorRiskClass{riskSimpleDirect}},
	{"bilibili_audio_album", "https://www.bilibili.com/audio/am10624", []extractorRiskClass{riskPlaylistAPI}},
	{"bilibili_player", "https://player.bilibili.com/player.html?aid=92494333&cid=157926707&page=1", []extractorRiskClass{riskSimpleDirect}},
	{"bilibili_dynamic", "https://t.bilibili.com/998134289197432852", []extractorRiskClass{riskPlaylistAPI}},
	{"biliintl", "https://www.bilibili.tv/en/play/12345/67890", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"biliintl_series", "https://www.bilibili.tv/en/play/12345", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"instagram", "https://www.instagram.com/p/aye83DjauH/", []extractorRiskClass{riskPlaylistAPI, riskAntiBot}},
	{"kick", "https://kick.com/fixture-channel", []extractorRiskClass{riskLive, riskAntiBot, riskManifestHeavy}},
	{"bbciplayer", "https://www.bbc.co.uk/iplayer/episode/p0000000/title", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"bbc_co_uk_article", "https://www.bbc.co.uk/programmes/articles/FixtureArticleId/title", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"bbc_co_uk_playlist", "https://www.bbc.co.uk/programmes/p0000000/clips", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"bbc_co_uk_iplayer_episodes", "https://www.bbc.co.uk/iplayer/episodes/p0000000/fixture", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"bbc_co_uk_iplayer_group", "https://www.bbc.co.uk/iplayer/group/p0000000", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"ard", "https://www.ardmediathek.de/player/Y3JpZDovL2ZpeHR1cmU", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"ard_audiothek", "https://www.ardaudiothek.de/episode/urn:ard:episode:eabead1add170e93/", []extractorRiskClass{riskPlaylistAPI, riskSimpleDirect, riskRegional}},
	{"ard_audiothek_playlist", "https://www.ardaudiothek.de/sendung/mia-insomnia/urn:ard:show:c405aa26d9a4060a/", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"radiofrance", "http://maison.radiofrance.fr/radiovisions/one-one", []extractorRiskClass{riskSimpleDirect}},
	{"franceculture", "https://www.radiofrance.fr/franceculture/podcasts/science-en-questions/la-physique-d-einstein-8440487", []extractorRiskClass{riskPlaylistAPI, riskSimpleDirect}},
	{"radiofrance_live", "https://www.radiofrance.fr/franceinter/", []extractorRiskClass{riskLive, riskManifestHeavy}},
	{"radiofrance_podcast", "https://www.radiofrance.fr/franceinfo/podcasts/le-billet-vert", []extractorRiskClass{riskPlaylistAPI}},
	{"radiofrance_profile", "https://www.radiofrance.fr/personnes/thomas-pesquet", []extractorRiskClass{riskPlaylistAPI}},
	{"radiofrance_program_schedule", "https://www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023", []extractorRiskClass{riskPlaylistAPI}},
	{"nrk", "nrk:MDDP12000117", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"nrktv", "https://tv.nrk.no/program/MDDP12000117", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"nrktv_series", "https://tv.nrk.no/serie/fixture", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"nrk_skole", "https://www.nrk.no/skole/?mediaId=14099", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"nhk_vod", "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"nhk_vod_program", "https://www3.nhk.or.jp/nhkworld/en/shows/sumo/", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"nhk_for_school_bangumi", "https://www2.nhk.or.jp/school/movie/bangumi.cgi?das_id=D0005150191_00000", []extractorRiskClass{riskManifestHeavy, riskRegional}},
	{"nhk_for_school_subject", "https://www.nhk.or.jp/school/rika/", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"nhk_for_school_program_list", "https://www.nhk.or.jp/school/rika/program-a/", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"nhk_radiru_live", "https://www.nhk.or.jp/radio/player/?ch=r1", []extractorRiskClass{riskLive, riskRegional, riskManifestHeavy}},
	{"nhk_radio_news_page", "https://www.nhk.or.jp/radionews/", []extractorRiskClass{riskPlaylistAPI, riskRegional}},
	{"nhk_radiru", "https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01", []extractorRiskClass{riskPlaylistAPI, riskRegional, riskManifestHeavy}},
	{"bluesky", "https://bsky.app/profile/fixture.bsky.social/post/3l4omssdl632g", []extractorRiskClass{riskPlaylistAPI, riskManifestHeavy, riskRegional}},
	{"imgur", "https://imgur.com/gallery/fixture-A61SaA1", []extractorRiskClass{riskPlaylistAPI, riskSimpleDirect}},
	{"flickr", "https://www.flickr.com/photos/fixture-user/5645318632/in/photostream/", []extractorRiskClass{riskPlaylistAPI, riskSimpleDirect}},
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
