package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
)

type jwWave1ProductTransport struct {
	pages     map[string][]byte
	responses map[string]fixtureHTTP
}

type fixtureHTTP struct {
	status int
	body   []byte
}

func (transport *jwWave1ProductTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if page, ok := transport.pages[rawURL]; ok {
		return append([]byte(nil), page...), make(http.Header), nil
	}
	return nil, nil, errors.New("unexpected fixture page")
}

func (transport *jwWave1ProductTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response, ok := transport.responses[request.URL.String()]
	if !ok {
		return nil, errors.New("unexpected fixture request")
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(response.body)),
		Request:    request,
	}, nil
}

func jwWave1ProductFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "extractor", "testdata", "jwplatform_adapters_wave1", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProductRegistryJWPlatformAdaptersWave1Routing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL string
		name   string
	}{
		{"https://www.bundesliga.com/en/bundesliga/videos?vid=AbCd1234", "bundesliga"},
		{"https://uk.businessinsider.com/article-slug", "businessinsider"},
		{"https://www.dagbladet.no/video/slug/PalfB2Cw", "dbtv"},
		{"https://www.hollywoodreporter.com/video/slug/", "hollywoodreporter"},
		{"https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2", "iltalehti"},
		{"https://video.lefigaro.fr/embed/figaro/video/slug/", "lefigarovideoembed"},
		{"https://www.mirror.co.uk/tv/tv-news/article-27163139", "mirrorcouk"},
		{"http://www.outsidetv.com/home/play/ZjQYboH6/1/10/Hdg0jukV/4", "outsidetv"},
		{"https://theintercept.com/fieldofvision/slug/", "theintercept"},
	}
	registry := productRegistry()
	for _, test := range tests {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.name {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.name)
		}
	}
}

func TestProductMirrorCoUKReentersJWPlatformMediaID(t *testing.T) {
	t.Parallel()
	const (
		mirrorURL  = "https://www.mirror.co.uk/tv/tv-news/love-island-fans-baffled-after-27163139"
		jwEndpoint = "https://cdn.jwplayer.com/v2/media/voyyS7SV"
	)
	transport := &jwWave1ProductTransport{
		pages: map[string][]byte{
			mirrorURL: jwWave1ProductFixture(t, "mirror_page.html"),
		},
		responses: map[string]fixtureHTTP{
			jwEndpoint: {body: []byte(`{"title":"Mirror Fixture","playlist":[{"mediaid":"voyyS7SV","title":"Mirror Fixture","sources":[{"file":"https://media.example/jw/master.m3u8","type":"application/x-mpegURL"},{"file":"https://media.example/jw/video.mp4","height":720,"bitrate":1500}]}]}`)},
		},
	}
	registry := NewClient().productRegistry()
	first, firstName, err := registry.Extract(context.Background(), extractor.Request{URL: mirrorURL, Transport: transport})
	if err != nil || firstName != "mirrorcouk" || !first.IsURL() || first.Redirect == nil {
		t.Fatalf("first=%#v name=%q err=%v", first, firstName, err)
	}
	if first.Redirect.URL != "jwplatform:voyyS7SV" || first.Redirect.ID != "voyyS7SV" {
		t.Fatalf("redirect=%#v", first.Redirect)
	}
	second, err := registry.SelectFor(first.Redirect.URL, first.Redirect.ExtractorKey)
	if err != nil || second.Name() != "jwplatform" {
		t.Fatalf("select=%v err=%v", second, err)
	}
	media, err := second.Extract(context.Background(), extractor.Request{URL: first.Redirect.URL, Transport: transport})
	if err != nil || media.IsURL() || media.IsPlaylist() {
		t.Fatalf("media=%#v err=%v", media, err)
	}
	id, ok := media.Info.Lookup("id").StringValue()
	if !ok || id != "voyyS7SV" {
		t.Fatalf("media id=%q ok=%t", id, ok)
	}
}
