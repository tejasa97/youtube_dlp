package extractor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/javascript/engine"
)

// breadthVimeoTransport serves deterministic Vimeo player fixtures for any
// numeric video id by rewriting the shared offline page/config templates.
type breadthVimeoTransport struct {
	page   []byte
	config []byte
}

func (t *breadthVimeoTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected native page request")
}

func (t *breadthVimeoTransport) ReadPageProfile(_ context.Context, rawURL, _ string) ([]byte, http.Header, error) {
	id := strings.TrimPrefix(rawURL, "https://vimeo.com/")
	if id == rawURL {
		id = strings.TrimPrefix(rawURL, "https://player.vimeo.com/video/")
	}
	if !laracastsVimeoID.MatchString(id) {
		return nil, nil, fmt.Errorf("unexpected webpage URL %q", rawURL)
	}
	page := bytes.ReplaceAll(t.page, []byte("123456789"), []byte(id))
	return page, make(http.Header), nil
}

func (t *breadthVimeoTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "player.vimeo.com" {
		return nil, fmt.Errorf("unexpected config request %s %s", request.Method, request.URL)
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "video" || parts[2] != "config" || !laracastsVimeoID.MatchString(parts[1]) {
		return nil, fmt.Errorf("unexpected config path %s", request.URL.Path)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(t.config)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (t *breadthVimeoTransport) DoProfile(ctx context.Context, request *http.Request, _ string) (*http.Response, error) {
	return t.Do(ctx, request)
}

func (t *breadthVimeoTransport) DoWithoutCredentialsNoRedirectWithReferer(ctx context.Context, request *http.Request) (*http.Response, error) {
	return t.Do(ctx, request)
}

func breadthVimeoReentryTransport(t *testing.T) *breadthVimeoTransport {
	t.Helper()
	return &breadthVimeoTransport{
		page:   readVimeoFixture(t, "page.html"),
		config: readVimeoFixture(t, "config.json"),
	}
}

func TestLaracastsPropsHTMLUnescape(t *testing.T) {
	t.Parallel()
	const series = "30-days-to-learn-laravel-11/episodes/1"
	canonical := "https://laracasts.com/series/" + series

	t.Run("entities and literal plus", func(t *testing.T) {
		// &quot; / &amp; decode; literal '+' must survive (QueryUnescape would break it).
		page := []byte(`<html><div id="app" data-page="{&quot;props&quot;:{&quot;lesson&quot;:{&quot;vimeoId&quot;:&quot;123456789&quot;,&quot;title&quot;:&quot;A+B &amp; C&quot;}}}"></div></html>`)
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: page}}
		result, err := NewLaracasts().Extract(context.Background(), Request{URL: canonical, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsURL() || result.Redirect.URL != "https://player.vimeo.com/video/123456789" {
			t.Fatalf("%#v", result)
		}
		props, err := laracastsProps(context.Background(), Request{URL: canonical, Transport: transport}, series)
		if err != nil {
			t.Fatal(err)
		}
		lesson := props["lesson"].(map[string]any)
		if title, _ := lesson["title"].(string); title != "A+B & C" {
			t.Fatalf("title=%q want preserved plus and decoded amp", title)
		}
	})

	t.Run("malformed props", func(t *testing.T) {
		_, err := laracastsDecodeDataPage([]byte(`{"props": not-json}`))
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("malformed=%v", err)
		}
	})

	t.Run("oversize decoded props", func(t *testing.T) {
		raw := []byte(`{"props":{"lesson":{"vimeoId":"1","pad":"` + strings.Repeat("x", int(maxExtractorJSONBytes)+8) + `"}}}`)
		_, err := laracastsDecodeDataPage(raw)
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("oversize=%v", err)
		}
	})

	t.Run("oversize page rejected", func(t *testing.T) {
		page := bytes.Repeat([]byte("a"), int(maxExtractorJSONBytes)+16)
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: page}}
		_, err := NewLaracasts().Extract(context.Background(), Request{URL: canonical, Transport: transport})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("oversize page=%v", err)
		}
	})
}

func TestMediaStreamFormatOrderDeterministic(t *testing.T) {
	t.Parallel()
	u := "https://mdstrm.com/embed/6318e3f1d1d316083ae48831"
	page := []byte(`<html><script>window.MDSTRM.OPTIONS = {"title":"Clip","type":"vod","src":{"mp4":"https://mdstrm.example/clip.mp4","hls":"https://mdstrm.example/master.m3u8","dash":"https://mdstrm.example/manifest.mpd"}};</script></html>`)
	var first []string
	for i := 0; i < 25; i++ {
		transport := &sharedFixtureTransport{pages: map[string][]byte{u: page}}
		result, err := NewMediaStream().Extract(context.Background(), Request{URL: u, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		formats, ok := result.Info.Formats()
		if !ok || len(formats) != 3 {
			t.Fatalf("formats=%v ok=%t", formats, ok)
		}
		ids := make([]string, 0, len(formats))
		for _, format := range formats {
			obj, ok := format.Object()
			if !ok {
				t.Fatal("format object")
			}
			id, _ := obj.Lookup("format_id").StringValue()
			ids = append(ids, id)
		}
		if first == nil {
			first = ids
			continue
		}
		if strings.Join(ids, ",") != strings.Join(first, ",") {
			t.Fatalf("nondeterministic format order: %v vs %v", first, ids)
		}
	}
	if strings.Join(first, ",") != "dash,hls,mp4" {
		t.Fatalf("want sorted keys dash,hls,mp4 got %v", first)
	}
}

func TestBuzzFeedDoesNotEmitFacebookExtractorKey(t *testing.T) {
	t.Parallel()
	u := "https://www.buzzfeed.com/abagg/mixed-embeds"
	page := []byte(`<html>
<div class="video-embed" rel:bf_bucket_data='{"video":{"id":"fixture0001","url":"https://www.youtube.com/watch?v=fixture0001"}}'></div>
<div class="video-embed" rel:bf_bucket_data='{"video":{"id":"fb1","url":"https://www.facebook.com/watch/?v=971793786185728"}}'></div>
</html>`)
	transport := &sharedFixtureTransport{pages: map[string][]byte{u: page}}
	result, err := NewBuzzFeed().Extract(context.Background(), Request{URL: u, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, breadthAdapterMaxEntries)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if entries[0].ExtractorKey != "youtube" {
		t.Fatalf("youtube key=%q", entries[0].ExtractorKey)
	}
	if entries[1].ExtractorKey != "" {
		t.Fatalf("facebook entry must not set explicit key, got %q", entries[1].ExtractorKey)
	}
	for _, entry := range entries {
		if entry.ExtractorKey == "facebook" {
			t.Fatal("must never emit nonexistent facebook ExtractorKey")
		}
	}
	// YouTube child re-entry with deterministic fixtures.
	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	watch := readYouTubeFixture(t, "watch.html")
	player := readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js")
	ytTransport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: watch,
		youtubePlayerURL:  player,
	}}
	media, err := NewYouTube().Extract(context.Background(), Request{
		URL: entries[0].URL, Transport: ytTransport, ChallengeSolver: solver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
		t.Fatal("youtube re-entry missing formats")
	}
}

func TestBreadthAdaptersHostileMalformedSecretSafe(t *testing.T) {
	t.Parallel()
	secretBody := []byte("token=must-not-leak Authorization=Bearer secret")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	type caseSpec struct {
		name       string
		extractor  Extractor
		okURL      string
		pageURL    string // ReadPage key when needed
		malformed  []byte
		secretURL  string
		secretResp map[string]fixtureHTTP
		secretPage map[string][]byte
	}

	cases := []caseSpec{
		{
			name: "teachingchannel", extractor: NewTeachingChannel(),
			okURL:     "https://www.teachingchannel.org/videos/teacher-teaming-evolution",
			pageURL:   "https://www.teachingchannel.org/videos/teacher-teaming-evolution",
			malformed: []byte(`<html>no mid</html>`),
			secretPage: map[string][]byte{
				"https://www.teachingchannel.org/videos/teacher-teaming-evolution": append([]byte(`<html>sign in `), secretBody...),
			},
		},
		{
			name: "nowcanal", extractor: NewNowCanal(),
			okURL:     "https://www.nowcanal.pt/ultimas/detalhe/pedro-sousa-hjulmand",
			pageURL:   "https://www.nowcanal.pt/ultimas/detalhe/pedro-sousa-hjulmand",
			malformed: []byte(`<html></html>`),
			secretPage: map[string][]byte{
				"https://www.nowcanal.pt/ultimas/detalhe/pedro-sousa-hjulmand": append([]byte(`<html>login `), secretBody...),
			},
		},
		{
			name: "democracynow", extractor: NewDemocracyNow(),
			okURL:     "https://www.democracynow.org/shows/2015/7/3",
			pageURL:   "https://www.democracynow.org/shows/2015/7/3",
			malformed: []byte(`<html></html>`),
			secretPage: map[string][]byte{
				"https://www.democracynow.org/shows/2015/7/3": append([]byte(`<html>denied `), secretBody...),
			},
		},
		{
			name: "buzzfeed", extractor: NewBuzzFeed(),
			okURL:     "https://www.buzzfeed.com/abagg/x",
			pageURL:   "https://www.buzzfeed.com/abagg/x",
			malformed: []byte(`<html>no buckets</html>`),
		},
		{
			name: "mediastream", extractor: NewMediaStream(),
			okURL:     "https://mdstrm.com/embed/6318e3f1d1d316083ae48831",
			pageURL:   "https://mdstrm.com/embed/6318e3f1d1d316083ae48831",
			malformed: []byte(`<html></html>`),
			secretPage: map[string][]byte{
				"https://mdstrm.com/embed/6318e3f1d1d316083ae48831": append([]byte(`<html>geo `), secretBody...),
			},
		},
		{
			name: "winsports", extractor: NewWinSports(),
			okURL:     "https://www.winsports.co/videos/x",
			pageURL:   "https://www.winsports.co/videos/x",
			malformed: []byte(`<html></html>`),
		},
		{
			name: "vidsio", extractor: NewVidsIo(),
			okURL:     "https://how-to-video.vids.io/videos/799cd8b11c10efc1f0/how-to-video-live-streaming",
			pageURL:   "https://how-to-video.vids.io/videos/799cd8b11c10efc1f0/how-to-video-live-streaming",
			malformed: []byte(`<html></html>`),
		},
		{
			name: "laracasts", extractor: NewLaracasts(),
			okURL:     "https://laracasts.com/series/30-days-to-learn-laravel-11/episodes/1",
			pageURL:   "https://laracasts.com/series/30-days-to-learn-laravel-11/episodes/1",
			malformed: []byte(`<html></html>`),
		},
		{
			name: "laracasts_series", extractor: NewLaracastsSeries(),
			okURL:     "https://laracasts.com/series/30-days-to-learn-laravel-11",
			pageURL:   "https://laracasts.com/series/30-days-to-learn-laravel-11",
			malformed: []byte(`<html><div id="app" data-page='{"props":{"series":{"chapters":[]}}}'></div></html>`),
		},
		{
			name: "abcotvs", extractor: NewABCOTVS(),
			okURL:     "https://abc7news.com/entertainment/east-bay-museum/472581/",
			secretURL: "https://abc7news.com/entertainment/east-bay-museum/472581/",
			secretResp: map[string]fixtureHTTP{
				"https://api.abcotvs.com/v2/content?id=472581&key=otv.web.kgo.story&station=kgo": {
					status: http.StatusUnauthorized, body: secretBody,
				},
			},
		},
		{
			name: "abcotvs_clips", extractor: NewABCOTVSClips(),
			okURL:     "https://clips.abcotvs.com/kabc/video/214814",
			secretURL: "https://clips.abcotvs.com/kabc/video/214814",
			secretResp: map[string]fixtureHTTP{
				"https://clips.abcotvs.com/vogo/video/getByIds?ids=214814": {
					status: http.StatusUnauthorized, body: secretBody,
				},
			},
		},
	}

	for _, spec := range cases {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			okParsed, err := url.Parse(spec.okURL)
			if err != nil || !spec.extractor.Suitable(okParsed) {
				t.Fatalf("Suitable(%q)=false err=%v", spec.okURL, err)
			}
			hostile, _ := url.Parse("https://evil.example/steal")
			if spec.extractor.Suitable(hostile) {
				t.Fatal("must reject hostile host")
			}
			if _, err := spec.extractor.Extract(canceled, Request{URL: spec.okURL, Transport: &sharedFixtureTransport{}}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel=%v", err)
			}
			if spec.malformed != nil {
				tr := &sharedFixtureTransport{pages: map[string][]byte{spec.pageURL: spec.malformed}}
				result, err := spec.extractor.Extract(context.Background(), Request{URL: spec.okURL, Transport: tr})
				if err == nil && result.IsPlaylist() {
					_, err = CollectEntries(context.Background(), result.Entries, breadthAdapterMaxEntries)
				}
				if err == nil || (!errors.Is(err, ErrInvalidMetadata) && !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrAuthentication)) {
					t.Fatalf("malformed=%v", err)
				}
			}
			if spec.secretResp != nil || spec.secretPage != nil {
				tr := &sharedFixtureTransport{responses: spec.secretResp, pages: spec.secretPage}
				raw := spec.secretURL
				if raw == "" {
					raw = spec.okURL
				}
				_, err := spec.extractor.Extract(context.Background(), Request{URL: raw, Transport: tr})
				if err == nil {
					t.Fatal("expected secret-safe error")
				}
				if strings.Contains(err.Error(), "must-not-leak") {
					t.Fatalf("secret leak: %v", err)
				}
			}
		})
	}
}

func FuzzBreadthPriority100AdapterParsers(f *testing.F) {
	f.Add("https://www.teachingchannel.org/videos/teacher-teaming-evolution")
	f.Add("https://www.nowcanal.pt/ultimas/detalhe/x")
	f.Add("https://www.democracynow.org/shows/2015/7/3")
	f.Add("https://www.buzzfeed.com/a/b")
	f.Add("https://mdstrm.com/embed/abc")
	f.Add("https://mdstrm.com/live-stream/abc")
	f.Add("https://www.winsports.co/videos/x")
	f.Add("https://abc7news.com/x/472581/")
	f.Add("https://clips.abcotvs.com/kabc/video/214814")
	f.Add("https://how-to-video.vids.io/videos/799cd8b11c10efc1f0/slug")
	f.Add("https://laracasts.com/series/s/episodes/1")
	f.Add("https://laracasts.com/series/s")
	f.Add("https://evil.example/x")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseTeachingChannelURL(parsed)
		_, _ = parseNowCanalURL(parsed)
		_, _ = parseDemocracyNowURL(parsed)
		_, _ = parseBuzzFeedURL(parsed)
		_, _, _ = parseMediaStreamURL(parsed)
		_, _ = parseWinSportsURL(parsed)
		_, _, _ = parseABCOTVSURL(parsed)
		_, _ = parseABCOTVSClipsURL(parsed)
		_, _ = parseVidsIoURL(parsed)
		_, _ = parseLaracastsEpisodeURL(parsed)
		_, _ = parseLaracastsSeriesURL(parsed)
	})
}

func TestBreadthAdaptersOversizeInputs(t *testing.T) {
	t.Parallel()
	oversizedPage := bytes.Repeat([]byte("a"), int(maxExtractorJSONBytes)+32)
	oversizedJSON := bytes.Repeat([]byte("b"), int(maxExtractorJSONBytes)+32)

	type caseSpec struct {
		name      string
		run       func(*testing.T) error
		want      error
		exercised string // page | api
	}

	cases := []caseSpec{
		{
			name: "teachingchannel page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://www.teachingchannel.org/videos/teacher-teaming-evolution"
				_, err := NewTeachingChannel().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: oversizedPage}},
				})
				return err
			},
		},
		{
			name: "nowcanal page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://www.nowcanal.pt/ultimas/detalhe/pedro-sousa-hjulmand"
				_, err := NewNowCanal().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: oversizedPage}},
				})
				return err
			},
		},
		{
			name: "democracynow page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://www.democracynow.org/shows/2015/7/3"
				_, err := NewDemocracyNow().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{
						"https://www.democracynow.org/shows/2015/7/3": oversizedPage,
					}},
				})
				return err
			},
		},
		{
			name: "buzzfeed page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://www.buzzfeed.com/abagg/oversize"
				result, err := NewBuzzFeed().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: oversizedPage}},
				})
				if err != nil {
					return err
				}
				_, err = CollectEntries(context.Background(), result.Entries, breadthAdapterMaxEntries)
				return err
			},
		},
		{
			name: "mediastream page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://mdstrm.com/embed/6318e3f1d1d316083ae48831"
				_, err := NewMediaStream().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: oversizedPage}},
				})
				return err
			},
		},
		{
			name: "winsports page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://www.winsports.co/videos/oversize-clip"
				_, err := NewWinSports().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{
						"https://www.winsports.co/videos/oversize-clip": oversizedPage,
					}},
				})
				return err
			},
		},
		{
			name: "vidsio page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://how-to-video.vids.io/videos/799cd8b11c10efc1f0/how-to-video-live-streaming"
				_, err := NewVidsIo().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: oversizedPage}},
				})
				return err
			},
		},
		{
			name: "laracasts page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://laracasts.com/series/30-days-to-learn-laravel-11/episodes/1"
				_, err := NewLaracasts().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: oversizedPage}},
				})
				return err
			},
		},
		{
			name: "laracasts_series page", want: ErrInvalidMetadata, exercised: "page",
			run: func(t *testing.T) error {
				u := "https://laracasts.com/series/30-days-to-learn-laravel-11"
				result, err := NewLaracastsSeries().Extract(context.Background(), Request{
					URL: u, Transport: &sharedFixtureTransport{pages: map[string][]byte{u: oversizedPage}},
				})
				if err != nil {
					return err
				}
				_, err = CollectEntries(context.Background(), result.Entries, breadthAdapterMaxEntries)
				return err
			},
		},
		{
			name: "abcotvs api", want: ErrJSONResponseTooLarge, exercised: "api",
			run: func(t *testing.T) error {
				_, err := NewABCOTVS().Extract(context.Background(), Request{
					URL: "https://abc7news.com/entertainment/east-bay-museum/472581/",
					Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
						"https://api.abcotvs.com/v2/content?id=472581&key=otv.web.kgo.story&station=kgo": {body: oversizedJSON},
					}},
				})
				return err
			},
		},
		{
			name: "abcotvs_clips api", want: ErrJSONResponseTooLarge, exercised: "api",
			run: func(t *testing.T) error {
				_, err := NewABCOTVSClips().Extract(context.Background(), Request{
					URL: "https://clips.abcotvs.com/kabc/video/214814",
					Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
						"https://clips.abcotvs.com/vogo/video/getByIds?ids=214814": {body: oversizedJSON},
					}},
				})
				return err
			},
		},
	}

	for _, spec := range cases {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			t.Parallel()
			err := spec.run(t)
			if !errors.Is(err, spec.want) {
				t.Fatalf("exercised=%s err=%v want %v", spec.exercised, err, spec.want)
			}
		})
	}
}

func TestBuzzFeedProvenanceFixtureDistinctChildren(t *testing.T) {
	t.Parallel()
	u := "https://www.buzzfeed.com/abagg/this-angry-ram-destroys-a-punching-bag-like-a-boss"
	transport := &sharedFixtureTransport{pages: map[string][]byte{u: familyFixture(t, "buzzfeed", "page.html")}}
	result, err := NewBuzzFeed().Extract(context.Background(), Request{URL: u, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, breadthAdapterMaxEntries)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%v err=%v want 2 distinct children", entries, err)
	}
	if entries[0].ExtractorKey != "youtube" || entries[0].URL != "https://www.youtube.com/watch?v=fixture0001" {
		t.Fatalf("youtube entry=%#v", entries[0])
	}
	if entries[1].ExtractorKey != "" || !strings.Contains(entries[1].URL, "facebook.com") {
		t.Fatalf("facebook bare entry=%#v", entries[1])
	}
}

func TestABCOTVSMalformedEmptyFormats(t *testing.T) {
	t.Parallel()
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://api.abcotvs.com/v2/content?id=472581&key=otv.web.kgo.story&station=kgo": {
			body: []byte(`{"data":{"featuredMedia":{"video":{"id":472548,"title":"X"}}}}`),
		},
	}}
	_, err := NewABCOTVS().Extract(context.Background(), Request{
		URL: "https://abc7news.com/entertainment/east-bay-museum/472581/", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("empty formats=%v", err)
	}
}
