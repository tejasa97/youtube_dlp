package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestBrightcoveAdaptersSuitableAndHandoff(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(
		NewPGATour(), NewNineNews(), NewNineNow(), NewNetApp(), NewNetAppCollection(),
		NewAMCNetworks(), NewCraftsy(), NewTVO(), NewTVAPlus(), NewTVANouvelles(),
		NewTVANouvellesArticle(), NewBrightcove(),
	)

	brightConfig := func(account, player, video string) map[string]fixtureHTTP {
		return map[string]fixtureHTTP{
			"https://players.brightcove.net/" + account + "/" + player + "_default/config.json": {
				body: sharedFixture(t, "brightcove.json"),
			},
			"https://edge.api.brightcove.com/playback/v1/accounts/" + account + "/videos/" + video: {
				body: []byte(`{"id":"` + video + `","name":"Brightcove Fixture","duration":12000,"sources":[{"src":"https://media.example/bc/master.m3u8","type":"application/x-mpegURL"},{"src":"https://media.example/bc/video.mp4","height":720,"avg_bitrate":1500000}]}`),
			},
		}
	}

	t.Run("pgatour", func(t *testing.T) {
		rawURL := "https://www.pgatour.com/video/features/6322506425112/follow-the-players-trophy"
		result, err := NewPGATour().Extract(context.Background(), Request{URL: rawURL})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("pgatour=%#v err=%v", result, err)
		}
		if !strings.Contains(result.Redirect.URL, pgaTourFeaturesAccount) || !strings.Contains(result.Redirect.URL, "6322506425112") {
			t.Fatalf("redirect=%q", result.Redirect.URL)
		}
		selected, err := registry.SelectFor(result.Redirect.URL, result.Redirect.ExtractorKey)
		if err != nil || selected.Name() != "brightcove" {
			t.Fatal(err)
		}
		transport := &sharedFixtureTransport{responses: brightConfig(pgaTourFeaturesAccount, pgaTourFeaturesPlayer, "6322506425112")}
		media, err := selected.Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
		cloudcast := "https://www.pgatour.com/video/competition/T6322447785112/adam-hadwin"
		cloud, err := NewPGATour().Extract(context.Background(), Request{URL: cloudcast})
		if err != nil || !strings.Contains(cloud.Redirect.URL, pgaTourCloudcastAccount) {
			t.Fatalf("cloudcast=%#v err=%v", cloud, err)
		}
	})

	t.Run("tvanouvelles", func(t *testing.T) {
		rawURL := "https://www.tvanouvelles.ca/videos/5117035533001"
		result, err := NewTVANouvelles().Extract(context.Background(), Request{URL: rawURL})
		if err != nil || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
		selected, _ := registry.SelectFor(result.Redirect.URL, "brightcove")
		transport := &sharedFixtureTransport{responses: brightConfig(tvaNouvellesAccount, "default", "5117035533001")}
		if _, err := selected.Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: transport}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tvanouvelles_article", func(t *testing.T) {
		pageURL := "https://www.tvanouvelles.ca/2016/11/17/des-policiers-qui-ont-la-meche-un-peu-courte"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.tvanouvelles.ca/2016/11/17/des-policiers-qui-ont-la-meche-un-peu-courte": familyFixture(t, "tvanouvelles_article", "page.html"),
		}}
		result, err := NewTVANouvellesArticle().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, brightcoveAdapterMaxEntries)
		if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "tvanouvelles" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})

	t.Run("netapp", func(t *testing.T) {
		uuid := "da25fc01-82ad-5284-95bc-26920200a222"
		rawURL := "https://media.netapp.com/video-detail/" + uuid + "/seamless"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.media.netapp.com/client/detail/" + uuid: {body: familyFixture(t, "netapp", "detail.json")},
		}}
		result, err := NewNetApp().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != brightcovePlayerURL(netAppBrightcoveAccount, "default", "123") {
			t.Fatalf("%#v %v", result, err)
		}
		selected, _ := registry.SelectFor(result.Redirect.URL, "brightcove")
		bcTransport := &sharedFixtureTransport{responses: brightConfig(netAppBrightcoveAccount, "default", "123")}
		if _, err := selected.Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: bcTransport}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("netapp_collection", func(t *testing.T) {
		uuid := "9820e190-f2a6-47ac-9c0a-98e5e64234a4"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.media.netapp.com/client/collection/" + uuid: {body: familyFixture(t, "netapp_collection", "collection.json")},
		}}
		result, err := NewNetAppCollection().Extract(context.Background(), Request{URL: "https://media.netapp.com/collection/" + uuid, Transport: transport})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, brightcoveAdapterMaxEntries)
		if err != nil || len(entries) != 2 || entries[0].ExtractorKey != "brightcove" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})

	t.Run("ninenews", func(t *testing.T) {
		pageURL := "https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m": familyFixture(t, "ninenews", "page.html"),
		}}
		result, err := NewNineNews().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("ninenow", func(t *testing.T) {
		pageURL := "https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7": familyFixture(t, "ninenow", "page.html"),
		}}
		result, err := NewNineNow().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || !strings.Contains(result.Redirect.URL, nineNowBrightcoveAccount) {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("amcnetworks", func(t *testing.T) {
		pageURL := "https://www.amc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.amc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027": familyFixture(t, "amcnetworks", "page.html"),
		}}
		result, err := NewAMCNetworks().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || result.Redirect.ID != "123" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("craftsy", func(t *testing.T) {
		pageURL := "https://www.craftsy.com/class/the-midnight-quilt-show-season-5"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.craftsy.com/class/the-midnight-quilt-show-season-5/": familyFixture(t, "craftsy", "page.html"),
		}}
		result, err := NewCraftsy().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, brightcoveAdapterMaxEntries)
		if err != nil || len(entries) != 2 {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
	})

	t.Run("tvo", func(t *testing.T) {
		pageURL := "https://www.tvo.org/video/how-can-ontario-survive-the-trade-war"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://hmy0rc1bo2.execute-api.ca-central-1.amazonaws.com/graphql": {body: familyFixture(t, "tvo", "graphql.json")},
		}}
		result, err := NewTVO().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("tva", func(t *testing.T) {
		pageURL := "https://www.tvaplus.ca/tva/alerte-amber/saison-1/episode-01-1000036619"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.tvaplus.ca/tva/alerte-amber/saison-1/episode-01-1000036619": familyFixture(t, "tva", "page.html"),
		}}
		result, err := NewTVAPlus().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || result.Redirect.ID != "123" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://www.pgatour.com/video/features/6322506425112/x", "pgatour"},
		{"https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m", "ninenews"},
		{"https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7", "ninenow"},
		{"https://media.netapp.com/video-detail/da25fc01-82ad-5284-95bc-26920200a222/x", "netapp"},
		{"https://media.netapp.com/collection/9820e190-f2a6-47ac-9c0a-98e5e64234a4", "netapp_collection"},
		{"https://www.amc.com/shows/dark-winds/videos/x", "amcnetworks"},
		{"https://www.bbcamerica.com/shows/the-hunt/full-episodes/season-1/episode-01", "amcnetworks"},
		{"https://www.craftsy.com/class/sew-your-own-designer-handbag", "craftsy"},
		{"https://www.tvo.org/video/documentaries/the-pitch", "tvo"},
		{"https://www.tvaplus.ca/tva/le-baiser-du-barbu/le-baiser-du-barbu-886644190", "tva"},
		{"https://www.tvanouvelles.ca/videos/5117035533001", "tvanouvelles"},
		{"https://www.tvanouvelles.ca/2016/11/17/des-policiers", "tvanouvelles_article"},
		{"https://players.brightcove.net/12345/default_default/index.html?videoId=123", "brightcove"},
		{"https://example.invalid/video/1", ""},
	} {
		selected, err := registry.Select(test.rawURL)
		if test.want == "" {
			if err == nil {
				t.Fatalf("Select(%q) got %q", test.rawURL, selected.Name())
			}
			continue
		}
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestBrightcoveAdapterNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewNineNews().Extract(canceled, Request{
		URL: "https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	drmPage := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7": []byte(`{"clip":{"video":{"drm":true,"brightcoveId":"123"}}}`),
	}}
	if _, err := NewNineNow().Extract(context.Background(), Request{
		URL: "https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7", Transport: drmPage,
	}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("drm=%v", err)
	}
	auth := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.amc.com/shows/dark-winds/videos/x": []byte(`window.initialData = JSON.parse(String.raw` + "`" + `{"initialData":{"properties":{}},"config":{"brightcove":{"accountId":"12345","playerId":"default"}}}` + "`" + `)`),
	}}
	if _, err := NewAMCNetworks().Extract(context.Background(), Request{
		URL: "https://www.amc.com/shows/dark-winds/videos/x", Transport: auth,
	}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("amc drm=%v", err)
	}
	secret := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://api.media.netapp.com/client/detail/da25fc01-82ad-5284-95bc-26920200a222": {
			status: http.StatusUnauthorized, body: []byte("token=must-not-leak"),
		},
	}}
	if _, err := NewNetApp().Extract(context.Background(), Request{
		URL: "https://media.netapp.com/video-detail/da25fc01-82ad-5284-95bc-26920200a222/x", Transport: secret,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("secret=%v", err)
	}
	parsed, _ := url.Parse("https://www.pgatour.com/video/features/not-digits/slug")
	if NewPGATour().Suitable(parsed) {
		t.Fatal("expected unsuitable")
	}
}

func FuzzParsePGATourURL(f *testing.F) {
	f.Add("https://www.pgatour.com/video/features/6322506425112/slug")
	f.Add("https://www.pgatour.com/video/competition/T6322447785112/slug")
	f.Add("http://user:pass@pgatour.com/video/features/1/x")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _, _, _ = parsePGATourURL(parsed)
	})
}
