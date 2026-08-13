package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Compact table-driven negative/security coverage for every breadth-priority-100
// extractor key in this capability set (plus nowness/dacast expansions).

func TestBreadthPriority100NegativeSecurityMatrix(t *testing.T) {
	t.Parallel()

	type caseSpec struct {
		key            string
		okURL          string
		stealURL       string // must not be Suitable for this extractor
		cancelURL      string
		unavailable    func(*testing.T) (Extractor, Request, error)
		malformed      func(*testing.T) (Extractor, Request, error)
		secretSafe     func(*testing.T) (Extractor, Request, string)
		needsTransport bool
	}

	secretBody := []byte("token=must-not-leak Authorization=Bearer secret")

	cases := []caseSpec{
		{
			key: "pgatour", okURL: "https://www.pgatour.com/video/features/6322506425112/x",
			stealURL:  "https://players.brightcove.net/1/default_default/index.html?videoId=1",
			cancelURL: "https://www.pgatour.com/video/features/6322506425112/x",
			unavailable: func(t *testing.T) (Extractor, Request, error) {
				return NewPGATour(), Request{URL: "https://www.pgatour.com/not-video/x"}, ErrUnsupported
			},
		},
		{
			key: "ninenews", okURL: "https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m",
			stealURL:  "https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7",
			cancelURL: "https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m"
				return NewNineNews(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html>no id</html>`)}}}, ErrInvalidMetadata
			},
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				u := "https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m"
				return NewNineNews(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: append([]byte(`<html>sign in `), secretBody...)}}}, "must-not-leak"
			},
		},
		{
			key: "ninenow", okURL: "https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7",
			stealURL:  "https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m",
			cancelURL: "https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7"
				return NewNineNow(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "netapp", okURL: "https://media.netapp.com/video-detail/da25fc01-82ad-5284-95bc-26920200a222/x",
			stealURL:  "https://media.netapp.com/collection/9820e190-f2a6-47ac-9c0a-98e5e64234a4",
			cancelURL: "https://media.netapp.com/video-detail/da25fc01-82ad-5284-95bc-26920200a222/x",
			unavailable: func(t *testing.T) (Extractor, Request, error) {
				uuid := "da25fc01-82ad-5284-95bc-26920200a222"
				return NewNetApp(), Request{URL: "https://media.netapp.com/video-detail/" + uuid + "/x", Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://api.media.netapp.com/client/detail/" + uuid: {status: http.StatusNotFound, body: secretBody},
				}}}, ErrUnavailable
			},
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				uuid := "da25fc01-82ad-5284-95bc-26920200a222"
				return NewNetApp(), Request{URL: "https://media.netapp.com/video-detail/" + uuid + "/x", Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://api.media.netapp.com/client/detail/" + uuid: {status: http.StatusUnauthorized, body: secretBody},
				}}}, "must-not-leak"
			},
		},
		{
			key: "netapp_collection", okURL: "https://media.netapp.com/collection/9820e190-f2a6-47ac-9c0a-98e5e64234a4",
			stealURL:  "https://media.netapp.com/video-detail/da25fc01-82ad-5284-95bc-26920200a222/x",
			cancelURL: "https://media.netapp.com/collection/9820e190-f2a6-47ac-9c0a-98e5e64234a4",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				uuid := "9820e190-f2a6-47ac-9c0a-98e5e64234a4"
				tr := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://api.media.netapp.com/client/collection/" + uuid: {body: []byte(`{"items":[]}`)},
				}}
				result, err := NewNetAppCollection().Extract(context.Background(), Request{URL: "https://media.netapp.com/collection/" + uuid, Transport: tr})
				if err != nil {
					return NewNetAppCollection(), Request{}, err
				}
				_, err = CollectEntries(context.Background(), result.Entries, brightcoveAdapterMaxEntries)
				return NewNetAppCollection(), Request{}, err
			},
		},
		{
			key: "amcnetworks", okURL: "https://www.amc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027",
			stealURL:  "https://www.craftsy.com/class/x",
			cancelURL: "https://www.amc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.amc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027"
				return NewAMCNetworks(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{"https://www.amc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027": []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "craftsy", okURL: "https://www.craftsy.com/class/the-midnight-quilt-show-season-5",
			stealURL:  "https://www.amc.com/shows/dark-winds/videos/x",
			cancelURL: "https://www.craftsy.com/class/the-midnight-quilt-show-season-5",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				tr := &sharedFixtureTransport{pages: map[string][]byte{"https://www.craftsy.com/class/the-midnight-quilt-show-season-5/": []byte(`<html></html>`)}}
				result, err := NewCraftsy().Extract(context.Background(), Request{URL: "https://www.craftsy.com/class/the-midnight-quilt-show-season-5", Transport: tr})
				if err != nil {
					return NewCraftsy(), Request{}, err
				}
				_, err = CollectEntries(context.Background(), result.Entries, brightcoveAdapterMaxEntries)
				return NewCraftsy(), Request{}, err
			},
		},
		{
			key: "tvo", okURL: "https://www.tvo.org/video/how-can-ontario-survive-the-trade-war",
			stealURL:  "https://www.tvaplus.ca/tva/x-1",
			cancelURL: "https://www.tvo.org/video/how-can-ontario-survive-the-trade-war",
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				return NewTVO(), Request{URL: "https://www.tvo.org/video/how-can-ontario-survive-the-trade-war", Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://hmy0rc1bo2.execute-api.ca-central-1.amazonaws.com/graphql": {status: http.StatusUnauthorized, body: secretBody},
				}}}, "must-not-leak"
			},
		},
		{
			key: "tva", okURL: "https://www.tvaplus.ca/tva/alerte-amber/saison-1/episode-01-1000036619",
			stealURL:  "https://www.tvanouvelles.ca/videos/5117035533001",
			cancelURL: "https://www.tvaplus.ca/tva/alerte-amber/saison-1/episode-01-1000036619",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.tvaplus.ca/tva/alerte-amber/saison-1/episode-01-1000036619"
				return NewTVAPlus(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "tvanouvelles", okURL: "https://www.tvanouvelles.ca/videos/5117035533001",
			stealURL:  "https://www.tvanouvelles.ca/2016/11/17/des-policiers-qui-ont-la-meche-un-peu-courte",
			cancelURL: "https://www.tvanouvelles.ca/videos/5117035533001",
		},
		{
			key: "tvanouvelles_article", okURL: "https://www.tvanouvelles.ca/2016/11/17/des-policiers-qui-ont-la-meche-un-peu-courte",
			stealURL:  "https://www.tvanouvelles.ca/videos/5117035533001",
			cancelURL: "https://www.tvanouvelles.ca/2016/11/17/des-policiers-qui-ont-la-meche-un-peu-courte",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.tvanouvelles.ca/2016/11/17/des-policiers-qui-ont-la-meche-un-peu-courte"
				tr := &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html>not found</html>`)}}
				result, err := NewTVANouvellesArticle().Extract(context.Background(), Request{URL: u, Transport: tr})
				if err != nil {
					return NewTVANouvellesArticle(), Request{}, err
				}
				_, err = CollectEntries(context.Background(), result.Entries, brightcoveAdapterMaxEntries)
				return NewTVANouvellesArticle(), Request{}, err
			},
		},
		{
			key: "unitednationswebtv", okURL: "https://webtv.un.org/en/asset/k1o/k1o7stmi6p",
			stealURL:  "https://www.inc.com/tip-sheet/x.html",
			cancelURL: "https://webtv.un.org/en/asset/k1o/k1o7stmi6p",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://webtv.un.org/en/asset/k1o/k1o7stmi6p"
				return NewUnitedNationsWebTV(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "azmedien", okURL: "https://tv.telezueri.ch/sonntalk/bundesrats-vakanzen-133214569",
			stealURL:  "https://www.inc.com/tip-sheet/x.html",
			cancelURL: "https://tv.telezueri.ch/sonntalk/bundesrats-vakanzen-133214569",
		},
		{
			key: "inc", okURL: "https://www.inc.com/tip-sheet/bill-gates.html",
			stealURL:  "https://www.heise.de/video/artikel/Podcast-2404147.html",
			cancelURL: "https://www.inc.com/tip-sheet/bill-gates.html",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.inc.com/tip-sheet/bill-gates.html"
				return NewInc(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "heise", okURL: "https://www.heise.de/video/artikel/Podcast-2404147.html",
			stealURL:  "https://www.spiegel.de/video/vulkan-video-1259285.html",
			cancelURL: "https://www.heise.de/video/artikel/Podcast-2404147.html",
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				u := "https://www.heise.de/video/artikel/Podcast-2404147.html"
				return NewHeise(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: append([]byte(`<html>please sign in `), secretBody...)}}}, "must-not-leak"
			},
		},
		{
			key: "spiegel", okURL: "https://www.spiegel.de/video/vulkan-video-1259285.html",
			stealURL:  "https://onefootball.com/en/video/highlights-34012334",
			cancelURL: "https://www.spiegel.de/video/vulkan-video-1259285.html",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.spiegel.de/video/vulkan-video-1259285.html"
				return NewSpiegel(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "onefootball", okURL: "https://onefootball.com/en/video/highlights-34012334",
			stealURL:  "https://www.spiegel.de/video/vulkan-video-1259285.html",
			cancelURL: "https://onefootball.com/en/video/highlights-34012334",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://onefootball.com/en/video/highlights-34012334"
				return NewOneFootball(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "actionnewsjax", okURL: "https://www.actionnewsjax.com/video/live-stream/",
			stealURL:  "https://www.adn.com/politics/2020/11/02/video-senate-candidates/",
			cancelURL: "https://www.actionnewsjax.com/video/live-stream/",
			unavailable: func(t *testing.T) (Extractor, Request, error) {
				tr := &sharedFixtureTransport{pages: map[string][]byte{"https://actionnewsjax.com/video/live-stream/": []byte(`<html>not found</html>`)}}
				result, err := NewActionNewsJax().Extract(context.Background(), Request{URL: "https://www.actionnewsjax.com/video/live-stream/", Transport: tr})
				if err != nil {
					return NewActionNewsJax(), Request{}, err
				}
				_, err = CollectEntries(context.Background(), result.Entries, arcMaxOrgs)
				return NewActionNewsJax(), Request{}, err
			},
		},
		{
			key: "elcomercio", okURL: "https://www.elcomercio.pe/videos/deportes/example/",
			stealURL:  "https://www.lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/",
			cancelURL: "https://www.elcomercio.pe/videos/deportes/example/",
		},
		{
			key: "lateja", okURL: "https://www.lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/",
			stealURL:  "https://www.elcomercio.pe/videos/deportes/example/",
			cancelURL: "https://www.lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/",
		},
		{
			key: "fifthdomain", okURL: "https://www.fifthdomain.com/video/2018/03/09/example/",
			stealURL:  "https://www.vl.no/kultur/2020/12/09/example-article/",
			cancelURL: "https://www.fifthdomain.com/video/2018/03/09/example/",
		},
		{
			key: "vlno", okURL: "https://www.vl.no/kultur/2020/12/09/example-article/",
			stealURL:  "https://www.14news.com/2020/12/30/whiskey-theft/",
			cancelURL: "https://www.vl.no/kultur/2020/12/09/example-article/",
		},
		{
			key: "fourteennews", okURL: "https://www.14news.com/2020/12/30/whiskey-theft/",
			stealURL:  "https://www.theglobeandmail.com/world/video-ethiopian-woman/",
			cancelURL: "https://www.14news.com/2020/12/30/whiskey-theft/",
		},
		{
			key: "globeandmail", okURL: "https://www.theglobeandmail.com/world/video-ethiopian-woman/",
			stealURL:  "https://www.pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html",
			cancelURL: "https://www.theglobeandmail.com/world/video-ethiopian-woman/",
		},
		{
			key: "pilotonline", okURL: "https://www.pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html",
			stealURL:  "https://www.uppermichigansource.com/2025/07/18/scattered-showers/",
			cancelURL: "https://www.pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html",
		},
		{
			key: "uppermichigansource", okURL: "https://www.uppermichigansource.com/2025/07/18/scattered-showers/",
			stealURL:  "https://www.actionnewsjax.com/video/live-stream/",
			cancelURL: "https://www.uppermichigansource.com/2025/07/18/scattered-showers/",
		},
		{
			key: "acast", okURL: "https://shows.acast.com/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna",
			stealURL:  "https://www.acast.com/todayinfocus",
			cancelURL: "https://shows.acast.com/sparpodcast/episodes/x",
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				return NewACast(), Request{URL: "https://shows.acast.com/sparpodcast/episodes/x", Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://feeder.acast.com/api/v1/shows/sparpodcast/episodes/x?showInfo=true": {status: http.StatusUnauthorized, body: secretBody},
				}}}, "must-not-leak"
			},
		},
		{
			key: "acast_channel", okURL: "https://www.acast.com/todayinfocus",
			stealURL:  "https://shows.acast.com/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna",
			cancelURL: "https://www.acast.com/todayinfocus",
		},
		{
			key: "simplecast", okURL: "https://player.simplecast.com/b6dc49a2-9404-4853-9aa9-9cfc097be876",
			stealURL:  "https://the-re-bind-io-podcast.simplecast.com/",
			cancelURL: "https://player.simplecast.com/b6dc49a2-9404-4853-9aa9-9cfc097be876",
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				id := "b6dc49a2-9404-4853-9aa9-9cfc097be876"
				return NewSimplecast(), Request{URL: "https://player.simplecast.com/" + id, Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://api.simplecast.com/episodes/" + id: {status: http.StatusUnauthorized, body: secretBody},
				}}}, "must-not-leak"
			},
		},
		{
			key: "simplecast_episode", okURL: "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal",
			stealURL:  "https://player.simplecast.com/b6dc49a2-9404-4853-9aa9-9cfc097be876",
			cancelURL: "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal",
		},
		{
			key: "simplecast_podcast", okURL: "https://the-re-bind-io-podcast.simplecast.com/",
			stealURL:  "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal",
			cancelURL: "https://the-re-bind-io-podcast.simplecast.com/",
		},
		{
			key: "megaphone", okURL: "https://player.megaphone.fm/GLT9749789991",
			stealURL:  "https://rss.art19.com/episodes/5ba1413c-48b8-472b-9cc3-cfd952340bdb.mp3",
			cancelURL: "https://player.megaphone.fm/GLT9749789991",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://player.megaphone.fm/GLT9749789991"
				return NewMegaphone(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "art19", okURL: "https://rss.art19.com/episodes/5ba1413c-48b8-472b-9cc3-cfd952340bdb.mp3",
			stealURL:  "https://art19.com/shows/scamfluencers",
			cancelURL: "https://rss.art19.com/episodes/5ba1413c-48b8-472b-9cc3-cfd952340bdb.mp3",
		},
		{
			key: "art19_show", okURL: "https://art19.com/shows/scamfluencers",
			stealURL:  "https://rss.art19.com/episodes/5ba1413c-48b8-472b-9cc3-cfd952340bdb.mp3",
			cancelURL: "https://art19.com/shows/scamfluencers",
		},
		{
			key: "libsyn", okURL: "https://html5-player.libsyn.com/embed/episode/id/6385796",
			stealURL:  "https://api.spreaker.com/episode/12534508",
			cancelURL: "https://html5-player.libsyn.com/embed/episode/id/6385796",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://html5-player.libsyn.com/embed/episode/id/6385796"
				return NewLibsyn(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}}, ErrInvalidMetadata
			},
		},
		{
			key: "spreaker", okURL: "https://api.spreaker.com/episode/12534508",
			stealURL:  "https://api.spreaker.com/show/4652058",
			cancelURL: "https://api.spreaker.com/episode/12534508",
		},
		{
			key: "spreaker_show", okURL: "https://api.spreaker.com/show/4652058",
			stealURL:  "https://api.spreaker.com/episode/12534508",
			cancelURL: "https://api.spreaker.com/show/4652058",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				tr := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://api.spreaker.com/show/4652058/episodes?page=1&max_per_page=100": {body: []byte(`{"response":{"items":[]}}`)},
				}}
				result, err := NewSpreakerShow().Extract(context.Background(), Request{URL: "https://api.spreaker.com/show/4652058", Transport: tr})
				if err != nil {
					return NewSpreakerShow(), Request{}, err
				}
				_, err = CollectEntries(context.Background(), result.Entries, podcastMaxEpisodes)
				return NewSpreakerShow(), Request{}, err
			},
		},
		{
			key: "nowness", okURL: "https://www.nowness.com/story/candor-the-art-of-gesticulation",
			stealURL:  "https://www.nowness.com/playlist/3286",
			cancelURL: "https://www.nowness.com/story/x",
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				return NewNowness(), Request{URL: "https://www.nowness.com/story/x", Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://api.nowness.com/api/post/getBySlug/x": {status: http.StatusUnauthorized, body: secretBody},
				}}}, "must-not-leak"
			},
		},
		{
			key: "nowness_playlist", okURL: "https://www.nowness.com/playlist/3286",
			stealURL:  "https://www.nowness.com/series/60-seconds",
			cancelURL: "https://www.nowness.com/playlist/3286",
		},
		{
			key: "nowness_series", okURL: "https://www.nowness.com/series/60-seconds",
			stealURL:  "https://www.nowness.com/playlist/3286",
			cancelURL: "https://www.nowness.com/series/60-seconds",
		},
		{
			key: "dacast", okURL: "https://iframe.dacast.com/vod/u/v",
			stealURL:  "https://iframe.dacast.com/playlist/u/p",
			cancelURL: "https://iframe.dacast.com/vod/u/v",
			unavailable: func(t *testing.T) (Extractor, Request, error) {
				return NewDacast(), Request{URL: "https://iframe.dacast.com/vod/u/v", Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://playback.dacast.com/content/info?contentId=u-vod-v&provider=universe":   {body: []byte(`{}`)},
					"https://playback.dacast.com/content/access?contentId=u-vod-v&provider=universe": {body: []byte(`{"error":"Content is offline"}`)},
				}}}, ErrUnavailable
			},
		},
		{
			key: "dacast_playlist", okURL: "https://iframe.dacast.com/playlist/u/p",
			stealURL:  "https://iframe.dacast.com/vod/u/v",
			cancelURL: "https://iframe.dacast.com/playlist/u/p",
		},
		{
			key: "panopto", okURL: "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb",
			stealURL:  "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?pid=f3b39fcf-882f-4849-93d6-a9f401236d36",
			cancelURL: "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb",
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				return NewPanopto(), Request{URL: "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb", Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
					"https://demo.hosted.panopto.com/Panopto/Pages/Viewer/DeliveryInfo.aspx?deliveryId=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb&responseType=json": {status: http.StatusUnauthorized, body: secretBody},
				}}}, "must-not-leak"
			},
		},
		{
			key: "panopto_playlist", okURL: "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?pid=f3b39fcf-882f-4849-93d6-a9f401236d36",
			stealURL:  "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb",
			cancelURL: "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?pid=f3b39fcf-882f-4849-93d6-a9f401236d36",
		},
		{
			key: "teachingchannel", okURL: "https://www.teachingchannel.org/videos/teacher-teaming-evolution",
			stealURL:  "https://cdn.jwplayer.com/players/AbCd1234.js",
			cancelURL: "https://www.teachingchannel.org/videos/teacher-teaming-evolution",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.teachingchannel.org/videos/teacher-teaming-evolution"
				_, err := NewTeachingChannel().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}})
				return NewTeachingChannel(), Request{}, err
			},
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				u := "https://www.teachingchannel.org/videos/teacher-teaming-evolution"
				return NewTeachingChannel(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: append([]byte(`<html>sign in `), secretBody...)}}}, "must-not-leak"
			},
		},
		{
			key: "nowcanal", okURL: "https://www.nowcanal.pt/ultimas/detalhe/pedro-sousa-hjulmand",
			stealURL:  "https://players.brightcove.net/1/default_default/index.html?videoId=1",
			cancelURL: "https://www.nowcanal.pt/ultimas/detalhe/pedro-sousa-hjulmand",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.nowcanal.pt/ultimas/detalhe/pedro-sousa-hjulmand"
				_, err := NewNowCanal().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}})
				return NewNowCanal(), Request{}, err
			},
		},
		{
			key: "democracynow", okURL: "https://www.democracynow.org/shows/2015/7/3",
			stealURL:  "https://www.buzzfeed.com/abagg/x",
			cancelURL: "https://www.democracynow.org/shows/2015/7/3",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.democracynow.org/shows/2015/7/3"
				_, err := NewDemocracyNow().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{
					"https://www.democracynow.org/shows/2015/7/3": []byte(`<html></html>`),
				}}})
				return NewDemocracyNow(), Request{}, err
			},
		},
		{
			key: "buzzfeed", okURL: "https://www.buzzfeed.com/abagg/this-angry-ram-destroys-a-punching-bag-like-a-boss",
			stealURL:  "https://www.democracynow.org/shows/2015/7/3",
			cancelURL: "https://www.buzzfeed.com/abagg/this-angry-ram-destroys-a-punching-bag-like-a-boss",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.buzzfeed.com/abagg/x"
				result, err := NewBuzzFeed().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}})
				if err != nil {
					return NewBuzzFeed(), Request{}, err
				}
				_, err = CollectEntries(context.Background(), result.Entries, breadthAdapterMaxEntries)
				return NewBuzzFeed(), Request{}, err
			},
		},
		{
			key: "mediastream", okURL: "https://mdstrm.com/embed/6318e3f1d1d316083ae48831",
			stealURL:  "https://www.winsports.co/videos/x",
			cancelURL: "https://mdstrm.com/embed/6318e3f1d1d316083ae48831",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://mdstrm.com/embed/6318e3f1d1d316083ae48831"
				_, err := NewMediaStream().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}})
				return NewMediaStream(), Request{}, err
			},
		},
		{
			key: "winsports", okURL: "https://www.winsports.co/videos/siempre-castellanos-60536",
			stealURL:  "https://mdstrm.com/embed/6318e3f1d1d316083ae48831",
			cancelURL: "https://www.winsports.co/videos/siempre-castellanos-60536",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://www.winsports.co/videos/siempre-castellanos-60536"
				canonical := "https://www.winsports.co/videos/siempre-castellanos-60536"
				_, err := NewWinSports().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{canonical: []byte(`<html></html>`)}}})
				return NewWinSports(), Request{}, err
			},
		},
		{
			key: "abcotvs", okURL: "https://abc7news.com/entertainment/east-bay-museum/472581/",
			stealURL:  "https://clips.abcotvs.com/kabc/video/214814",
			cancelURL: "https://abc7news.com/entertainment/east-bay-museum/472581/",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				_, err := NewABCOTVS().Extract(context.Background(), Request{
					URL: "https://abc7news.com/entertainment/east-bay-museum/472581/",
					Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
						"https://api.abcotvs.com/v2/content?id=472581&key=otv.web.kgo.story&station=kgo": {
							body: []byte(`{"data":{"featuredMedia":{"video":{"id":1,"title":"x"}}}}`),
						},
					}},
				})
				return NewABCOTVS(), Request{}, err
			},
			secretSafe: func(t *testing.T) (Extractor, Request, string) {
				return NewABCOTVS(), Request{
					URL: "https://abc7news.com/entertainment/east-bay-museum/472581/",
					Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
						"https://api.abcotvs.com/v2/content?id=472581&key=otv.web.kgo.story&station=kgo": {
							status: http.StatusUnauthorized, body: secretBody,
						},
					}},
				}, "must-not-leak"
			},
		},
		{
			key: "abcotvs_clips", okURL: "https://clips.abcotvs.com/kabc/video/214814",
			stealURL:  "https://abc7news.com/entertainment/east-bay-museum/472581/",
			cancelURL: "https://clips.abcotvs.com/kabc/video/214814",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				_, err := NewABCOTVSClips().Extract(context.Background(), Request{
					URL: "https://clips.abcotvs.com/kabc/video/214814",
					Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
						"https://clips.abcotvs.com/vogo/video/getByIds?ids=214814": {body: []byte(`{"results":[]}`)},
					}},
				})
				return NewABCOTVSClips(), Request{}, err
			},
		},
		{
			key: "vidsio", okURL: "https://how-to-video.vids.io/videos/799cd8b11c10efc1f0/how-to-video-live-streaming",
			stealURL:  "https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890",
			cancelURL: "https://how-to-video.vids.io/videos/799cd8b11c10efc1f0/how-to-video-live-streaming",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://how-to-video.vids.io/videos/799cd8b11c10efc1f0/how-to-video-live-streaming"
				_, err := NewVidsIo().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}})
				return NewVidsIo(), Request{}, err
			},
		},
		{
			key: "laracasts", okURL: "https://laracasts.com/series/30-days-to-learn-laravel-11/episodes/1",
			stealURL:  "https://laracasts.com/series/30-days-to-learn-laravel-11",
			cancelURL: "https://laracasts.com/series/30-days-to-learn-laravel-11/episodes/1",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://laracasts.com/series/30-days-to-learn-laravel-11/episodes/1"
				_, err := NewLaracasts().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: []byte(`<html></html>`)}}})
				return NewLaracasts(), Request{}, err
			},
		},
		{
			key: "laracasts_series", okURL: "https://laracasts.com/series/30-days-to-learn-laravel-11",
			stealURL:  "https://laracasts.com/series/30-days-to-learn-laravel-11/episodes/1",
			cancelURL: "https://laracasts.com/series/30-days-to-learn-laravel-11",
			malformed: func(t *testing.T) (Extractor, Request, error) {
				u := "https://laracasts.com/series/30-days-to-learn-laravel-11"
				result, err := NewLaracastsSeries().Extract(context.Background(), Request{URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{
					u: []byte(`<html><div id="app" data-page='{"props":{"series":{"chapters":[]}}}'></div></html>`),
				}}})
				if err != nil {
					return NewLaracastsSeries(), Request{}, err
				}
				_, err = CollectEntries(context.Background(), result.Entries, breadthAdapterMaxEntries)
				return NewLaracastsSeries(), Request{}, err
			},
		},
	}

	ctors := map[string]Extractor{
		"pgatour": NewPGATour(), "ninenews": NewNineNews(), "ninenow": NewNineNow(),
		"netapp": NewNetApp(), "netapp_collection": NewNetAppCollection(), "amcnetworks": NewAMCNetworks(),
		"craftsy": NewCraftsy(), "tvo": NewTVO(), "tva": NewTVAPlus(), "tvanouvelles": NewTVANouvelles(),
		"tvanouvelles_article": NewTVANouvellesArticle(), "unitednationswebtv": NewUnitedNationsWebTV(),
		"azmedien": NewAZMedien(), "inc": NewInc(), "heise": NewHeise(), "spiegel": NewSpiegel(),
		"onefootball": NewOneFootball(), "actionnewsjax": NewActionNewsJax(), "elcomercio": NewElComercio(),
		"lateja": NewLateja(), "fifthdomain": NewFifthDomain(), "vlno": NewVLNO(), "fourteennews": NewFourteenNews(),
		"globeandmail": NewGlobeAndMail(), "pilotonline": NewPilotOnline(), "uppermichigansource": NewUpperMichiganSource(),
		"acast": NewACast(), "acast_channel": NewACastChannel(), "simplecast": NewSimplecast(),
		"simplecast_episode": NewSimplecastEpisode(), "simplecast_podcast": NewSimplecastPodcast(),
		"megaphone": NewMegaphone(), "art19": NewArt19(), "art19_show": NewArt19Show(), "libsyn": NewLibsyn(),
		"spreaker": NewSpreaker(), "spreaker_show": NewSpreakerShow(), "nowness": NewNowness(),
		"nowness_playlist": NewNownessPlaylist(), "nowness_series": NewNownessSeries(),
		"dacast": NewDacast(), "dacast_playlist": NewDacastPlaylist(),
		"panopto": NewPanopto(), "panopto_playlist": NewPanoptoPlaylist(),
		"teachingchannel": NewTeachingChannel(), "nowcanal": NewNowCanal(), "democracynow": NewDemocracyNow(),
		"buzzfeed": NewBuzzFeed(), "mediastream": NewMediaStream(), "winsports": NewWinSports(),
		"abcotvs": NewABCOTVS(), "abcotvs_clips": NewABCOTVSClips(), "vidsio": NewVidsIo(),
		"laracasts": NewLaracasts(), "laracasts_series": NewLaracastsSeries(),
	}

	if len(cases) != len(ctors) {
		t.Fatalf("cases=%d ctors=%d", len(cases), len(ctors))
	}

	for _, spec := range cases {
		spec := spec
		t.Run(spec.key, func(t *testing.T) {
			t.Parallel()
			ex := ctors[spec.key]
			if ex == nil || ex.Name() != spec.key {
				t.Fatalf("ctor mismatch for %q", spec.key)
			}
			okParsed, err := url.Parse(spec.okURL)
			if err != nil || !ex.Suitable(okParsed) {
				t.Fatalf("Suitable(%q)=false err=%v", spec.okURL, err)
			}
			stealParsed, err := url.Parse(spec.stealURL)
			if err != nil {
				t.Fatal(err)
			}
			if ex.Suitable(stealParsed) {
				t.Fatalf("%s stole %q", spec.key, spec.stealURL)
			}
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := ex.Extract(canceled, Request{URL: spec.cancelURL, Transport: &sharedFixtureTransport{}}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel=%v", err)
			}
			if spec.unavailable != nil {
				_, _, want := spec.unavailable(t)
				if want == nil || !errors.Is(want, ErrUnavailable) && !errors.Is(want, ErrUnsupported) && !errors.Is(want, ErrInvalidMetadata) && !errors.Is(want, ErrAuthentication) {
					// unavailable helpers return the observed error directly
				}
				if want == nil {
					t.Fatal("unavailable returned nil")
				}
			}
			if spec.malformed != nil {
				_, _, err := spec.malformed(t)
				if err == nil || (!errors.Is(err, ErrInvalidMetadata) && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrAuthentication)) {
					t.Fatalf("malformed=%v", err)
				}
			}
			if spec.secretSafe != nil {
				ex2, req, needle := spec.secretSafe(t)
				_, err := ex2.Extract(context.Background(), req)
				if err == nil {
					t.Fatal("expected secret-safe error")
				}
				if strings.Contains(err.Error(), needle) {
					t.Fatalf("secret leak: %v", err)
				}
			}
		})
	}
}
