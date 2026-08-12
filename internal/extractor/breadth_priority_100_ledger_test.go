package extractor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Program-wide auditable inventory for extractor-breadth since baseline
// 172a718c (the shared-family breadth set). Counts require successful Extract
// (or verified URLResult re-entry / playlist CollectEntries), never
// Suitable-only, and never
// hostname aliases with identical behavior.

const (
	breadthMinSuccessShapes = 100
	breadthMinPlaylists     = 20
)

type breadthShapeSpec struct {
	ID        string
	Canonical string // stable shape identity: key|route-syntax (not fixture cardinality)
	Key       string
	Kind      string // media | url_result | playlist
	Run       func(t *testing.T)
}

type breadthPlaylistSpec struct {
	ID  string
	Key string
	Run func(t *testing.T)
}

func TestBreadthPriority100AuditableInventory(t *testing.T) {
	t.Parallel()
	shapes := breadthProgramSuccessShapes(t)
	playlists := breadthProgramPlaylists(t)

	if len(shapes) < breadthMinSuccessShapes {
		t.Fatalf("success shapes=%d want >=%d; inventory=%v", len(shapes), breadthMinSuccessShapes, shapeIDs(shapes))
	}
	if len(playlists) < breadthMinPlaylists {
		t.Fatalf("playlist behaviors=%d want >=%d; inventory=%v", len(playlists), breadthMinPlaylists, playlistIDs(playlists))
	}

	seenShape := map[string]bool{}
	seenCanonical := map[string]bool{}
	for _, shape := range shapes {
		if shape.ID == "" || shape.Canonical == "" || shape.Key == "" || shape.Run == nil {
			t.Fatalf("invalid shape %#v", shape)
		}
		if seenShape[shape.ID] {
			t.Fatalf("duplicate shape id %q", shape.ID)
		}
		if seenCanonical[shape.Canonical] {
			t.Fatalf("duplicate canonical shape identity %q (id %q)", shape.Canonical, shape.ID)
		}
		seenShape[shape.ID] = true
		seenCanonical[shape.Canonical] = true
		t.Run("shape/"+shape.ID, shape.Run)
	}

	seenPL := map[string]bool{}
	for _, pl := range playlists {
		if pl.ID == "" || pl.Key == "" || pl.Run == nil {
			t.Fatalf("invalid playlist %#v", pl)
		}
		if seenPL[pl.ID] {
			t.Fatalf("duplicate playlist id %q", pl.ID)
		}
		seenPL[pl.ID] = true
		t.Run("playlist/"+pl.ID, pl.Run)
	}

	t.Logf("breadth inventory: %d success shapes, %d playlist behaviors", len(shapes), len(playlists))
}

func shapeIDs(shapes []breadthShapeSpec) []string {
	out := make([]string, 0, len(shapes))
	for _, s := range shapes {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

func playlistIDs(playlists []breadthPlaylistSpec) []string {
	out := make([]string, 0, len(playlists))
	for _, p := range playlists {
		out = append(out, p.ID)
	}
	sort.Strings(out)
	return out
}

func assertURLResultReentry(t *testing.T, result Extraction, wantKey string, child Extractor, childTransport Transport) {
	t.Helper()
	if !result.IsURL() || result.Redirect.ExtractorKey != wantKey {
		t.Fatalf("url_result=%#v want key %q", result, wantKey)
	}
	media, err := child.Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: childTransport})
	if err != nil {
		t.Fatal(err)
	}
	if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
		t.Fatal("re-entry missing formats")
	}
}

func assertLazyPlaylist(t *testing.T, result Extraction, transport *sharedFixtureTransport, maxEntries int, wantKey string, minEntries int) []Entry {
	t.Helper()
	if !result.IsPlaylist() {
		t.Fatalf("want playlist got %#v", result)
	}
	if transport != nil && transport.requestCount() != 0 {
		t.Fatalf("lazy playlist networked before iteration: %d", transport.requestCount())
	}
	first, err := CollectEntries(context.Background(), result.Entries, maxEntries)
	if err != nil || len(first) < minEntries {
		t.Fatalf("entries=%v err=%v min=%d", first, err, minEntries)
	}
	if wantKey != "" && first[0].ExtractorKey != wantKey {
		t.Fatalf("entry key=%q want %q", first[0].ExtractorKey, wantKey)
	}
	second, err := CollectEntries(context.Background(), result.Entries, maxEntries)
	if err != nil || len(second) != len(first) {
		t.Fatalf("reusable iteration failed first=%d second=%d err=%v", len(first), len(second), err)
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].URL != second[i].URL {
			t.Fatalf("order/reuse mismatch at %d: %#v vs %#v", i, first[i], second[i])
		}
	}
	return first
}

func brightcoveFixtureTransport(t *testing.T, account, player, video string) *sharedFixtureTransport {
	t.Helper()
	if player == "" {
		player = "default"
	}
	return &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://players.brightcove.net/" + account + "/" + player + "_default/config.json": {body: sharedFixture(t, "brightcove.json")},
		"https://edge.api.brightcove.com/playback/v1/accounts/" + account + "/videos/" + video: {
			body: []byte(`{"id":"` + video + `","name":"BC","duration":1000,"sources":[{"src":"https://media.example/bc/master.m3u8","type":"application/x-mpegURL"}]}`),
		},
	}}
}

func breadthArcTransport(org, uuid string) *sharedFixtureTransport {
	return &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		arcAPIEndpoint(org) + "?uuid=" + uuid: {body: mustReadFamilyFixture("arcpublishing", "video.json")},
	}}
}

func breadthAnvatoTransport() *sharedFixtureTransport {
	accessKey := fox9AnvatoAccessKey
	const wantAdstAuth = "APVytK5DkP4="
	serverEndpoint := anvatoAPIBase + "/server_time?anvack=" + url.QueryEscape(accessKey)
	query := url.Values{}
	query.Set("anvack", accessKey)
	query.Set("X-Anvato-Adst-Auth", wantAdstAuth)
	query.Set("rtyp", "fp")
	videoEndpoint := anvatoAPIBase + "/mcp/video/8032455?" + query.Encode()
	return &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
		videoEndpoint:  {body: mustReadFamilyFixture("anvato", "video.json")},
	}}
}

func mustReadFamilyFixture(family, name string) []byte {
	data, err := os.ReadFile(filepath.Join(familyFixtureRoot, family, name))
	if err != nil {
		panic(err)
	}
	return data
}

func arcMultiPowaHTML(org, uuid1, uuid2 string) []byte {
	return []byte(fmt.Sprintf(
		`<html><div class="powa" data-org="%s" data-uuid="%s"></div><div class="powa" data-org="%s" data-uuid="%s"></div></html>`,
		org, uuid1, org, uuid2,
	))
}

func breadthProgramPlaylists(t *testing.T) []breadthPlaylistSpec {
	t.Helper()
	return []breadthPlaylistSpec{
		{ID: "hytale", Key: "hytale", Run: func(t *testing.T) {
			transport := &sharedFixtureTransport{pages: map[string][]byte{
				"https://hytale.com/news/2021/07/summer-2021-development-update": familyFixture(t, "hytale", "news.html"),
			}}
			result, err := NewHytale().Extract(context.Background(), Request{
				URL: "https://www.hytale.com/news/2021/07/summer-2021-development-update", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, hytaleMaxStreamIDs, "cloudflarestream", 1)
		}},
		arcPlaylistSpec("adn", NewADN(), "https://www.adn.com/politics/2020/11/02/video-senate-candidates/", "https://adn.com/politics/2020/11/02/video-senate-candidates/", "adn", "8c99cb6e-b29c-4bc9-9173-7bf9979225ab", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("bostonglobe", NewBostonGlobe(), "https://www.bostonglobe.com/video/2020/12/30/metro/example/", "https://bostonglobe.com/video/2020/12/30/metro/example/", "bostonglobe", "232b7ae6-7d73-432d-bc0a-85dbf0119ab1", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("gray", NewGray(), "https://www.wabi.tv/video/2020/12/30/example/", "https://wabi.tv/video/2020/12/30/example/", "gray", "0b0ba30e-032a-4598-8810-901d70e6033e", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("clickondetroit", NewClickOnDetroit(), "https://www.clickondetroit.com/video/community/2020/05/15/example/", "https://clickondetroit.com/video/community/2020/05/15/example/", "gmg", "c8793fb2-8d44-4242-881e-2db31da2d9fe", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("actionnewsjax", NewActionNewsJax(), "https://www.actionnewsjax.com/video/live-stream/", "https://actionnewsjax.com/video/live-stream/", "cmg", "cfb1cf1b-3ab5-4d1b-86c5-a5515d311f2a", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("elcomercio", NewElComercio(), "https://www.elcomercio.pe/videos/deportes/example/", "https://elcomercio.pe/videos/deportes/example/", "elcomercio", "27a7e1f8-2ec7-4177-874f-a4feed2885b3", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("lateja", NewLateja(), "https://www.lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/", "https://lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/", "gruponacion", "dfcbfa57-527f-45ff-a69b-35fe71054143", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("fifthdomain", NewFifthDomain(), "https://www.fifthdomain.com/video/2018/03/09/example/", "https://fifthdomain.com/video/2018/03/09/example/", "mco", "aa0ca6fe-1127-46d4-b32c-be0d6fdb8055", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("vlno", NewVLNO(), "https://www.vl.no/kultur/2020/12/09/example-article/", "https://vl.no/kultur/2020/12/09/example-article/", "mentormedier", "47a12084-650b-4011-bfd0-3699b6947b2d", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("fourteennews", NewFourteenNews(), "https://www.14news.com/2020/12/30/whiskey-theft/", "https://14news.com/2020/12/30/whiskey-theft/", "raycom", "b89f61f8-79fa-4c09-8255-e64237119bf7", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("globeandmail", NewGlobeAndMail(), "https://www.theglobeandmail.com/world/video-ethiopian-woman/", "https://theglobeandmail.com/world/video-ethiopian-woman/", "tgam", "411b34c1-8701-4036-9831-26964711664b", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("pilotonline", NewPilotOnline(), "https://www.pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html", "https://pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html", "tronc", "460f2931-8130-4719-8ea1-ffcb2d7cb685", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		arcPlaylistSpec("uppermichigansource", NewUpperMichiganSource(), "https://www.uppermichigansource.com/2025/07/18/scattered-showers/", "https://uppermichigansource.com/2025/07/18/scattered-showers/", "gray", "508116f7-e999-48db-b7c2-60a04842679b", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		{ID: "netapp_collection", Key: "netapp_collection", Run: func(t *testing.T) {
			uuid := "9820e190-f2a6-47ac-9c0a-98e5e64234a4"
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://api.media.netapp.com/client/collection/" + uuid: {body: familyFixture(t, "netapp_collection", "collection.json")},
			}}
			result, err := NewNetAppCollection().Extract(context.Background(), Request{URL: "https://media.netapp.com/collection/" + uuid, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, brightcoveAdapterMaxEntries, "brightcove", 2)
		}},
		{ID: "craftsy", Key: "craftsy", Run: func(t *testing.T) {
			transport := &sharedFixtureTransport{pages: map[string][]byte{
				"https://www.craftsy.com/class/the-midnight-quilt-show-season-5/": familyFixture(t, "craftsy", "page.html"),
			}}
			result, err := NewCraftsy().Extract(context.Background(), Request{
				URL: "https://www.craftsy.com/class/the-midnight-quilt-show-season-5", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, brightcoveAdapterMaxEntries, "brightcove", 2)
		}},
		{ID: "tvanouvelles_article", Key: "tvanouvelles_article", Run: func(t *testing.T) {
			pageURL := "https://www.tvanouvelles.ca/2016/11/17/des-policiers-qui-ont-la-meche-un-peu-courte"
			transport := &sharedFixtureTransport{pages: map[string][]byte{pageURL: familyFixture(t, "tvanouvelles_article", "page.html")}}
			result, err := NewTVANouvellesArticle().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, brightcoveAdapterMaxEntries, "tvanouvelles", 2)
		}},
		{ID: "acast_channel", Key: "acast_channel", Run: func(t *testing.T) {
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://feeder.acast.com/api/v1/shows/todayinfocus": {body: familyFixture(t, "acast_channel", "show.json")},
			}}
			result, err := NewACastChannel().Extract(context.Background(), Request{URL: "https://www.acast.com/todayinfocus", Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "acast", 2)
		}},
		{ID: "simplecast_podcast", Key: "simplecast_podcast", Run: func(t *testing.T) {
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://api.simplecast.com/sites/search": {body: familyFixture(t, "simplecast_podcast", "search.json")},
				"https://api.simplecast.com/podcasts/e23df0da-bae4-4531-8bbf-71364a88dc13/episodes": {
					body: familyFixture(t, "simplecast_podcast", "episodes.json"),
				},
			}}
			result, err := NewSimplecastPodcast().Extract(context.Background(), Request{
				URL: "https://the-re-bind-io-podcast.simplecast.com/", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "simplecast", 1)
		}},
		{ID: "art19_show", Key: "art19_show", Run: func(t *testing.T) {
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://art19.com/shows/scamfluencers": {body: familyFixture(t, "art19_show", "show.json")},
			}}
			result, err := NewArt19Show().Extract(context.Background(), Request{URL: "https://art19.com/shows/scamfluencers", Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "art19", 1)
		}},
		{ID: "spreaker_show", Key: "spreaker_show", Run: func(t *testing.T) {
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://api.spreaker.com/show/4652058/episodes?page=1&max_per_page=100": {
					body: familyFixture(t, "spreaker_show", "episodes.json"),
				},
			}}
			result, err := NewSpreakerShow().Extract(context.Background(), Request{URL: "https://api.spreaker.com/show/4652058", Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "spreaker", 1)
		}},
		{ID: "nowness_playlist", Key: "nowness_playlist", Run: func(t *testing.T) {
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://api.nowness.com/api/post?PlaylistId=3286": {body: familyFixture(t, "nowness_playlist", "playlist.json")},
			}}
			result, err := NewNownessPlaylist().Extract(context.Background(), Request{
				URL: "https://www.nowness.com/playlist/3286/blues", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, nownessMaxEntries, "nowness", 2)
		}},
		{ID: "nowness_series", Key: "nowness_series", Run: func(t *testing.T) {
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://api.nowness.com/api/series/getBySlug/60-seconds": {body: familyFixture(t, "nowness_series", "series.json")},
			}}
			result, err := NewNownessSeries().Extract(context.Background(), Request{
				URL: "https://www.nowness.com/series/60-seconds", Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, nownessMaxPosts, "nowness", 2)
		}},
		{ID: "panopto_playlist", Key: "panopto_playlist", Run: func(t *testing.T) {
			host := "demo.hosted.panopto.com"
			pid := "f3b39fcf-882f-4849-93d6-a9f401236d36"
			slist := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
			transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://" + host + "/Panopto/Api/Playlists/" + pid: {body: familyFixture(t, "panopto_playlist", "playlist.json")},
				"https://" + host + "/Panopto/Api/SessionLists/" + slist + "?collections[0].maxCount=500&collections[0].name=items": {
					body: familyFixture(t, "panopto_playlist", "sessionlist.json"),
				},
			}}
			result, err := NewPanoptoPlaylist().Extract(context.Background(), Request{
				URL: "https://" + host + "/Panopto/Pages/Viewer.aspx?pid=" + pid, Transport: transport,
			})
			if err != nil {
				t.Fatal(err)
			}
			entries := assertLazyPlaylist(t, result, transport, panoptoMaxEntries, "panopto", 2)
			videoTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://" + host + "/Panopto/Pages/Viewer/DeliveryInfo.aspx?deliveryId=" + entries[0].ID + "&responseType=json": {
					body: familyFixture(t, "panopto", "deliveryinfo.json"),
				},
			}}
			media, err := NewPanopto().Extract(context.Background(), Request{URL: entries[0].URL, Transport: videoTransport})
			if err != nil {
				t.Fatal(err)
			}
			if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
				t.Fatal("re-entry formats")
			}
		}},
		{ID: "dacast_playlist", Key: "dacast_playlist", Run: func(t *testing.T) {
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
			if err != nil {
				t.Fatal(err)
			}
			assertLazyPlaylist(t, result, transport, dacastMaxPlaylistEntries, "dacast", 2)
		}},
		{ID: "buzzfeed", Key: "buzzfeed", Run: func(t *testing.T) {
			u := "https://www.buzzfeed.com/abagg/this-angry-ram-destroys-a-punching-bag-like-a-boss"
			page := []byte(`<html>
<div class="video-embed" rel:bf_bucket_data='{"video":{"id":"fixture0001","url":"https://www.youtube.com/watch?v=fixture0001"}}'></div>
<div class="video-embed" rel:bf_bucket_data='{"video":{"id":"fb1","url":"https://www.facebook.com/watch/?v=971793786185728"}}'></div>
</html>`)
			transport := &sharedFixtureTransport{pages: map[string][]byte{u: page}}
			result, err := NewBuzzFeed().Extract(context.Background(), Request{URL: u, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			entries := assertLazyPlaylist(t, result, transport, breadthAdapterMaxEntries, "youtube", 2)
			if entries[1].ExtractorKey != "" {
				t.Fatalf("facebook entry key=%q", entries[1].ExtractorKey)
			}
		}},
		{ID: "laracasts_series", Key: "laracasts_series", Run: func(t *testing.T) {
			u := "https://laracasts.com/series/30-days-to-learn-laravel-11"
			transport := &sharedFixtureTransport{pages: map[string][]byte{u: familyFixture(t, "laracasts_series", "page.html")}}
			result, err := NewLaracastsSeries().Extract(context.Background(), Request{URL: u, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			entries := assertLazyPlaylist(t, result, transport, breadthAdapterMaxEntries, "vimeo", 2)
			child := breadthVimeoReentryTransport(t)
			for _, entry := range entries {
				media, err := NewVimeo().Extract(context.Background(), Request{URL: entry.URL, Transport: child})
				if err != nil {
					t.Fatal(err)
				}
				if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
					t.Fatalf("vimeo re-entry missing formats for %q", entry.URL)
				}
			}
		}},
	}
}

func arcPlaylistSpec(id string, ctor Extractor, pageURL, hostPath, org, uuid1, uuid2 string) breadthPlaylistSpec {
	return breadthPlaylistSpec{ID: id, Key: id, Run: func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			hostPath: arcMultiPowaHTML(org, uuid1, uuid2),
		}}
		result, err := ctor.Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
		if !strings.Contains(entries[0].URL, org+":"+uuid1) {
			t.Fatalf("unexpected first entry %q", entries[0].URL)
		}
	}}
}
