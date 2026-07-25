package extractor

import (
	"context"
	"net/url"
	"testing"
)

func breadthProgramSuccessShapes(t *testing.T) []breadthShapeSpec {
	t.Helper()
	out := make([]breadthShapeSpec, 0, 128)
	add := func(id, key, kind string, run func(*testing.T)) {
		out = append(out, breadthShapeSpec{ID: id, Key: key, Kind: kind, Run: run})
	}

	// --- Wave 1 ---
	add("cfstream-watch-hex", "cloudflarestream", "media", func(t *testing.T) {
		result, err := NewCloudflareStream().Extract(context.Background(), Request{URL: "https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1"})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("cfstream-jwt", "cloudflarestream", "media", func(t *testing.T) {
		const fixedJWT = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiI4OGQ0MTA4YTM2NDIwNzNlYWJhYWY4N2RhMTgyZDI2MyJ9.signature"
		result, err := NewCloudflareStream().Extract(context.Background(), Request{URL: "https://watch.cloudflarestream.com/" + fixedJWT})
		if err != nil {
			t.Fatal(err)
		}
		if id, ok := result.Info.ID(); !ok || id != "88d4108a3642073eabaaf87da182d263" {
			t.Fatalf("id=%q", id)
		}
	})
	add("cfstream-videodelivery", "cloudflarestream", "media", func(t *testing.T) {
		result, err := NewCloudflareStream().Extract(context.Background(), Request{URL: "https://videodelivery.net/9df17203414fd1db3e3ed74abbe936c1"})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("cfstream-customer", "cloudflarestream", "media", func(t *testing.T) {
		result, err := NewCloudflareStream().Extract(context.Background(), Request{URL: "https://customer-aw5py76sw8wyqzmh.cloudflarestream.com/2463f6d3e06fa29710a337f5f5389fd8"})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("cfstream-bytehighway", "cloudflarestream", "media", func(t *testing.T) {
		result, err := NewCloudflareStream().Extract(context.Background(), Request{URL: "https://bytehighway.net/9df17203414fd1db3e3ed74abbe936c1"})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("hytale-news", "hytale", "playlist", func(t *testing.T) {
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
	})
	add("arc-scheme", "arcpublishing", "media", func(t *testing.T) {
		result, err := NewArcPublishing().Extract(context.Background(), Request{
			URL:       "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab",
			Transport: breadthArcTransport("adn", "8c99cb6e-b29c-4bc9-9173-7bf9979225ab"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("wapo-video", "washingtonpost", "url_result", func(t *testing.T) {
		result, err := NewWashingtonPost().Extract(context.Background(), Request{
			URL: "https://www.washingtonpost.com/video/c/video/480ba4ee-1ec7-11e6-82c2-a7dcb313287d",
		})
		if err != nil {
			t.Fatal(err)
		}
		assertURLResultReentry(t, result, "arcpublishing", NewArcPublishing(), breadthArcTransport("wapo", "480ba4ee-1ec7-11e6-82c2-a7dcb313287d"))
	})

	add("adn-single", "adn", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://adn.com/politics/2020/11/02/video-senate-candidates/": familyFixture(t, "adn", "powa.html"),
		}}
		result, err := NewADN().Extract(context.Background(), Request{URL: "https://www.adn.com/politics/2020/11/02/video-senate-candidates/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("adn", "8c99cb6e-b29c-4bc9-9173-7bf9979225ab")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("adn-multi", "adn", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://adn.com/politics/2020/11/02/video-senate-candidates/": arcMultiPowaHTML("adn", "8c99cb6e-b29c-4bc9-9173-7bf9979225ab", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewADN().Extract(context.Background(), Request{URL: "https://www.adn.com/politics/2020/11/02/video-senate-candidates/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("bostonglobe-single", "bostonglobe", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://bostonglobe.com/video/2020/12/30/metro/example/": familyFixture(t, "bostonglobe", "powa.html"),
		}}
		result, err := NewBostonGlobe().Extract(context.Background(), Request{URL: "https://www.bostonglobe.com/video/2020/12/30/metro/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:bostonglobe:232b7ae6-7d73-432d-bc0a-85dbf0119ab1" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("bostonglobe", "232b7ae6-7d73-432d-bc0a-85dbf0119ab1")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("bostonglobe-multi", "bostonglobe", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://bostonglobe.com/video/2020/12/30/metro/example/": arcMultiPowaHTML("bostonglobe", "232b7ae6-7d73-432d-bc0a-85dbf0119ab1", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewBostonGlobe().Extract(context.Background(), Request{URL: "https://www.bostonglobe.com/video/2020/12/30/metro/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("gray-single", "gray", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://wabi.tv/video/2020/12/30/example/": familyFixture(t, "gray", "powa.html"),
		}}
		result, err := NewGray().Extract(context.Background(), Request{URL: "https://www.wabi.tv/video/2020/12/30/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:gray:0b0ba30e-032a-4598-8810-901d70e6033e" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("gray", "0b0ba30e-032a-4598-8810-901d70e6033e")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("gray-multi", "gray", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://wabi.tv/video/2020/12/30/example/": arcMultiPowaHTML("gray", "0b0ba30e-032a-4598-8810-901d70e6033e", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewGray().Extract(context.Background(), Request{URL: "https://www.wabi.tv/video/2020/12/30/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("clickondetroit-single", "clickondetroit", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://clickondetroit.com/video/community/2020/05/15/example/": familyFixture(t, "clickondetroit", "powa.html"),
		}}
		result, err := NewClickOnDetroit().Extract(context.Background(), Request{URL: "https://www.clickondetroit.com/video/community/2020/05/15/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:gmg:c8793fb2-8d44-4242-881e-2db31da2d9fe" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("gmg", "c8793fb2-8d44-4242-881e-2db31da2d9fe")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("clickondetroit-multi", "clickondetroit", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://clickondetroit.com/video/community/2020/05/15/example/": arcMultiPowaHTML("gmg", "c8793fb2-8d44-4242-881e-2db31da2d9fe", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewClickOnDetroit().Extract(context.Background(), Request{URL: "https://www.clickondetroit.com/video/community/2020/05/15/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("actionnewsjax-single", "actionnewsjax", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://actionnewsjax.com/video/live-stream/": familyFixture(t, "actionnewsjax", "powa.html"),
		}}
		result, err := NewActionNewsJax().Extract(context.Background(), Request{URL: "https://www.actionnewsjax.com/video/live-stream/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:cmg:cfb1cf1b-3ab5-4d1b-86c5-a5515d311f2a" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("cmg", "cfb1cf1b-3ab5-4d1b-86c5-a5515d311f2a")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("actionnewsjax-multi", "actionnewsjax", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://actionnewsjax.com/video/live-stream/": arcMultiPowaHTML("cmg", "cfb1cf1b-3ab5-4d1b-86c5-a5515d311f2a", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewActionNewsJax().Extract(context.Background(), Request{URL: "https://www.actionnewsjax.com/video/live-stream/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("elcomercio-single", "elcomercio", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://elcomercio.pe/videos/deportes/example/": familyFixture(t, "elcomercio", "powa.html"),
		}}
		result, err := NewElComercio().Extract(context.Background(), Request{URL: "https://www.elcomercio.pe/videos/deportes/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:elcomercio:27a7e1f8-2ec7-4177-874f-a4feed2885b3" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("elcomercio", "27a7e1f8-2ec7-4177-874f-a4feed2885b3")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("elcomercio-multi", "elcomercio", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://elcomercio.pe/videos/deportes/example/": arcMultiPowaHTML("elcomercio", "27a7e1f8-2ec7-4177-874f-a4feed2885b3", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewElComercio().Extract(context.Background(), Request{URL: "https://www.elcomercio.pe/videos/deportes/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("lateja-single", "lateja", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/": familyFixture(t, "lateja", "powa.html"),
		}}
		result, err := NewLateja().Extract(context.Background(), Request{URL: "https://www.lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:gruponacion:dfcbfa57-527f-45ff-a69b-35fe71054143" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("gruponacion", "dfcbfa57-527f-45ff-a69b-35fe71054143")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("lateja-multi", "lateja", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/": arcMultiPowaHTML("gruponacion", "dfcbfa57-527f-45ff-a69b-35fe71054143", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewLateja().Extract(context.Background(), Request{URL: "https://www.lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("fifthdomain-single", "fifthdomain", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://fifthdomain.com/video/2018/03/09/example/": familyFixture(t, "fifthdomain", "powa.html"),
		}}
		result, err := NewFifthDomain().Extract(context.Background(), Request{URL: "https://www.fifthdomain.com/video/2018/03/09/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:mco:aa0ca6fe-1127-46d4-b32c-be0d6fdb8055" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("mco", "aa0ca6fe-1127-46d4-b32c-be0d6fdb8055")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("fifthdomain-multi", "fifthdomain", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://fifthdomain.com/video/2018/03/09/example/": arcMultiPowaHTML("mco", "aa0ca6fe-1127-46d4-b32c-be0d6fdb8055", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewFifthDomain().Extract(context.Background(), Request{URL: "https://www.fifthdomain.com/video/2018/03/09/example/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("vlno-single", "vlno", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://vl.no/kultur/2020/12/09/example-article/": familyFixture(t, "vlno", "powa.html"),
		}}
		result, err := NewVLNO().Extract(context.Background(), Request{URL: "https://www.vl.no/kultur/2020/12/09/example-article/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:mentormedier:47a12084-650b-4011-bfd0-3699b6947b2d" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("mentormedier", "47a12084-650b-4011-bfd0-3699b6947b2d")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("vlno-multi", "vlno", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://vl.no/kultur/2020/12/09/example-article/": arcMultiPowaHTML("mentormedier", "47a12084-650b-4011-bfd0-3699b6947b2d", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewVLNO().Extract(context.Background(), Request{URL: "https://www.vl.no/kultur/2020/12/09/example-article/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("fourteennews-single", "fourteennews", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://14news.com/2020/12/30/whiskey-theft/": familyFixture(t, "fourteennews", "powa.html"),
		}}
		result, err := NewFourteenNews().Extract(context.Background(), Request{URL: "https://www.14news.com/2020/12/30/whiskey-theft/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:raycom:b89f61f8-79fa-4c09-8255-e64237119bf7" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("raycom", "b89f61f8-79fa-4c09-8255-e64237119bf7")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("fourteennews-multi", "fourteennews", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://14news.com/2020/12/30/whiskey-theft/": arcMultiPowaHTML("raycom", "b89f61f8-79fa-4c09-8255-e64237119bf7", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewFourteenNews().Extract(context.Background(), Request{URL: "https://www.14news.com/2020/12/30/whiskey-theft/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("globeandmail-single", "globeandmail", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://theglobeandmail.com/world/video-ethiopian-woman/": familyFixture(t, "globeandmail", "powa.html"),
		}}
		result, err := NewGlobeAndMail().Extract(context.Background(), Request{URL: "https://www.theglobeandmail.com/world/video-ethiopian-woman/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:tgam:411b34c1-8701-4036-9831-26964711664b" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("tgam", "411b34c1-8701-4036-9831-26964711664b")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("globeandmail-multi", "globeandmail", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://theglobeandmail.com/world/video-ethiopian-woman/": arcMultiPowaHTML("tgam", "411b34c1-8701-4036-9831-26964711664b", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewGlobeAndMail().Extract(context.Background(), Request{URL: "https://www.theglobeandmail.com/world/video-ethiopian-woman/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("pilotonline-single", "pilotonline", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html": familyFixture(t, "pilotonline", "powa.html"),
		}}
		result, err := NewPilotOnline().Extract(context.Background(), Request{URL: "https://www.pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:tronc:460f2931-8130-4719-8ea1-ffcb2d7cb685" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("tronc", "460f2931-8130-4719-8ea1-ffcb2d7cb685")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("pilotonline-multi", "pilotonline", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html": arcMultiPowaHTML("tronc", "460f2931-8130-4719-8ea1-ffcb2d7cb685", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewPilotOnline().Extract(context.Background(), Request{URL: "https://www.pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})
	add("uppermichigansource-single", "uppermichigansource", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://uppermichigansource.com/2025/07/18/scattered-showers/": familyFixture(t, "uppermichigansource", "powa.html"),
		}}
		result, err := NewUpperMichiganSource().Extract(context.Background(), Request{URL: "https://www.uppermichigansource.com/2025/07/18/scattered-showers/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		entries := assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 1)
		if entries[0].URL != "arcpublishing:gray:508116f7-e999-48db-b7c2-60a04842679b" {
			t.Fatalf("%s", entries[0].URL)
		}
		media, err := NewArcPublishing().Extract(context.Background(), Request{URL: entries[0].URL, Transport: breadthArcTransport("gray", "508116f7-e999-48db-b7c2-60a04842679b")})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("re-entry formats")
		}
	})
	add("uppermichigansource-multi", "uppermichigansource", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://uppermichigansource.com/2025/07/18/scattered-showers/": arcMultiPowaHTML("gray", "508116f7-e999-48db-b7c2-60a04842679b", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		}}
		result, err := NewUpperMichiganSource().Extract(context.Background(), Request{URL: "https://www.uppermichigansource.com/2025/07/18/scattered-showers/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, arcMaxOrgs, "arcpublishing", 2)
	})

	add("anvato-scheme", "anvato", "media", func(t *testing.T) {
		result, err := NewAnvato().Extract(context.Background(), Request{
			URL: "anvato:" + fox9AnvatoAccessKey + ":8032455", Transport: breadthAnvatoTransport(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("fox9-video", "fox9", "url_result", func(t *testing.T) {
		result, err := NewFOX9().Extract(context.Background(), Request{URL: "https://www.fox9.com/video/8032455"})
		if err != nil {
			t.Fatal(err)
		}
		assertURLResultReentry(t, result, "anvato", NewAnvato(), breadthAnvatoTransport())
	})
	add("fox9-news", "fox9_news", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.fox9.com/news/bear-climbs-tree": familyFixture(t, "fox9", "news.html"),
		}}
		result, err := NewFOX9News().Extract(context.Background(), Request{URL: "https://www.fox9.com/news/bear-climbs-tree", Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "fox9" || result.Redirect.ID != "314473" {
			t.Fatalf("%#v %v", result, err)
		}
		fox, err := NewFOX9().Extract(context.Background(), Request{URL: result.Redirect.URL})
		if err != nil || !fox.IsURL() || fox.Redirect.ExtractorKey != "anvato" {
			t.Fatalf("%#v %v", fox, err)
		}
		videoDataURL := anvatoAPIBase + "/mcp/video/314473?anvack=" + url.QueryEscape(fox9AnvatoAccessKey)
		auth314 := anvatoAdstAuth(videoDataURL, 1700000000)
		q314 := url.Values{}
		q314.Set("anvack", fox9AnvatoAccessKey)
		q314.Set("X-Anvato-Adst-Auth", auth314)
		q314.Set("rtyp", "fp")
		serverEndpoint := anvatoAPIBase + "/server_time?anvack=" + url.QueryEscape(fox9AnvatoAccessKey)
		videoEndpoint := anvatoAPIBase + "/mcp/video/314473?" + q314.Encode()
		anvTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
			videoEndpoint:  {body: familyFixture(t, "anvato", "video.json")},
		}}
		media, err := NewAnvato().Extract(context.Background(), Request{URL: fox.Redirect.URL, Transport: anvTransport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("theplatform-link", "theplatform", "media", func(t *testing.T) {
		rawURL := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT"
		smilURL := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT?mbr=true&format=SMIL"
		metaURL := "https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT?format=preview"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			smilURL: {body: familyFixture(t, "theplatform", "media.smil")},
			metaURL: {body: familyFixture(t, "theplatform", "preview.json")},
		}}
		result, err := NewThePlatform().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("theplatform-feed-byguid", "theplatform_feed", "media", func(t *testing.T) {
		feedURL := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?byGuid=n_hardball_5biden_140207"
		feedEndpoint := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?form=json&byGuid=n_hardball_5biden_140207"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			feedEndpoint: {body: familyFixture(t, "theplatform", "feed.json")},
		}}
		result, err := NewThePlatformFeed().Extract(context.Background(), Request{URL: feedURL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("weathercom-video", "weathercom", "media", func(t *testing.T) {
		weatherURL := "https://weather.com/storms/hurricane/video/invest-95l-fixture"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://weather.com/api/v1/p/redux-dal": {body: familyFixture(t, "weathercom", "redux.json")},
		}}
		result, err := NewWeatherCom().Extract(context.Background(), Request{URL: weatherURL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("panopto-video", "panopto", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://demo.hosted.panopto.com/Panopto/Pages/Viewer/DeliveryInfo.aspx?deliveryId=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb&responseType=json": {
				body: familyFixture(t, "panopto", "deliveryinfo.json"),
			},
		}}
		result, err := NewPanopto().Extract(context.Background(), Request{
			URL: "https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("panopto-embed", "panopto", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://demo.hosted.panopto.com/Panopto/Pages/Viewer/DeliveryInfo.aspx?deliveryId=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb&responseType=json": {
				body: familyFixture(t, "panopto", "deliveryinfo.json"),
			},
		}}
		result, err := NewPanopto().Extract(context.Background(), Request{
			URL: "https://demo.hosted.panopto.com/Panopto/Pages/Embed.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("nbcolympics-player", "nbcolympics", "url_result", func(t *testing.T) {
		result, err := NewNBCOlympics().Extract(context.Background(), Request{
			URL: "https://vplayer.nbcolympics.com/p/NnzsPC/widget/select/media/4Y0TlYUr_ZT7",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsURL() || result.Redirect.ExtractorKey != "theplatform" {
			t.Fatalf("%#v", result)
		}
	})

	// --- Brightcove adapters ---
	add("pgatour-features", "pgatour", "url_result", func(t *testing.T) {
		result, err := NewPGATour().Extract(context.Background(), Request{URL: "https://www.pgatour.com/video/features/6322506425112/follow-the-players-trophy"})
		if err != nil {
			t.Fatal(err)
		}
		assertURLResultReentry(t, result, "brightcove", NewBrightcove(), brightcoveFixtureTransport(t, pgaTourFeaturesAccount, pgaTourFeaturesPlayer, "6322506425112"))
	})
	add("pgatour-competition", "pgatour", "url_result", func(t *testing.T) {
		result, err := NewPGATour().Extract(context.Background(), Request{URL: "https://www.pgatour.com/video/competition/T6322447785112/adam-hadwin"})
		if err != nil {
			t.Fatal(err)
		}
		assertURLResultReentry(t, result, "brightcove", NewBrightcove(), brightcoveFixtureTransport(t, pgaTourCloudcastAccount, pgaTourCloudcastPlayer, "6322447785112"))
	})
	add("ninenews-videos", "ninenews", "url_result", func(t *testing.T) {
		pageURL := "https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m"
		transport := &sharedFixtureTransport{pages: map[string][]byte{pageURL: familyFixture(t, "ninenews", "page.html")}}
		result, err := NewNineNews().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v", result)
		}
	})
	add("ninenow-clip", "ninenow", "url_result", func(t *testing.T) {
		pageURL := "https://www.9now.com.au/today/season-2025/clip-cm8hw9h5z00080hquqa5hszq7"
		transport := &sharedFixtureTransport{pages: map[string][]byte{pageURL: familyFixture(t, "ninenow", "page.html")}}
		result, err := NewNineNow().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("netapp-detail", "netapp", "url_result", func(t *testing.T) {
		uuid := "da25fc01-82ad-5284-95bc-26920200a222"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.media.netapp.com/client/detail/" + uuid: {body: familyFixture(t, "netapp", "detail.json")},
		}}
		result, err := NewNetApp().Extract(context.Background(), Request{URL: "https://media.netapp.com/video-detail/" + uuid + "/seamless", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertURLResultReentry(t, result, "brightcove", NewBrightcove(), brightcoveFixtureTransport(t, netAppBrightcoveAccount, "default", "123"))
	})
	add("netapp-collection", "netapp_collection", "playlist", func(t *testing.T) {
		uuid := "9820e190-f2a6-47ac-9c0a-98e5e64234a4"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.media.netapp.com/client/collection/" + uuid: {body: familyFixture(t, "netapp_collection", "collection.json")},
		}}
		result, err := NewNetAppCollection().Extract(context.Background(), Request{URL: "https://media.netapp.com/collection/" + uuid, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, brightcoveAdapterMaxEntries, "brightcove", 2)
	})
	add("amcnetworks-amc", "amcnetworks", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.amc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027": familyFixture(t, "amcnetworks", "page.html"),
		}}
		result, err := NewAMCNetworks().Extract(context.Background(), Request{URL: "https://www.amc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027", Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("amcnetworks-bbcamerica", "amcnetworks", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.bbcamerica.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027": familyFixture(t, "amcnetworks", "page.html"),
		}}
		result, err := NewAMCNetworks().Extract(context.Background(), Request{URL: "https://www.bbcamerica.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027", Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("amcnetworks-ifc", "amcnetworks", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.ifc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027": familyFixture(t, "amcnetworks", "page.html"),
		}}
		result, err := NewAMCNetworks().Extract(context.Background(), Request{URL: "https://www.ifc.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027", Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("amcnetworks-wetv", "amcnetworks", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.wetv.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027": familyFixture(t, "amcnetworks", "page.html"),
		}}
		result, err := NewAMCNetworks().Extract(context.Background(), Request{URL: "https://www.wetv.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027", Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("amcnetworks-sundancetv", "amcnetworks", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.sundancetv.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027": familyFixture(t, "amcnetworks", "page.html"),
		}}
		result, err := NewAMCNetworks().Extract(context.Background(), Request{URL: "https://www.sundancetv.com/shows/dark-winds/videos/dark-winds-a-look-at-season-3--1072027", Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	add("craftsy-class", "craftsy", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.craftsy.com/class/the-midnight-quilt-show-season-5/": familyFixture(t, "craftsy", "page.html"),
		}}
		result, err := NewCraftsy().Extract(context.Background(), Request{URL: "https://www.craftsy.com/class/the-midnight-quilt-show-season-5", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, brightcoveAdapterMaxEntries, "brightcove", 2)
	})
	add("tvo-video", "tvo", "url_result", func(t *testing.T) {
		pageURL := "https://www.tvo.org/video/how-can-ontario-survive-the-trade-war"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://hmy0rc1bo2.execute-api.ca-central-1.amazonaws.com/graphql": {body: familyFixture(t, "tvo", "graphql.json")},
		}}
		result, err := NewTVO().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("tva-plus", "tva", "url_result", func(t *testing.T) {
		pageURL := "https://www.tvaplus.ca/tva/alerte-amber/saison-1/episode-01-1000036619"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.tvaplus.ca/tva/alerte-amber/saison-1/episode-01-1000036619": familyFixture(t, "tva", "page.html"),
		}}
		result, err := NewTVAPlus().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("tvanouvelles-videos", "tvanouvelles", "url_result", func(t *testing.T) {
		result, err := NewTVANouvelles().Extract(context.Background(), Request{URL: "https://www.tvanouvelles.ca/videos/5117035533001"})
		if err != nil {
			t.Fatal(err)
		}
		assertURLResultReentry(t, result, "brightcove", NewBrightcove(), brightcoveFixtureTransport(t, tvaNouvellesAccount, "default", "5117035533001"))
	})
	add("tvanouvelles-article", "tvanouvelles_article", "playlist", func(t *testing.T) {
		pageURL := "https://www.tvanouvelles.ca/2016/11/17/des-policiers-qui-ont-la-meche-un-peu-courte"
		transport := &sharedFixtureTransport{pages: map[string][]byte{pageURL: familyFixture(t, "tvanouvelles_article", "page.html")}}
		result, err := NewTVANouvellesArticle().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, brightcoveAdapterMaxEntries, "tvanouvelles", 2)
	})

	add("unwebtv-asset", "unitednationswebtv", "url_result", func(t *testing.T) {
		pageURL := "https://webtv.un.org/en/asset/k1o/k1o7stmi6p"
		transport := &sharedFixtureTransport{pages: map[string][]byte{pageURL: familyFixture(t, "unitednationswebtv", "page.html")}}
		result, err := NewUnitedNationsWebTV().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || result.Redirect.URL != "kaltura:123:1_abcd1234" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("azmedien-fragment", "azmedien", "url_result", func(t *testing.T) {
		result, err := NewAZMedien().Extract(context.Background(), Request{URL: "https://www.telebaern.tv/telebaern-news/montag-1-oktober-2018-ganze-sendung-133531189#video=0_7xjo9lf1"})
		if err != nil || result.Redirect.URL != "kaltura:1719221:0_7xjo9lf1" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("azmedien-telezueri", "azmedien", "url_result", func(t *testing.T) {
		rawURL := "https://tv.telezueri.ch/sonntalk/bundesrats-vakanzen-eu-rahmenabkommen-133214569"
		transport := &sharedFixtureTransport{pages: map[string][]byte{rawURL: familyFixture(t, "azmedien", "page.html")}}
		result, err := NewAZMedien().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("azmedien-telebaern-page", "azmedien", "url_result", func(t *testing.T) {
		rawURL := "https://www.telebaern.tv/telebaern-news/fixture-page-133214569"
		transport := &sharedFixtureTransport{pages: map[string][]byte{rawURL: familyFixture(t, "azmedien", "page.html")}}
		result, err := NewAZMedien().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("inc-article", "inc", "url_result", func(t *testing.T) {
		rawURL := "https://www.inc.com/tip-sheet/bill-gates-says-these-5-books-will-make-you-smarter.html"
		transport := &sharedFixtureTransport{pages: map[string][]byte{rawURL: familyFixture(t, "inc", "page.html")}}
		result, err := NewInc().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("heise-video", "heise", "url_result", func(t *testing.T) {
		rawURL := "https://www.heise.de/video/artikel/Podcast-c-t-uplink-3-3-Owncloud-2404147.html"
		transport := &sharedFixtureTransport{pages: map[string][]byte{rawURL: familyFixture(t, "heise", "page.html")}}
		result, err := NewHeise().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("spiegel-video", "spiegel", "url_result", func(t *testing.T) {
		rawURL := "https://www.spiegel.de/video/vulkan-tungurahua-in-ecuador-ist-wieder-aktiv-video-1259285.html"
		transport := &sharedFixtureTransport{pages: map[string][]byte{rawURL: familyFixture(t, "spiegel", "page.html")}}
		result, err := NewSpiegel().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:AbCd1234" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("spiegel-manager", "spiegel", "url_result", func(t *testing.T) {
		rawURL := "https://www.manager-magazin.de/unternehmen/video-aae8df48-43c1-4c61-867d-23f0a2d254b7"
		transport := &sharedFixtureTransport{pages: map[string][]byte{rawURL: familyFixture(t, "spiegel", "page.html")}}
		result, err := NewSpiegel().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("onefootball-video", "onefootball", "url_result", func(t *testing.T) {
		rawURL := "https://onefootball.com/en/video/highlights-fc-zuerich-3-3-fc-basel-34012334"
		transport := &sharedFixtureTransport{pages: map[string][]byte{rawURL: familyFixture(t, "onefootball", "page.html")}}
		result, err := NewOneFootball().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})

	// Podcasts
	add("acast-shows", "acast", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://feeder.acast.com/api/v1/shows/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna?showInfo=true": {body: familyFixture(t, "acast", "episode.json")},
		}}
		result, err := NewACast().Extract(context.Background(), Request{URL: "https://shows.acast.com/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("acast-play", "acast", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://feeder.acast.com/api/v1/shows/sparpodcast/episodes/2a92b283-1a75-4ad8-8396-499c641de0d9?showInfo=true": {body: familyFixture(t, "acast", "episode.json")},
		}}
		result, err := NewACast().Extract(context.Background(), Request{URL: "https://play.acast.com/s/sparpodcast/2a92b283-1a75-4ad8-8396-499c641de0d9", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("acast-www", "acast", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://feeder.acast.com/api/v1/shows/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna?showInfo=true": {body: familyFixture(t, "acast", "episode.json")},
		}}
		result, err := NewACast().Extract(context.Background(), Request{URL: "https://www.acast.com/sparpodcast/2.raggarmordet-rosterurdetforflutna", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("acast-channel-www", "acast_channel", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://feeder.acast.com/api/v1/shows/todayinfocus": {body: familyFixture(t, "acast_channel", "show.json")},
		}}
		result, err := NewACastChannel().Extract(context.Background(), Request{URL: "https://www.acast.com/todayinfocus", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "acast", 2)
	})
	add("acast-channel-shows", "acast_channel", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://feeder.acast.com/api/v1/shows/todayinfocus": {body: familyFixture(t, "acast_channel", "show.json")},
		}}
		result, err := NewACastChannel().Extract(context.Background(), Request{URL: "https://shows.acast.com/todayinfocus", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "acast", 2)
	})
	add("simplecast-player", "simplecast", "media", func(t *testing.T) {
		id := "b6dc49a2-9404-4853-9aa9-9cfc097be876"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.simplecast.com/episodes/" + id: {body: familyFixture(t, "simplecast", "episode.json")},
		}}
		result, err := NewSimplecast().Extract(context.Background(), Request{URL: "https://player.simplecast.com/" + id, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("simplecast-api", "simplecast", "media", func(t *testing.T) {
		id := "b6dc49a2-9404-4853-9aa9-9cfc097be876"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.simplecast.com/episodes/" + id: {body: familyFixture(t, "simplecast", "episode.json")},
		}}
		result, err := NewSimplecast().Extract(context.Background(), Request{URL: "https://api.simplecast.com/episodes/" + id, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("simplecast-episode", "simplecast_episode", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.simplecast.com/episodes/search": {body: familyFixture(t, "simplecast_episode", "search.json")},
		}}
		result, err := NewSimplecastEpisode().Extract(context.Background(), Request{URL: "https://the-re-bind-io-podcast.simplecast.com/episodes/errant-signal", Transport: transport})
		if err != nil || result.Redirect.ExtractorKey != "simplecast" {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("simplecast-podcast", "simplecast_podcast", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.simplecast.com/podcasts/search":                                        {body: familyFixture(t, "simplecast_podcast", "search.json")},
			"https://api.simplecast.com/podcasts/e23df0da-bae4-4531-8bbf-71364a88dc13/episodes": {body: familyFixture(t, "simplecast_podcast", "episodes.json")},
		}}
		result, err := NewSimplecastPodcast().Extract(context.Background(), Request{URL: "https://the-re-bind-io-podcast.simplecast.com/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "simplecast", 1)
	})
	add("megaphone-player", "megaphone", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://player.megaphone.fm/GLT9749789991": familyFixture(t, "megaphone", "page.html"),
		}}
		result, err := NewMegaphone().Extract(context.Background(), Request{URL: "https://player.megaphone.fm/GLT9749789991", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("art19-rss", "art19", "media", func(t *testing.T) {
		id := "5ba1413c-48b8-472b-9cc3-cfd952340bdb"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://art19.com/episodes/" + id: {body: familyFixture(t, "art19", "episode.json")},
		}}
		result, err := NewArt19().Extract(context.Background(), Request{URL: "https://rss.art19.com/episodes/" + id + ".mp3", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("art19-show-episode", "art19", "media", func(t *testing.T) {
		id := "5ba1413c-48b8-472b-9cc3-cfd952340bdb"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://art19.com/episodes/" + id: {body: familyFixture(t, "art19", "episode.json")},
		}}
		result, err := NewArt19().Extract(context.Background(), Request{URL: "https://art19.com/shows/scamfluencers/episodes/" + id, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("art19-show", "art19_show", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://art19.com/shows/scamfluencers": {body: familyFixture(t, "art19_show", "show.json")},
		}}
		result, err := NewArt19Show().Extract(context.Background(), Request{URL: "https://art19.com/shows/scamfluencers", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "art19", 1)
	})
	add("libsyn-embed", "libsyn", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://html5-player.libsyn.com/embed/episode/id/6385796": familyFixture(t, "libsyn", "page.html"),
		}}
		result, err := NewLibsyn().Extract(context.Background(), Request{URL: "https://html5-player.libsyn.com/embed/episode/id/6385796/", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("spreaker-api", "spreaker", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.spreaker.com/v2/episodes/12534508": {body: familyFixture(t, "spreaker", "episode.json")},
		}}
		result, err := NewSpreaker().Extract(context.Background(), Request{URL: "https://api.spreaker.com/episode/12534508", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("spreaker-www", "spreaker", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.spreaker.com/v2/episodes/60269615": {body: familyFixture(t, "spreaker", "episode.json")},
		}}
		result, err := NewSpreaker().Extract(context.Background(), Request{URL: "https://www.spreaker.com/episode/60269615", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("spreaker-show-api", "spreaker_show", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.spreaker.com/show/4652058/episodes?page=1&max_per_page=100": {body: familyFixture(t, "spreaker_show", "episodes.json")},
		}}
		result, err := NewSpreakerShow().Extract(context.Background(), Request{URL: "https://api.spreaker.com/show/4652058", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "spreaker", 1)
	})
	add("spreaker-show-www", "spreaker_show", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.spreaker.com/show/5918323/episodes?page=1&max_per_page=100": {body: familyFixture(t, "spreaker_show", "episodes.json")},
		}}
		result, err := NewSpreakerShow().Extract(context.Background(), Request{URL: "https://www.spreaker.com/podcast/health-wealth--5918323", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, podcastMaxEpisodes, "spreaker", 1)
	})

	add("nowness-story", "nowness", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{
			responses: map[string]fixtureHTTP{"https://api.nowness.com/api/post/getBySlug/candor-the-art-of-gesticulation": {body: familyFixture(t, "nowness", "post.json")}},
			pages:     map[string][]byte{"https://www.nowness.com/iframe?id=2520295746001": familyFixture(t, "nowness", "iframe.html")},
		}
		result, err := NewNowness().Extract(context.Background(), Request{URL: "https://www.nowness.com/story/candor-the-art-of-gesticulation", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertURLResultReentry(t, result, "brightcove", NewBrightcove(), brightcoveFixtureTransport(t, "2385340575001", "default", "2520295746001"))
	})
	add("nowness-series-story", "nowness", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{
			responses: map[string]fixtureHTTP{"https://api.nowness.com/api/post/getBySlug/jean-luc-godard-supercut": {body: familyFixture(t, "nowness", "post.json")}},
			pages:     map[string][]byte{"https://www.nowness.com/iframe?id=2520295746001": familyFixture(t, "nowness", "iframe.html")},
		}
		result, err := NewNowness().Extract(context.Background(), Request{URL: "https://www.nowness.com/series/nowness-picks/jean-luc-godard-supercut", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsURL() {
			t.Fatalf("%#v", result)
		}
	})
	add("nowness-cn-story", "nowness", "url_result", func(t *testing.T) {
		transport := &sharedFixtureTransport{
			responses: map[string]fixtureHTTP{"https://api.nowness.com/api/post/getBySlug/candor-the-art-of-gesticulation": {body: familyFixture(t, "nowness", "post.json")}},
			pages:     map[string][]byte{"https://www.nowness.com/iframe?id=2520295746001": familyFixture(t, "nowness", "iframe.html")},
		}
		result, err := NewNowness().Extract(context.Background(), Request{URL: "https://cn.nowness.com/story/candor-the-art-of-gesticulation", Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("nowness-playlist", "nowness_playlist", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.nowness.com/api/post?PlaylistId=3286": {body: familyFixture(t, "nowness_playlist", "playlist.json")},
		}}
		result, err := NewNownessPlaylist().Extract(context.Background(), Request{URL: "https://www.nowness.com/playlist/3286/blues", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, nownessMaxEntries, "nowness", 2)
	})
	add("nowness-series", "nowness_series", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.nowness.com/api/series/getBySlug/60-seconds": {body: familyFixture(t, "nowness_series", "series.json")},
		}}
		result, err := NewNownessSeries().Extract(context.Background(), Request{URL: "https://www.nowness.com/series/60-seconds", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, nownessMaxPosts, "nowness", 2)
	})
	add("dacast-vod", "dacast", "media", func(t *testing.T) {
		user := "acae82153ef4d7a7344ae4eaa86af534"
		vid := "1c6143e3-5a06-371d-8695-19b96ea49090"
		contentID := user + "-vod-" + vid
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://playback.dacast.com/content/info?contentId=" + contentID + "&provider=universe":   {body: familyFixture(t, "dacast", "info.json")},
			"https://playback.dacast.com/content/access?contentId=" + contentID + "&provider=universe": {body: familyFixture(t, "dacast", "access.json")},
		}}
		result, err := NewDacast().Extract(context.Background(), Request{URL: "https://iframe.dacast.com/vod/" + user + "/" + vid, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("dacast-playlist", "dacast_playlist", "playlist", func(t *testing.T) {
		plUser := "943bb1ab3c03695ba85330d92d6d226e"
		plID := "b632eb053cac17a9c9a02bcfc827f2d8"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://playback.dacast.com/content/info?contentId=" + plUser + "-playlist-" + plID + "&provider=universe": {body: familyFixture(t, "dacast_playlist", "info.json")},
		}}
		result, err := NewDacastPlaylist().Extract(context.Background(), Request{URL: "https://iframe.dacast.com/playlist/" + plUser + "/" + plID, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, dacastMaxPlaylistEntries, "dacast", 2)
	})
	add("panopto-viewer", "panopto", "media", func(t *testing.T) {
		vid := "26b3ae9e-4a48-4dcc-96ba-0befba08a0fb"
		host := "demo.hosted.panopto.com"
		endpoint := "https://" + host + "/Panopto/Pages/Viewer/DeliveryInfo.aspx?deliveryId=" + vid + "&responseType=json"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			endpoint: {body: familyFixture(t, "panopto", "deliveryinfo.json")},
		}}
		result, err := NewPanopto().Extract(context.Background(), Request{
			URL: "https://" + host + "/Panopto/Pages/Viewer.aspx?id=" + vid, Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("panopto-playlist", "panopto_playlist", "playlist", func(t *testing.T) {
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
		assertLazyPlaylist(t, result, transport, panoptoMaxEntries, "panopto", 2)
	})

	add("theplatform-feed-byid", "theplatform_feed", "media", func(t *testing.T) {
		feedURL := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?byId=n_hardball_5biden_140207"
		feedEndpoint := "https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?form=json&byId=n_hardball_5biden_140207"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			feedEndpoint: {body: familyFixture(t, "theplatform", "feed.json")},
		}}
		result, err := NewThePlatformFeed().Extract(context.Background(), Request{URL: feedURL, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("cfstream-cloudflarestream-com", "cloudflarestream", "media", func(t *testing.T) {
		result, err := NewCloudflareStream().Extract(context.Background(), Request{URL: "https://cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1"})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("cfstream-embed-js", "cloudflarestream", "media", func(t *testing.T) {
		result, err := NewCloudflareStream().Extract(context.Background(), Request{URL: "https://embed.cloudflarestream.com/embed/we4g.fla9.latest.js?video=31c9291ab41fac05471db4e73aa11717"})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("acast-embed", "acast", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://feeder.acast.com/api/v1/shows/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna?showInfo=true": {body: familyFixture(t, "acast", "episode.json")},
		}}
		result, err := NewACast().Extract(context.Background(), Request{URL: "https://embed.acast.com/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("tvo-documentaries", "tvo", "url_result", func(t *testing.T) {
		pageURL := "https://www.tvo.org/video/documentaries/how-can-ontario-survive-the-trade-war"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://hmy0rc1bo2.execute-api.ca-central-1.amazonaws.com/graphql": {body: familyFixture(t, "tvo", "graphql.json")},
		}}
		result, err := NewTVO().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
	})
	add("spreaker-v2", "spreaker", "media", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.spreaker.com/v2/episodes/12534508": {body: familyFixture(t, "spreaker", "episode.json")},
		}}
		result, err := NewSpreaker().Extract(context.Background(), Request{URL: "https://api.spreaker.com/v2/episodes/12534508", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("nowness-playlist-bare", "nowness_playlist", "playlist", func(t *testing.T) {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://api.nowness.com/api/post?PlaylistId=3286": {body: familyFixture(t, "nowness_playlist", "playlist.json")},
		}}
		result, err := NewNownessPlaylist().Extract(context.Background(), Request{URL: "https://www.nowness.com/playlist/3286", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		assertLazyPlaylist(t, result, transport, nownessMaxEntries, "nowness", 2)
	})
	add("dacast-vod-alt", "dacast", "media", func(t *testing.T) {
		user := "f9823fc6-faba-b98f-0d00-4a7b50a58c5b"
		vid := "348c5c84-b6af-4859-bb9d-1d01009c795b"
		contentID := user + "-vod-" + vid
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			"https://playback.dacast.com/content/info?contentId=" + contentID + "&provider=universe":   {body: familyFixture(t, "dacast", "info.json")},
			"https://playback.dacast.com/content/access?contentId=" + contentID + "&provider=universe": {body: familyFixture(t, "dacast", "access.json")},
		}}
		result, err := NewDacast().Extract(context.Background(), Request{URL: "https://iframe.dacast.com/vod/" + user + "/" + vid, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := result.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})
	add("nbcolympics-reentry", "nbcolympics", "url_result", func(t *testing.T) {
		nbc := "https://vplayer.nbcolympics.com/p/NnzsPC/widget/select/media/4Y0TlYUr_ZT7"
		result, err := NewNBCOlympics().Extract(context.Background(), Request{URL: nbc})
		if err != nil || !result.IsURL() {
			t.Fatalf("%#v %v", result, err)
		}
		smilURL := "https://link.theplatform.com/s/NnzsPC/media/4Y0TlYUr_ZT7?mbr=true&format=SMIL"
		metaURL := "https://link.theplatform.com/s/NnzsPC/media/4Y0TlYUr_ZT7?format=preview"
		tpTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			smilURL: {body: familyFixture(t, "theplatform", "media.smil")},
			metaURL: {body: familyFixture(t, "theplatform", "preview.json")},
		}}
		media, err := NewThePlatform().Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: tpTransport})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})

	return out
}
