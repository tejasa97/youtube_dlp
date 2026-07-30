package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

func (t *nhkFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	return t.Do(ctx, request)
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

type nhkBareTransport struct {
	bodies map[string]string
}

func (t *nhkBareTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	body, ok := t.bodies[request.URL.String()]
	if !ok {
		return nil, errors.New("missing fixture")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (t *nhkBareTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := t.Do(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.Header.Clone(), err
}

type nhkRedirectTransport struct {
	bodies map[string]string
}

func (t *nhkRedirectTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	body, ok := t.bodies[request.URL.String()]
	if !ok {
		return nil, errors.New("missing fixture")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (t *nhkRedirectTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := t.Do(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.Header.Clone(), err
}

func (t *nhkRedirectTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{"https://evil.example/redirected.m3u8"}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}

func nhkReadFixture(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "nhk", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNHKWorldVODClipAPIURL(t *testing.T) {
	clip := nhkReadFixture(t, "world/clip_episode.json")
	master := nhkReadFixture(t, "world/master.m3u8")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://api.nhkworld.jp/showsapi/v1/en/video_clips/9999011":     clip,
		"https://vod-stream.nhk.jp/nhkworld/en/clips/9999011/index.m3u8": master,
	}}
	result, err := NewNhkVodIE().Extract(context.Background(), Request{
		URL:       "https://www3.nhk.or.jp/nhkworld/en/ondemand/video/9999011/",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantAPI := "https://api.nhkworld.jp/showsapi/v1/en/video_clips/9999011"
	found := false
	for _, call := range transport.calls {
		if call == wantAPI {
			found = true
		}
		if strings.Contains(call, "/video_clips/9999011/video_clips") {
			t.Fatalf("doubled clip path in %q", call)
		}
	}
	if !found {
		t.Fatalf("missing clip API call, got %v", transport.calls)
	}
	id, _ := result.Info.Lookup("id").StringValue()
	if id != "9999011-en" {
		t.Fatalf("id=%q", id)
	}
}

func TestNHKWorldVODTrailingPathRejected(t *testing.T) {
	vodParsed, _ := url.Parse("https://www3.nhk.or.jp/nhkworld/en/shows/2049165/extra/")
	if NewNhkVodIE().Suitable(vodParsed) {
		t.Fatal("VOD accepted trailing path")
	}
	programParsed, _ := url.Parse("https://www3.nhk.or.jp/nhkworld/en/shows/sumo/extra/")
	if NewNhkVodProgramIE().Suitable(programParsed) {
		t.Fatal("program accepted trailing path")
	}
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
	isolated, ok := formatObj.Lookup("_credential_isolated").Bool()
	if !ok || !isolated {
		t.Fatal("NHK School format is not credential-isolated")
	}
}

func TestNHKSchoolBangumiTrailingPathRejected(t *testing.T) {
	ie := NewNhkForSchoolBangumiIE()
	for _, raw := range []string{
		"https://www2.nhk.or.jp/school/movie/bangumi.cgi/extra?das_id=D0005150191_00000",
		"https://www2.nhk.or.jp/school/movie/clip.cgi/extra?das_id=D0005150191_00000",
		"https://www2.nhk.or.jp/school/movie/bangumi.cgi/?das_id=D0005150191_00000",
		"https://www2.nhk.or.jp/school/movie/clip.cgi/?das_id=D0005150191_00000",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if ie.Suitable(parsed) {
			t.Fatalf("trailing School route accepted: %s", raw)
		}
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

func TestNHKRadioSuitableURLBounds(t *testing.T) {
	for _, test := range []struct {
		name string
		base string
		ie   Extractor
	}{
		{name: "on-demand", base: "https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01&pad=", ie: NewNhkRadiruIE()},
		{name: "news", base: "https://www.nhk.or.jp/radionews/?pad=", ie: NewNhkRadioNewsPageIE()},
		{name: "live", base: "https://www.nhk.or.jp/radio/player/?ch=r1&pad=", ie: NewNhkRadiruLiveIE()},
	} {
		t.Run(test.name, func(t *testing.T) {
			exact := test.base + strings.Repeat("a", nhkRadioMaxURLBytes-len(test.base))
			if len(exact) != nhkRadioMaxURLBytes {
				t.Fatalf("fixture length = %d", len(exact))
			}
			parsed, err := url.Parse(exact)
			if err != nil {
				t.Fatal(err)
			}
			if !test.ie.Suitable(parsed) {
				t.Fatal("boundary URL rejected")
			}
			over, err := url.Parse(exact + "a")
			if err != nil {
				t.Fatal(err)
			}
			if test.ie.Suitable(over) {
				t.Fatal("over-bound URL accepted")
			}
		})
	}
}

func TestNHKRadioXMLAttributeBound(t *testing.T) {
	if _, err := nhkRadioParseConfigXML(nhkRadioXMLAttributes(nhkRadioMaxXMLAttributes)); err != nil {
		t.Fatalf("boundary attributes rejected: %v", err)
	}
	if _, err := nhkRadioParseConfigXML(nhkRadioXMLAttributes(nhkRadioMaxXMLAttributes + 1)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("over-bound attributes error = %v", err)
	}
}

func TestNHKNumericMetadataBounds(t *testing.T) {
	for _, text := range []string{"NaN", "Inf", "-Inf", "-1", "-0", "604801", "00:60:00", "00:00:60"} {
		if got, ok := nhkSchoolParseDurationChecked(text); ok || got != 0 {
			t.Errorf("nhkSchoolParseDurationChecked(%q) = %v, %t", text, got, ok)
		}
	}
	for _, text := range []string{"1", "00:00:01", "168:00:00"} {
		if got, ok := nhkSchoolParseDurationChecked(text); !ok || got <= 0 || math.IsNaN(got) || math.IsInf(got, 0) {
			t.Errorf("nhkSchoolParseDurationChecked(%q) = %v, %t", text, got, ok)
		}
	}
	if chapters := nhkSchoolExtractChapters([]byte(`chapterTime.push('999999');<div class="cpTitle"><span>scene 1</span>bad</div>`), 0); len(chapters) != 0 {
		t.Fatalf("out-of-range chapter time emitted: %v", chapters)
	}

	for _, raw := range []any{
		math.NaN(), math.Inf(1), math.Inf(-1), "Inf", "-1", "0",
		float64(nhkMaxDurationSec + 1), json.Number("604801"),
	} {
		if duration, ok := nhkRadioDurationSeconds(raw); ok || duration != 0 {
			t.Errorf("nhkRadioDurationSeconds(%v) = %v, %t", raw, duration, ok)
		}
	}
	if duration, ok := nhkRadioDurationSeconds(json.Number("42.5")); !ok || duration != 42.5 {
		t.Fatalf("valid radio duration = %v, %t", duration, ok)
	}
	if got := (&nhkRadioEpisode{DurationSec: math.Inf(1)}).DurationSeconds(); got != 0 {
		t.Fatalf("infinite episode duration = %v", got)
	}

	for _, raw := range []any{
		math.NaN(), math.Inf(1), float64(-1), float64(0), float64(1.5),
		float64(nhkMaxDimension + 1), int64(0), json.Number("100001"),
	} {
		if dimension, ok := nhkIntFromAny(raw); ok || dimension != 0 {
			t.Errorf("nhkIntFromAny(%v) = %d, %t", raw, dimension, ok)
		}
	}
	if dimension, ok := nhkIntFromAny(json.Number("100000")); !ok || dimension != nhkMaxDimension {
		t.Fatalf("boundary dimension = %d, %t", dimension, ok)
	}
	thumbnails := nhkThumbnails(map[string]any{"images": []any{map[string]any{
		"url": "https://vod-stream.nhk.jp/image.jpg", "width": math.NaN(), "height": math.Inf(1),
	}}}, "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/")
	if len(thumbnails) != 1 {
		t.Fatalf("thumbnail count = %d", len(thumbnails))
	}
	object, _ := thumbnails[0].Object()
	if _, ok := object.Lookup("width").Int(); ok {
		t.Fatalf("non-finite width emitted: %v", object)
	}
	if _, ok := object.Lookup("height").Int(); ok {
		t.Fatalf("non-finite dimensions emitted: %v", object)
	}
}

func nhkRadioXMLAttributes(count int) []byte {
	var builder strings.Builder
	builder.WriteString("<root")
	for index := 0; index < count; index++ {
		_, _ = fmt.Fprintf(&builder, " a%d=\"x\"", index)
	}
	builder.WriteString("/>")
	return []byte(builder.String())
}

func TestNHKRadiruEpisodeAndPlaylist(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	series := nhkReadFixture(t, "radio/series.json")
	extended := nhkReadFixture(t, "radio/extended_detail.json")
	episodeM3U8 := nhkReadFixture(t, "radio/episode.m3u8")
	seriesURL := "https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/series?site_id=LG96ZW5KZ4&corner_site_id=01"
	extendedURL := "https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/program/R113020250707.json"
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml": config,
		seriesURL:   series,
		extendedURL: extended,
		"https://radio-stream.nhk.jp/ondemand/4251382/index.m3u8": episodeM3U8,
	}}

	episode, err := NewNhkRadiruIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01_4251382",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundSeries := false
	for _, call := range transport.calls {
		if call == seriesURL {
			foundSeries = true
		}
		if strings.Contains(call, "corner_id=") {
			t.Fatalf("series API used corner_id: %q", call)
		}
	}
	if !foundSeries {
		t.Fatalf("missing series API call with corner_site_id, got %v", transport.calls)
	}
	id, _ := episode.Info.Lookup("id").StringValue()
	if id != "LG96ZW5KZ4_01_4251382" {
		t.Fatalf("id=%q", id)
	}
	title, _ := episode.Info.Lookup("title").StringValue()
	if title != "Extended Classic Feature" {
		t.Fatalf("title=%q", title)
	}
	description, _ := episode.Info.Lookup("description").StringValue()
	if !strings.Contains(description, "Extended Radiru description") {
		t.Fatalf("description=%q", description)
	}
	cast, ok := episode.Info.Lookup("cast").ListValue()
	if !ok || len(cast) == 0 {
		t.Fatal("missing extended cast")
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
	seriesTitle, _ := playlist.Info.Lookup("title").StringValue()
	if !strings.Contains(seriesTitle, "ベストオブクラシック") {
		t.Fatalf("series title=%q", seriesTitle)
	}
}

func TestNHKRadiruNumericEpisodeID(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	series := nhkReadFixture(t, "radio/series.json")
	episodeM3U8 := nhkReadFixture(t, "radio/episode.m3u8")
	seriesURL := "https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/series?site_id=LG96ZW5KZ4&corner_site_id=01"
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml": config,
		seriesURL: series,
		"https://radio-stream.nhk.jp/ondemand/4251382/index.m3u8": episodeM3U8,
	}}
	_, err := NewNhkRadiruIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01_4251382",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNHKRadiruExtendedMetadataFailureNonfatal(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	series := nhkReadFixture(t, "radio/series.json")
	episodeM3U8 := nhkReadFixture(t, "radio/episode.m3u8")
	seriesURL := "https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/series?site_id=LG96ZW5KZ4&corner_site_id=01"
	extendedURL := "https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/program/R113020250707.json"
	transport := &nhkFixtureTransport{
		bodies: map[string]string{
			"https://www.nhk.or.jp/radio/config/config_web.xml": config,
			seriesURL: series,
			"https://radio-stream.nhk.jp/ondemand/4251382/index.m3u8": episodeM3U8,
		},
		statuses: map[string]int{extendedURL: http.StatusBadRequest},
	}
	result, err := NewNhkRadiruIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01_4251382",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	title, _ := result.Info.Lookup("title").StringValue()
	if !strings.Contains(title, "クラシック") {
		t.Fatalf("fallback title=%q", title)
	}
}

func TestNHKRadioNewsHandoff(t *testing.T) {
	result, err := NewNhkRadioNewsPageIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radionews/",
		Transport: &nhkFixtureTransport{bodies: map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsURL() {
		t.Fatal("news page should return URL result")
	}
	if result.Redirect == nil {
		t.Fatal("missing redirect entry")
	}
	if result.Redirect.ExtractorKey != "nhk_radiru" {
		t.Fatalf("ie_key=%q", result.Redirect.ExtractorKey)
	}
	want := "https://www.nhk.or.jp/radio/ondemand/detail.html?p=18439M2W42_01"
	if result.Redirect.URL != want {
		t.Fatalf("url=%q want %q", result.Redirect.URL, want)
	}
}

func TestNHKRadiruNewsPlaylistAndHeadline(t *testing.T) {
	news := nhkReadFixture(t, "radio/news.json")
	episodeM3U8 := nhkReadFixture(t, "radio/episode.m3u8")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/s-media/news/news-site/list/v1/all.json": news,
		"https://radio-stream.nhk.jp/news/2025072701/index.m3u8":        episodeM3U8,
	}}
	playlist, err := NewNhkRadiruIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radio/ondemand/detail.html?p=18439M2W42_01",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !playlist.IsPlaylist() {
		t.Fatal("expected news playlist")
	}
	id, _ := playlist.Info.Lookup("id").StringValue()
	if id != "18439M2W42_01" {
		t.Fatalf("id=%q", id)
	}
	title, _ := playlist.Info.Lookup("title").StringValue()
	if title != "NHKラジオニュース" {
		t.Fatalf("title=%q", title)
	}
	for _, call := range transport.calls {
		if strings.Contains(call, "/ondemand/series") {
			t.Fatalf("news used ordinary series endpoint: %q", call)
		}
	}

	headline, err := NewNhkRadiruIE().Extract(context.Background(), Request{
		URL:       "https://www.nhk.or.jp/radio/ondemand/detail.html?p=18439M2W42_01_2025072701",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	hid, _ := headline.Info.Lookup("id").StringValue()
	if hid != "18439M2W42_01_2025072701" {
		t.Fatalf("headline id=%q", hid)
	}
	htitle, _ := headline.Info.Lookup("title").StringValue()
	if htitle != "正午のニュース" {
		t.Fatalf("headline title=%q", htitle)
	}
}

func TestNHKRadioNewsExtraPathRejected(t *testing.T) {
	parsed, _ := url.Parse("https://www.nhk.or.jp/radionews/extra")
	if NewNhkRadioNewsPageIE().Suitable(parsed) {
		t.Fatal("accepted /radionews/extra")
	}
}

func TestNHKRadiruLiveDefaultAndArea(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	live := nhkReadFixture(t, "radio/live.m3u8")
	noa := nhkReadFixture(t, "radio/noa_tokyo.json")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml":           config,
		"https://www.nhk.or.jp/radio/config/now_on_air/130.json":      noa,
		"https://www.nhk.or.jp/radio/config/now_on_air/010.json":      noa,
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
	title, _ := tokyo.Info.Lookup("title").StringValue()
	if !strings.Contains(title, "NHKラジオ第1・東京") {
		t.Fatalf("title=%q", title)
	}
	thumb, _ := tokyo.Info.Lookup("thumbnail").StringValue()
	if !strings.Contains(thumb, "r1-logo.svg") {
		t.Fatalf("thumbnail=%q", thumb)
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

func TestNHKRadiruLiveR2NationalCrossArea(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	live := nhkReadFixture(t, "radio/live.m3u8")
	noa := nhkReadFixture(t, "radio/noa_tokyo.json")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml":      config,
		"https://www.nhk.or.jp/radio/config/now_on_air/800.json": noa,
		"https://radio-stream.nhk.jp/hls/live/r2/master.m3u8":    live,
	}}
	result, err := NewNhkRadiruLiveIE().Extract(context.Background(), Request{
		URL: "https://www.nhk.or.jp/radio/player/?ch=r2", Transport: transport,
		NHK: NHKOptions{RadiruArea: "fukuoka"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.Info.Lookup("id").StringValue()
	if id != "bs-r2-400" {
		t.Fatalf("id=%q", id)
	}
	title, _ := result.Info.Lookup("title").StringValue()
	if strings.Contains(title, "福岡") {
		t.Fatalf("R2 title should remain national: %q", title)
	}
}

func TestNHKRadiruLiveUnavailableMetadataFallback(t *testing.T) {
	config := nhkReadFixture(t, "radio/config_web.xml")
	live := nhkReadFixture(t, "radio/live.m3u8")
	transport := &nhkFixtureTransport{bodies: map[string]string{
		"https://www.nhk.or.jp/radio/config/config_web.xml":         config,
		"https://radio-stream.nhk.jp/hls/live/r1-tokyo/master.m3u8": live,
	}, statuses: map[string]int{
		"https://www.nhk.or.jp/radio/config/now_on_air/130.json": http.StatusNotFound,
	}}
	result, err := NewNhkRadiruLiveIE().Extract(context.Background(), Request{
		URL: "https://www.nhk.or.jp/radio/player/?ch=r1", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.Info.Lookup("id").StringValue()
	if id != "bs-r1-130" {
		t.Fatalf("fallback id=%q", id)
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

func TestNHKSchoolSougouSubjectTitle(t *testing.T) {
	page := nhkReadFixture(t, "school/subject_sougou.html")
	subjectURL := "https://www.nhk.or.jp/school/sougou/"
	transport := &nhkFixtureTransport{bodies: map[string]string{subjectURL: page}}
	result, err := NewNhkForSchoolSubjectIE().Extract(context.Background(), Request{
		URL: subjectURL, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	title, _ := result.Info.Lookup("title").StringValue()
	if title != "総合的な学習の時間" {
		t.Fatalf("title=%q", title)
	}
}

func TestNHKCredentialIsolationRequired(t *testing.T) {
	episode := nhkReadFixture(t, "world/episode.json")
	transport := &nhkBareTransport{bodies: map[string]string{
		"https://api.nhkworld.jp/showsapi/v1/en/video_episodes/2049165": episode,
	}}
	_, err := NewNhkVodIE().Extract(context.Background(), Request{
		URL:       "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/",
		Transport: transport,
	})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("err=%v", err)
	}
}

func TestNHKCredentialIsolatedFormatsMarked(t *testing.T) {
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
	formats, ok := result.Info.Lookup("formats").ListValue()
	if !ok || len(formats) == 0 {
		t.Fatal("missing formats")
	}
	formatObj, ok := formats[0].Object()
	if !ok {
		t.Fatal("format not object")
	}
	isolated, ok := formatObj.Lookup("_credential_isolated").Bool()
	if !ok || !isolated {
		t.Fatal("format not credential-isolated")
	}
}

func TestNHKHostileMediaURLRejected(t *testing.T) {
	for _, raw := range []string{
		"https://127.0.0.1/a.m3u8",
		"https://localhost/a.m3u8",
		"http://user:pass@radio-stream.nhk.jp/a.m3u8",
		"https://radio-stream.nhk.jp:8443/a.m3u8",
		"https://radio-stream.nhk.jp/a.m3u8#frag",
	} {
		if nhkValidPublicURL(raw) {
			t.Fatalf("accepted hostile media URL %q", raw)
		}
	}
}

func TestNHKJSONTrailingGarbageRejected(t *testing.T) {
	cases := []struct {
		name string
		fn   func([]byte) error
	}{
		{"school", func(data []byte) error {
			_, err := nhkSchoolParseProgramJSON(data)
			return err
		}},
		{"radiru", func(data []byte) error {
			_, err := nhkRadioFetchSeriesJSON(data)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn([]byte(`{} {"x":1}`)); err == nil {
				t.Fatal("accepted trailing JSON")
			}
		})
	}
}

func TestNHKRedirectNotFollowedForMedia(t *testing.T) {
	episode := nhkReadFixture(t, "world/episode.json")
	transport := &nhkRedirectTransport{
		bodies: map[string]string{
			"https://api.nhkworld.jp/showsapi/v1/en/video_episodes/2049165": episode,
		},
	}
	_, err := NewNhkVodIE().Extract(context.Background(), Request{
		URL:       "https://www3.nhk.or.jp/nhkworld/en/shows/2049165/",
		Transport: transport,
	})
	if err == nil {
		t.Fatal("expected redirect failure")
	}
	if strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("redirect target leaked: %v", err)
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
	for _, base := range []string{
		"https://www.nhk.or.jp/radio/player/ondemand.html?p=LG96ZW5KZ4_01&pad=",
		"https://www.nhk.or.jp/radionews/?pad=",
		"https://www.nhk.or.jp/radio/player/?ch=r1&pad=",
	} {
		seeds = append(seeds,
			base+strings.Repeat("a", nhkRadioMaxURLBytes-len(base)),
			base+strings.Repeat("a", nhkRadioMaxURLBytes-len(base)+1),
		)
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
		if len(raw) > nhkRadioMaxURLBytes+1 {
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
	f.Add([]byte(`var r_duration = "Inf"; chapterTime.push('00:00:00');`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256<<10 {
			return
		}
		variables := nhkSchoolExtractQuotedVars(data)
		_ = nhkSchoolExtractProgramObj(data)
		_ = nhkSchoolExtractChapters(data, 600)
		_, _ = nhkSchoolParseDurationChecked(variables["r_duration"])
		_, _ = nhkSchoolParseDurationChecked(string(data))
	})
}

func FuzzParseNHKRadiruResponse(f *testing.F) {
	f.Add([]byte(`{"main":{"episodes":[{"id":"1","title":"t"}]}}`))
	f.Add([]byte(`{"duration":"Inf","width":1e999,"height":-1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		var payload map[string]any
		if err := nhkTestDecodeJSON(data, &payload); err != nil {
			return
		}
		_, _ = nhkRadioDurationSeconds(payload["duration"])
		_, _ = nhkIntFromAny(payload["width"])
		_, _ = nhkIntFromAny(payload["height"])
		_, err := nhkRadioBuildSeries(&nhkRadioConfig{Series: map[string]*nhkRadioConfigSeries{}}, payload, "SITE", "01")
		if err != nil && !errors.Is(err, ErrInvalidMetadata) && !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("uncategorized: %v", err)
		}
	})
}

func FuzzParseNHKConfigXML(f *testing.F) {
	f.Add([]byte(`<root><data><area>tokyo</area><areakey>130</areakey><r1hls>https://x/a.m3u8</r1hls></data></root>`))
	f.Add(nhkRadioXMLAttributes(nhkRadioMaxXMLAttributes))
	f.Add(nhkRadioXMLAttributes(nhkRadioMaxXMLAttributes + 1))
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
	return ensureJSONEOF(dec)
}

func nhkRadioFetchSeriesJSON(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrInvalidMetadata)
	}
	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&payload); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	return payload, nil
}
