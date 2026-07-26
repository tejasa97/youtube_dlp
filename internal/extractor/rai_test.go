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

type raiTestTransport struct {
	json           map[string]string
	page           map[string]string
	relinker       string
	relinkerStatus int
	seen           http.Header
	seenURL        *url.URL
	isolated       bool
	calls          int
}

func (t *raiTestTransport) ReadPage(_ context.Context, raw string) ([]byte, http.Header, error) {
	if body, ok := t.page[raw]; ok {
		return []byte(body), make(http.Header), nil
	}
	return nil, nil, errors.New("missing page fixture")
}
func (t *raiTestTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return t.reply(ctx, request)
}
func (t *raiTestTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	t.isolated = true
	return t.reply(ctx, request)
}
func (t *raiTestTransport) reply(_ context.Context, request *http.Request) (*http.Response, error) {
	t.calls++
	t.seen = request.Header.Clone()
	t.seenURL = request.URL
	var body string
	if strings.Contains(request.URL.Host, "relinker.rai.it") {
		body = t.relinker
		if body == "" {
			body = `<root><url type="content">https://cdn.example.test/path/chunklist_ao.m3u8</url><duration>01:01</duration><is_live>N</is_live></root>`
		}
	} else {
		body = t.json[request.URL.String()]
	}
	if body == "" {
		return nil, errors.New("missing response fixture")
	}
	status := t.relinkerStatus
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
}

func TestRaiPlaylistIsLazyAndUsesExplicitReentry(t *testing.T) {
	base := "https://www.raiplay.it/programmi/report"
	transport := &raiTestTransport{json: map[string]string{
		base + ".json":          `{"name":"Report","blocks":[{"sets":[{"id":"episodes"}]}]}`,
		base + "/episodes.json": `{"items":[{"path_id":"/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391.html"}]}`,
	}, page: map[string]string{}}
	result, err := NewRaiPlayPlaylist().Extract(context.Background(), Request{URL: base, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 {
		t.Fatalf("eager set fetches = %d", transport.calls)
	}
	iterator := result.Entries.Iterator()
	entry, ok, err := iterator.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("entry = %#v, %t, %v", entry, ok, err)
	}
	if entry.ExtractorKey != "raiplay" || entry.URL != "https://www.raiplay.it/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391.html" {
		t.Fatalf("entry = %#v", entry)
	}
	if transport.calls != 2 {
		t.Fatalf("lazy set fetches = %d", transport.calls)
	}
	second := result.Entries.Iterator()
	if _, ok, err := second.Next(context.Background()); err != nil || !ok {
		t.Fatalf("reusable iterator = %t, %v", ok, err)
	}
}

func TestRaiFilteredPlaylists(t *testing.T) {
	playBase := "https://www.raiplay.it/programmi/report"
	playTransport := &raiTestTransport{json: map[string]string{
		playBase + ".json":         `{"name":"Report","blocks":[{"name":"episodi","sets":[{"name":"Stagione 2","id":"season2"}]}]}`,
		playBase + "/season2.json": `{"items":[{"path_id":"/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391.html"}]}`,
	}, page: map[string]string{}}
	play, err := NewRaiPlayPlaylist().Extract(context.Background(), Request{URL: playBase + "/episodi/stagione-2", Transport: playTransport})
	if err != nil {
		t.Fatal(err)
	}
	if title, _ := play.Info.Title(); title != "Report - Stagione 2" {
		t.Fatalf("RaiPlay selected title = %q", title)
	}
	if _, ok, err := play.Entries.Iterator().Next(context.Background()); err != nil || !ok {
		t.Fatalf("RaiPlay selected entries = %t, %v", ok, err)
	}

	soundBase := "https://www.raiplaysound.it/programmi/report"
	soundTransport := &raiTestTransport{json: map[string]string{
		soundBase + ".json": `{"title":"Report","filters":[{"weblink":"/programmi/report/puntate/prima","path_id":"/programmi/report/puntate/prima.json"}]}`,
		"https://www.raiplaysound.it/programmi/report/puntate/prima.json": `{"title":"Prima","cards":[{"path_id":"/audio/x-cb27157f-9dd0-4aee-b788-b1f67643a391.html"}]}`,
	}, page: map[string]string{}}
	sound, err := NewRaiPlaySoundPlaylist().Extract(context.Background(), Request{URL: soundBase + "/puntate/prima", Transport: soundTransport})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := sound.Info.ID(); id != "report_puntate_prima" {
		t.Fatalf("RaiSound selected id = %q", id)
	}
	if title, _ := sound.Info.Title(); title != "Prima" {
		t.Fatalf("RaiSound selected title = %q", title)
	}
	if entry, ok, err := sound.Entries.Iterator().Next(context.Background()); err != nil || !ok || entry.ExtractorKey != "raiplaysound" {
		t.Fatalf("RaiSound selected entry = %#v, %t, %v", entry, ok, err)
	}
}

func TestRaiPlaylistEntryLimit(t *testing.T) {
	base := "https://www.raiplay.it/programmi/report"
	var items strings.Builder
	items.WriteString(`{"items":[`)
	for index := 0; index <= raiMaxEntries; index++ {
		if index != 0 {
			items.WriteByte(',')
		}
		items.WriteString(`{"path_id":"/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391.html"}`)
	}
	items.WriteString(`]}`)
	transport := &raiTestTransport{json: map[string]string{
		base + ".json":          `{"name":"Report","blocks":[{"sets":[{"id":"episodes"}]}]}`,
		base + "/episodes.json": items.String(),
	}, page: map[string]string{}}
	result, err := NewRaiPlayPlaylist().Extract(context.Background(), Request{URL: base, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := result.Entries.Iterator().Next(context.Background()); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("playlist overflow = %v", err)
	}
}

func TestRaiRoutingMatrixAndHostHardening(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	cases := []struct{ raw, want string }{
		{"https://www.raiplay.it/video/x-" + id + ".html", "raiplay"},
		{"https://www.raiplay.it/dirette/rainews24", "raiplay_live"},
		{"https://www.raiplay.it/programmi/report", "raiplay_playlist"},
		{"https://www.raiplaysound.it/audio/x-" + id + ".html", "raiplaysound"},
		{"https://www.raiplaysound.it/radio2", "raiplaysound_live"},
		{"https://www.raiplaysound.it/programmi/report", "raiplaysound_playlist"},
		{"https://www.rai.it/dl/a/x-" + id + ".html", "rai"},
		{"https://www.rainews.it/video/x-" + id + ".html", "rainews"},
		{"https://www.raicultura.it/video/x-" + id + ".html", "raicultura"},
		{"https://raisudtirol.rai.it/la/index.php?media=Ptv1619729460", "raisudtirol"},
	}
	registry := NewRegistry(NewRaiPlayPlaylist(), NewRaiPlayLive(), NewRaiPlay(), NewRaiPlaySoundPlaylist(), NewRaiPlaySoundLive(), NewRaiPlaySound(), NewRaiNews(), NewRaiCultura(), NewRaiSudtirol(), NewRai())
	for _, tc := range cases {
		got, err := registry.Select(tc.raw)
		if err != nil || got.Name() != tc.want {
			t.Fatalf("Select(%q) = %v, %v", tc.raw, got, err)
		}
	}
	for _, raw := range []string{
		"https://www.raiplay.it.evil.test/video/x-" + id + ".html", "https://user@www.raiplay.it/video/x-" + id + ".html", "https://www.raiplay.it:443/video/x-" + id + ".html", "https://www.raiplay.it/video/x-" + id + ".html#x", "https://www.raiplay.it/video/%2f" + id + ".html",
	} {
		if _, err := registry.Select(raw); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsafe URL accepted: %q (%v)", raw, err)
		}
	}
}

func TestRaiPlayRelinkerMetadataAndAudioCodec(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	page := "https://www.raiplay.it/video/x-" + id
	transport := &raiTestTransport{json: map[string]string{page + ".json": `{"id":"ContentItem-` + id + `","name":"Fixture Rai","description":"safe","video":{"content_url":"https://relinker.rai.it/rel","duration":"61","subtitles":"/caption.srt"},"images":{"small":"/image.jpg"}}`}, page: map[string]string{}}
	result, err := NewRaiPlay().Extract(context.Background(), Request{URL: page + ".html", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := result.Info.Title(); got != "Fixture Rai" {
		t.Fatalf("title = %q", got)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats = %#v", formats)
	}
	format, _ := formats[0].Object()
	if codec, _ := format.Lookup("vcodec").StringValue(); codec != "none" {
		t.Fatalf("audio-only vcodec = %q", codec)
	}
	if got := transport.seen.Get("User-Agent"); got != "Rai" {
		t.Fatalf("Rai User-Agent = %q", got)
	}
	if !transport.isolated {
		t.Fatal("relinker did not use the credential-isolated transport")
	}
	if _, ok := result.Info.Lookup("subtitles").Object(); !ok {
		t.Fatal("missing subtitles")
	}
}

func TestRaiRelinkerGeoDRMAndMalformed(t *testing.T) {
	for _, fixture := range []struct {
		body string
		want error
	}{
		{`<root><url type="content">https://cdn.example.test/video_no_available.mp4</url></root>`, ErrRegionRestricted},
		{`<root><license_url>https://license.example.test/x</license_url><url type="content">https://cdn.example.test/a.mp4</url></root>`, ErrUnavailable},
		{`<root><url>`, ErrInvalidMetadata},
	} {
		transport := &raiTestTransport{json: map[string]string{}, page: map[string]string{}, relinker: fixture.body}
		_, err := raiRelinker(context.Background(), transport, "https://relinker.rai.it/rel?existing=safe", "fixture", false)
		if !errors.Is(err, fixture.want) {
			t.Fatalf("XML error = %v", err)
		}
		if transport.seenURL.Query().Get("output") != "64" || transport.seenURL.Query().Get("existing") != "safe" {
			t.Fatalf("relinker query = %q", transport.seenURL.Redacted())
		}
	}
	if _, err := raiFormats("http://127.0.0.1/x.mp4", nil, false); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("private media error = %v", err)
	}
	status := &raiTestTransport{json: map[string]string{}, page: map[string]string{}, relinkerStatus: http.StatusNotFound}
	if _, err := raiRelinker(context.Background(), status, "https://relinker.rai.it/rel", "fixture", false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("relinker status mapping = %v", err)
	}
}

func TestRaiNewsAndCulturaEscapedPlayerData(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	for _, tc := range []struct {
		raw, tag  string
		extractor Extractor
	}{
		{"https://www.rainews.it/video/x-" + id + ".html", "news", NewRaiNews()},
		{"https://www.raicultura.it/video/x-" + id + ".html", "cultura", NewRaiCultura()},
	} {
		page := `<rai` + tc.tag + `-player data='{&quot;title&quot;:&quot;Fixture&quot;,&quot;mediapolis&quot;:&quot;https://relinker.rai.it/rel?x&#x3D;1&quot;}'></rai` + tc.tag + `-player>`
		transport := &raiTestTransport{json: map[string]string{}, page: map[string]string{tc.raw: page}}
		result, err := tc.extractor.Extract(context.Background(), Request{URL: tc.raw, Transport: transport})
		if err != nil {
			t.Fatalf("%s: %v", tc.tag, err)
		}
		if title, _ := result.Info.Title(); title != "Fixture" {
			t.Fatalf("%s title = %q", tc.tag, title)
		}
	}
}

func TestRaiSubtitlesPreserveLanguageVariants(t *testing.T) {
	subs := raiSubtitles("https://www.raiplay.it/video/x.html", map[string]any{
		"subtitlesArray": []any{
			map[string]any{"url": "/one.stl", "language": "it"},
			map[string]any{"url": "/two.srt", "language": "it"},
			map[string]any{"url": "/three.vtt", "language": "it"},
		},
	})
	entries, ok := subs.Lookup("it").ListValue()
	if !ok || len(entries) != 4 {
		t.Fatalf("Italian subtitles = %#v", entries)
	}
}

func FuzzRaiURLClassification(f *testing.F) {
	f.Add("https://www.raiplay.it/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391.html")
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		target := raiClassify(u)
		if target.kind != raiNone && !raiSafePageURL(u) {
			t.Fatal("accepted unsafe Rai page")
		}
		if target.id != "" && len(target.id) > 256 {
			t.Fatal("unbounded Rai identity")
		}
	})
}

func FuzzRaiRelinkerXML(f *testing.F) {
	f.Add([]byte(`<root><url type="content">https://cdn.example.test/a.mp4</url></root>`))
	f.Fuzz(func(t *testing.T, body []byte) {
		fields, err := raiXML(body)
		if err != nil {
			return
		}
		if media := fields["content_url"]; media != "" {
			_, formatErr := raiFormats(media, fields, false)
			if !raiPublicURL(media) && !errors.Is(formatErr, ErrInvalidMetadata) {
				t.Fatalf("unsafe XML media result = %v", formatErr)
			}
		}
	})
}
