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
	json  map[string]string
	page  map[string]string
	seen  http.Header
	calls int
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
	return t.reply(ctx, request)
}
func (t *raiTestTransport) reply(_ context.Context, request *http.Request) (*http.Response, error) {
	t.calls++
	t.seen = request.Header.Clone()
	var body string
	if strings.Contains(request.URL.Host, "relinker.rai.it") {
		body = `<root><url type="content">https://cdn.example.test/path/chunklist_ao.m3u8</url><duration>01:01</duration><is_live>N</is_live></root>`
	} else {
		body = t.json[request.URL.String()]
	}
	if body == "" {
		return nil, errors.New("missing response fixture")
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}, nil
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
		_, err := raiXML([]byte(fixture.body))
		if fixture.want == ErrInvalidMetadata && !errors.Is(err, fixture.want) {
			t.Fatalf("XML error = %v", err)
		}
	}
	if _, err := raiFormats("http://127.0.0.1/x.mp4", nil, false); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("private media error = %v", err)
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
