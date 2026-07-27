package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type nhkFixtureTransport struct {
	mu       sync.Mutex
	bodies   map[string]string
	statuses map[string]int
	calls    []string
}

func (t *nhkFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	key := request.URL.String()
	t.calls = append(t.calls, key)
	body, ok := t.bodies[key]
	if !ok {
		// Allow prefix matching for query-bearing series URLs.
		for configured, value := range t.bodies {
			if strings.HasPrefix(key, configured) {
				body = value
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil, errors.New("missing NHK fixture: " + key)
	}
	status := http.StatusOK
	if configured, found := t.statuses[key]; found {
		status = configured
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (t *nhkFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := t.Do(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	return data, resp.Header.Clone(), nil
}

func nhkReadFixture(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "nhk", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNHKWorldVODSuitable(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://www3.nhk.or.jp/nhkworld/en/shows/2049165/", true},
		{"https://www3.nhk.or.jp/nhkworld/en/ondemand/video/9999011/", true},
		{"https://www3.nhk.or.jp/nhkworld/en/shows/audio/livinginjapan-20240901-1/", true},
		{"https://www3.nhk.or.jp/nhkworld/fr/ondemand/audio/plugin-20190404-1/", true},
		{"https://www3.nhk.or.jp/nhkworld/en/shows/sumo/", false},
		{"https://www3.nhk.or.jp.evil/nhkworld/en/shows/2049165/", false},
		{"https://user@www3.nhk.or.jp/nhkworld/en/shows/2049165/", false},
		{"https://www3.nhk.or.jp:8443/nhkworld/en/shows/2049165/", false},
		{"https://www3.nhk.or.jp/nhkworld/en/shows/2049165/%2e%2e/", false},
	}
	ie := NewNhkVodIE()
	for _, tc := range cases {
		parsed, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.raw, err)
		}
		if got := ie.Suitable(parsed); got != tc.want {
			t.Fatalf("Suitable(%s)=%v want %v", tc.raw, got, tc.want)
		}
	}
}

func TestNHKWorldProgramDoesNotStealVOD(t *testing.T) {
	vod := "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/"
	program := "https://www3.nhk.or.jp/nhkworld/en/shows/sumo/"
	vodURL, _ := url.Parse(vod)
	programURL, _ := url.Parse(program)
	if NewNhkVodProgramIE().Suitable(vodURL) {
		t.Fatal("program extractor stole VOD URL")
	}
	if !NewNhkVodIE().Suitable(vodURL) {
		t.Fatal("VOD extractor rejected valid episode")
	}
	if !NewNhkVodProgramIE().Suitable(programURL) {
		t.Fatal("program extractor rejected valid program")
	}
	if NewNhkVodIE().Suitable(programURL) {
		t.Fatal("VOD extractor accepted program slug")
	}
}

func TestNHKWorldVODExtract(t *testing.T) {
	episode := nhkReadFixture(t, "world/episode.json")
	master := nhkReadFixture(t, "world/master.m3u8")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://api.nhkworld.jp/showsapi/v1/en/video_episodes/2049165":  episode,
		"https://vod-stream.nhk.jp/nhkworld/en/shows/2049165/index.m3u8": master,
	}}
	result, err := NewNhkVodIE().Extract(context.Background(), Request{
		URL:       "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() {
		t.Fatal("expected media")
	}
	id, _ := result.Info.Lookup("id").StringValue()
	if id != "2049165-en" {
		t.Fatalf("id=%q", id)
	}
	title, _ := result.Info.Lookup("title").StringValue()
	if !strings.Contains(title, "Japan Railway Journal") {
		t.Fatalf("title=%q", title)
	}
	formats, ok := result.Info.Lookup("formats").ListValue()
	if !ok || len(formats) == 0 {
		t.Fatal("missing formats")
	}
}

func TestNHKWorldVODMissingStream(t *testing.T) {
	payload := `{"id":"2049165","lang":"en","title":"Gone","video_program":{"title":"Series"},"video":{}}`
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://api.nhkworld.jp/showsapi/v1/en/video_episodes/2049165": payload,
	}}
	_, err := NewNhkVodIE().Extract(context.Background(), Request{
		URL:       "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/",
		Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestNHKWorldProgramPlaylist(t *testing.T) {
	page := nhkReadFixture(t, "world/program.html")
	api := nhkReadFixture(t, "world/program.json")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www3.nhk.or.jp/nhkworld/en/shows/sumo/":                            page,
		"https://api.nhkworld.jp/showsapi/v1/en/video_programs/sumo/video_episodes": api,
	}}
	result, err := NewNhkVodProgramIE().Extract(context.Background(), Request{
		URL:       "https://www3.nhk.or.jp/nhkworld/en/shows/sumo/",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("expected playlist")
	}
	title, _ := result.Info.Lookup("title").StringValue()
	if title != "Japan Railway Journal" {
		t.Fatalf("title=%q", title)
	}
	first := result.Entries.Iterator()
	entry, ok, err := first.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("entry=%v ok=%v err=%v", entry, ok, err)
	}
	if entry.ExtractorKey != "nhk_vod" {
		t.Fatalf("ie_key=%q", entry.ExtractorKey)
	}
	second := result.Entries.Iterator()
	if _, ok, err := second.Next(context.Background()); err != nil || !ok {
		t.Fatalf("reusable iterator failed: %t %v", ok, err)
	}
}

func TestNHKSchoolBangumiExtract(t *testing.T) {
	page := nhkReadFixture(t, "school/bangumi.html")
	rawURL := "https://www2.nhk.or.jp/school/movie/bangumi.cgi?das_id=D0005150191_00000"
	transport := &nhkFixtureTransport{bodies: map[string]string{rawURL: page}}
	result, err := NewNhkForSchoolBangumiIE().Extract(context.Background(), Request{
		URL: rawURL, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.Info.Lookup("id").StringValue()
	if id != "D0005150191_00003" {
		t.Fatalf("id=%q", id)
	}
	title, _ := result.Info.Lookup("title").StringValue()
	if title != "にている かな" {
		t.Fatalf("title=%q", title)
	}
	chapters, ok := result.Info.Lookup("chapters").ListValue()
	if !ok || len(chapters) != 3 {
		t.Fatalf("chapters=%v", chapters)
	}
	formats, ok := result.Info.Lookup("formats").ListValue()
	if !ok || len(formats) == 0 {
		t.Fatal("missing formats")
	}
	formatObj, ok := formats[0].Object()
	if !ok {
		t.Fatal("format not object")
	}
	formatURL, _ := formatObj.Lookup("url").StringValue()
	if !strings.Contains(formatURL, "D0005150191_00003_V_000.f4v") {
		t.Fatalf("format url=%q", formatURL)
	}
}

func TestNHKSchoolSubjectAndProgramList(t *testing.T) {
	subjectPage := nhkReadFixture(t, "school/subject.html")
	programPage := nhkReadFixture(t, "school/program.html")
	programJSON := nhkReadFixture(t, "school/program.json")
	subjectURL := "https://www.nhk.or.jp/school/rika/"
	programURL := "https://www.nhk.or.jp/school/rika/program-a/"
	transport := &nhkFixtureTransport{bodies: map[string]string{
		subjectURL: subjectPage,
		programURL: programPage,
		"https://www.nhk.or.jp/school/rika/program-a/meta/program.json": programJSON,
	}}

	subject, err := NewNhkForSchoolSubjectIE().Extract(context.Background(), Request{
		URL: subjectURL, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := subject.Entries.Iterator().Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("subject entry: %v %v", ok, err)
	}
	if entry.ExtractorKey != "nhk_for_school_program_list" {
		t.Fatalf("ie_key=%q", entry.ExtractorKey)
	}
	if strings.Contains(entry.URL, "evil") {
		t.Fatal("hostile child link leaked")
	}

	program, err := NewNhkForSchoolProgramListIE().Extract(context.Background(), Request{
		URL: programURL, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	title, _ := program.Info.Lookup("title").StringValue()
	if strings.Contains(title, "NHK for School") {
		t.Fatalf("title suffix not stripped: %q", title)
	}
	bangumi, ok, err := program.Entries.Iterator().Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("bangumi entry: %v %v", ok, err)
	}
	if bangumi.ExtractorKey != "nhk_for_school_bangumi" {
		t.Fatalf("ie_key=%q", bangumi.ExtractorKey)
	}
}

func TestNHKSchoolSubjectAllowlist(t *testing.T) {
	ie := NewNhkForSchoolSubjectIE()
	bad, _ := url.Parse("https://www.nhk.or.jp/school/unknown/")
	good, _ := url.Parse("https://www.nhk.or.jp/school/rika/")
	if ie.Suitable(bad) {
		t.Fatal("unknown subject accepted")
	}
	if !ie.Suitable(good) {
		t.Fatal("rika rejected")
	}
}

func TestNHKSchoolProgramListBeforeSubject(t *testing.T) {
	program, _ := url.Parse("https://www.nhk.or.jp/school/rika/program-a/")
	if !NewNhkForSchoolProgramListIE().Suitable(program) {
		t.Fatal("program list rejected")
	}
	if NewNhkForSchoolSubjectIE().Suitable(program) {
		t.Fatal("subject stole program list URL")
	}
}

func TestNHKRadiruEpisodeAndPlaylist(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	series := nhkReadFixture(t, "radio/series.json")
	episodeM3U8 := nhkReadFixture(t, "radio/episode.m3u8")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml":          config,
		"https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/series": series,
		"https://radio-stream.nhk.jp/ondemand/4251382/index.m3u8":    episodeM3U8,
	}}

	episode, err := NewNhkRadiruIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01_4251382",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := episode.Info.Lookup("id").StringValue()
	if id != "LG96ZW5KZ4_01_4251382" {
		t.Fatalf("id=%q", id)
	}
	title, _ := episode.Info.Lookup("title").StringValue()
	if !strings.Contains(title, "クラシック") {
		t.Fatalf("title=%q", title)
	}

	playlist, err := NewNhkRadiruIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radio/ondemand/detail.html?p=LG96ZW5KZ4_01",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !playlist.IsPlaylist() {
		t.Fatal("expected playlist")
	}
}

func TestNHKRadioNewsHandoff(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	series := nhkReadFixture(t, "radio/series.json")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml":          config,
		"https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/series": series,
	}}
	result, err := NewNhkRadioNewsPageIE().Extract(context.Background(), Request{
		URL: "https://www.nhk.or.jp/radionews/", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("news handoff should produce playlist for corner without headline")
	}
	id, _ := result.Info.Lookup("id").StringValue()
	if id != "18439M2W42_01" {
		t.Fatalf("id=%q", id)
	}
}

func TestNHKRadiruLiveDefaultAndArea(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	live := nhkReadFixture(t, "radio/live.m3u8")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml":           config,
		"https://radio-stream.nhk.jp/hls/live/r1-tokyo/master.m3u8":   live,
		"https://radio-stream.nhk.jp/hls/live/fm-sapporo/master.m3u8": live,
	}}
	tokyo, err := NewNhkRadiruLiveIE().Extract(context.Background(), Request{
		URL: "https://www.nhk.or.jp/radio/player/?ch=r1", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := tokyo.Info.Lookup("id").StringValue()
	if id != "bs-r1-130" {
		t.Fatalf("id=%q", id)
	}
	liveStatus, _ := tokyo.Info.Lookup("live_status").StringValue()
	if liveStatus != "is_live" {
		t.Fatalf("live_status=%q", liveStatus)
	}

	sapporo, err := NewNhkRadiruLiveIE().Extract(context.Background(), Request{
		URL: "https://www.nhk.or.jp/radio/player/?ch=fm", Transport: transport,
		NHK: NHKOptions{RadiruArea: "sapporo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := sapporo.Info.Lookup("id").StringValue()
	if sid != "bs-r3-010" {
		t.Fatalf("sapporo id=%q", sid)
	}
}

func TestNHKRadiruLiveInvalidArea(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml": config,
	}}
	_, err := NewNhkRadiruLiveIE().Extract(context.Background(), Request{
		URL: "https://www.nhk.or.jp/radio/player/?ch=r1", Transport: transport,
		NHK: NHKOptions{RadiruArea: "not-a-real-area"},
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
	for _, call := range transport.calls {
		if strings.Contains(call, "radio-stream") {
			t.Fatal("media fetched before area validation")
		}
	}
}

func TestNHKRadiruMissingHeadline(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	series := nhkReadFixture(t, "radio/series.json")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml":          config,
		"https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/series": series,
	}}
	_, err := NewNhkRadiruIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01_missing",
		Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestNHKCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewNhkVodIE().Extract(ctx, Request{
		URL:       "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/",
		Transport: &nhkFixtureTransport{bodies: map[string]string{}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestNHKConcurrentExtract(t *testing.T) {
	page := nhkReadFixture(t, "school/bangumi.html")
	rawURL := "https://www2.nhk.or.jp/school/movie/bangumi.cgi?das_id=D0005150191_00000"
	transport := &nhkFixtureTransport{bodies: map[string]string{rawURL: page}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := NewNhkForSchoolBangumiIE().Extract(context.Background(), Request{
				URL: rawURL, Transport: transport,
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestNHKSecretSafeErrors(t *testing.T) {
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://api.nhkworld.jp/showsapi/v1/en/video_episodes/2049165": `{"token":"super-secret-token","id":"2049165"}`,
	}, statuses: map[string]int{
		"https://api.nhkworld.jp/showsapi/v1/en/video_episodes/2049165": http.StatusUnauthorized,
	}}
	_, err := NewNhkVodIE().Extract(context.Background(), Request{
		URL: "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/", Transport: transport,
	})
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func FuzzClassifyNHKURL(f *testing.F) {
	seeds := []string{
		"https://www3.nhk.or.jp/nhkworld/en/shows/2049165/",
		"https://www3.nhk.or.jp/nhkworld/en/shows/sumo/",
		"https://www2.nhk.or.jp/school/movie/bangumi.cgi?das_id=D0005150191_00000",
		"https://www.nhk.or.jp/school/rika/",
		"https://www.nhk.or.jp/school/rika/program-a/",
		"https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01",
		"https://www.nhk.or.jp/radionews/",
		"https://www.nhk.or.jp/radio/player/?ch=r1",
		"https://evil.example/nhkworld/en/shows/2049165/",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	ies := []Extractor{
		NewNhkVodIE(), NewNhkVodProgramIE(),
		NewNhkForSchoolBangumiIE(), NewNhkForSchoolSubjectIE(), NewNhkForSchoolProgramListIE(),
		NewNhkRadiruLiveIE(), NewNhkRadioNewsPageIE(), NewNhkRadiruIE(),
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		matched := 0
		for _, ie := range ies {
			if ie.Suitable(parsed) {
				matched++
			}
		}
		if matched > 2 {
			t.Fatalf("too many matches for %q: %d", raw, matched)
		}
	})
}

func FuzzParseNHKWorldEpisode(f *testing.F) {
	f.Add([]byte(`{"id":"2049165","title":"t","video":{"url":"https://vod-stream.nhk.jp/x.m3u8","duration":1}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		var payload map[string]any
		_ = nhkTestDecodeJSON(data, &payload)
	})
}

func FuzzParseNHKSchoolPage(f *testing.F) {
	f.Add([]byte(`var r_version = "00003"; programObj.name = "x";`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256<<10 {
			return
		}
		_ = nhkSchoolExtractQuotedVars(data)
		_ = nhkSchoolExtractProgramObj(data)
		_ = nhkSchoolExtractChapters(data, 600)
	})
}

func FuzzParseNHKRadiruResponse(f *testing.F) {
	f.Add([]byte(`{"main":{"episodes":[{"id":"1","title":"t"}]}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		var payload map[string]any
		if err := nhkTestDecodeJSON(data, &payload); err != nil {
			return
		}
		_, err := nhkRadioBuildSeries(&nhkRadioConfig{Series: map[string]*nhkRadioConfigSeries{}}, payload, "SITE", "01")
		if err != nil && !errors.Is(err, ErrInvalidMetadata) && !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("uncategorized: %v", err)
		}
	})
}

func FuzzParseNHKConfigXML(f *testing.F) {
	f.Add([]byte(`<root><data><area>tokyo</area><areakey>130</areakey><r1hls>https://x/a.m3u8</r1hls></data></root>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		cfg, err := nhkRadioParseConfigXML(data)
		if err != nil {
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("uncategorized: %v", err)
			}
			return
		}
		if cfg == nil || cfg.Live.Areas == nil {
			t.Fatal("nil config")
		}
		if len(cfg.Live.Areas) > nhkRadioMaxAreas {
			t.Fatal("area bound exceeded")
		}
	})
}

func nhkTestDecodeJSON(data []byte, target any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrInvalidMetadata
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(target); err != nil {
		return err
	}
	if dec.More() {
		return ErrInvalidMetadata
	}
	return nil
}
