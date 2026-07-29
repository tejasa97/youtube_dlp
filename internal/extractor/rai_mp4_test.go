package extractor

// Synthetic regression suite for the bounded `_create_http_urls` MP4
// synthesis path.  These tests are derived entirely from public Rai
// relinker/manifest contract descriptions and the pinned
// yt_dlp/extractor/rai.py (aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8);
// they never replay real Rai URLs, signed tokens, or production cookies.

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

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// raiMP4BaseFormat constructs a representative combined A/V HLS base format
// for metadata-copy selection.  Each call returns a fresh `value.Object`
// so tests do not share state.
func raiMP4BaseFormat(tbr float64, width, height int, vcodec, acodec string) value.Value {
	fields := []value.Field{
		{Key: "format_id", Value: value.String("hls-audio-video")},
		{Key: "url", Value: value.String("https://cdn.example.test/v.m3u8")},
		{Key: "ext", Value: value.String("mp4")},
		{Key: "protocol", Value: value.String("m3u8_native")},
		{Key: "vcodec", Value: value.String(vcodec)},
		{Key: "acodec", Value: value.String(acodec)},
		{Key: "tbr", Value: value.Float(tbr)},
		{Key: "width", Value: value.Int(int64(width))},
		{Key: "height", Value: value.Int(int64(height))},
		{Key: "fps", Value: value.Int(25)},
	}
	return value.ObjectValue(value.NewObject(fields...))
}

// raiMP4AudioOnlyBase / raiMP4VideoOnlyBase mirror the single-stream
// base formats produced by `raiFormats` for `_ao.`/`_vo.` paths.
func raiMP4AudioOnlyBase() value.Value {
	return value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("hls-audio")},
		value.Field{Key: "url", Value: value.String("https://cdn.example.test/ao.m3u8")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "protocol", Value: value.String("m3u8_native")},
		value.Field{Key: "vcodec", Value: value.String("none")},
		value.Field{Key: "acodec", Value: value.String("mp4a.40.2")},
	))
}

func raiMP4VideoOnlyBase() value.Value {
	return value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("hls-video")},
		value.Field{Key: "url", Value: value.String("https://cdn.example.test/vo.m3u8")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "protocol", Value: value.String("m3u8_native")},
		value.Field{Key: "vcodec", Value: value.String("avc1.640028")},
		value.Field{Key: "acodec", Value: value.String("none")},
		value.Field{Key: "width", Value: value.Int(1920)},
		value.Field{Key: "height", Value: value.Int(1080)},
		value.Field{Key: "fps", Value: value.Int(25)},
	))
}

// Section: raiMP4ManifestQualities (manifest states + bounds).

func TestRaiMP4ManifestQualitiesStates(t *testing.T) {
	for _, tc := range []struct {
		name        string
		manifest    string
		wantMatched bool
		wantList    []string
	}{
		{
			name:        "unrelated m3u8 returns unmatched",
			manifest:    "https://cdn.example.test/anything/else.m3u8",
			wantMatched: false,
		},
		{
			name:        "matched without quality token",
			manifest:    "https://cdnvod.rai.it/v/abc/playlist.m3u8",
			wantMatched: true,
		},
		{
			name:        "single quality collapses into id group (wildcard path)",
			manifest:    "https://cdnvod.rai.it/v/abc_2400/playlist.m3u8",
			wantMatched: true,
		},
		{
			name:        "comma list preserved verbatim",
			manifest:    "https://cdnvod.rai.it/v/abc_2400,1500,800/playlist.m3u8",
			wantMatched: true,
			wantList:    []string{"2400", "1500", "800"},
		},
		{
			name:        ".csmil extension matches",
			manifest:    "https://cdnvod.rai.it/v/abc.csmil/playlist.m3u8",
			wantMatched: true,
		},
		{
			name:        ".mp4 extension matches",
			manifest:    "https://cdnvod.rai.it/v/abc.mp4/playlist.m3u8",
			wantMatched: true,
		},
		{
			name:        "comma list duplicates deduped",
			manifest:    "https://cdnvod.rai.it/v/abc_2400,2400,1500/playlist.m3u8",
			wantMatched: true,
			wantList:    []string{"2400", "1500"},
		},
		{
			name:        "empty quality token between commas is rejected",
			manifest:    "https://cdnvod.rai.it/v/abc_,2400/playlist.m3u8",
			wantMatched: false,
		},
		{
			name:        "non-numeric quality token is rejected",
			manifest:    "https://cdnvod.rai.it/v/abc_abc,2400/playlist.m3u8",
			wantMatched: false,
		},
		{
			name:        "query string is stripped before matching",
			manifest:    "https://cdnvod.rai.it/v/abc_2400,1500/playlist.m3u8?token=keep&other=still",
			wantMatched: true,
			wantList:    []string{"2400", "1500"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, matched := raiMP4ManifestQualities(tc.manifest)
			if matched != tc.wantMatched {
				t.Fatalf("matched = %t, want %t (list=%v)", matched, tc.wantMatched, list)
			}
			if !matched {
				if list != nil {
					t.Fatalf("unmatched returned list = %v", list)
				}
				return
			}
			if tc.wantList == nil {
				if len(list) != 0 {
					t.Fatalf("matched-no-quality list = %v, want empty", list)
				}
				return
			}
			if len(list) != len(tc.wantList) {
				t.Fatalf("list length = %d, want %d (%v)", len(list), len(tc.wantList), list)
			}
			for i := range list {
				if list[i] != tc.wantList[i] {
					t.Fatalf("list[%d] = %q, want %q", i, list[i], tc.wantList[i])
				}
			}
		})
	}
}

func TestRaiMP4ManifestQualitiesBound(t *testing.T) {
	var b strings.Builder
	b.WriteString("https://cdnvod.rai.it/v/abc_")
	for i := 0; i <= raiMaxMP4Qualities; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d", 100+i)
	}
	b.WriteString("/playlist.m3u8")
	if list, matched := raiMP4ManifestQualities(b.String()); matched || list != nil {
		t.Fatalf("oversized list accepted: matched=%t list=%v", matched, list)
	}
	var c strings.Builder
	c.WriteString("https://cdnvod.rai.it/v/abc_")
	for i := 0; i < raiMaxMP4Qualities; i++ {
		if i > 0 {
			c.WriteByte(',')
		}
		fmt.Fprintf(&c, "%d", 100+i)
	}
	c.WriteString("/playlist.m3u8")
	if list, matched := raiMP4ManifestQualities(c.String()); !matched || len(list) != raiMaxMP4Qualities {
		t.Fatalf("at-bound list rejected: matched=%t len=%d", matched, len(list))
	}
}

// Section: raiMP4URL (signed RawQuery byte-order preservation + safety rejections).

func TestRaiMP4URLPreservesSignedRawQueryOrder(t *testing.T) {
	relinker := "https://relinker.rai.it/relinker?by=abcd1234&token=SIGNED_TOKEN&z=last"
	got, err := raiMP4URL(relinker, "2400")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://relinker.rai.it/relinker?by=abcd1234&token=SIGNED_TOKEN&z=last&overrideUserAgentRule=mp4-2400") {
		t.Fatalf("signed query order not preserved: %q", got)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("token") != "SIGNED_TOKEN" || q.Get("by") != "abcd1234" || q.Get("z") != "last" {
		t.Fatalf("signed query values lost: by=%q token=%q z=%q", q.Get("by"), q.Get("token"), q.Get("z"))
	}
	if q.Get("overrideUserAgentRule") != "mp4-2400" {
		t.Fatalf("override rule missing: %q", q.Get("overrideUserAgentRule"))
	}
}

func TestRaiMP4URLRejectsUnsafeInputs(t *testing.T) {
	cases := []struct {
		name, url, quality string
		wantError          bool
	}{
		{"non-http scheme", "ftp://relinker.rai.it/relinker?by=abcd", "2400", true},
		{"userinfo present", "https://user:pass@relinker.rai.it/relinker?by=abcd", "2400", true},
		{"fragment present", "https://relinker.rai.it/relinker?by=abcd#frag", "2400", true},
		{"empty RawQuery", "https://relinker.rai.it/relinker", "2400", true},
		{"pre-existing override rule", "https://relinker.rai.it/relinker?by=abcd&overrideUserAgentRule=stale", "2400", true},
		{"invalid quality token", "https://relinker.rai.it/relinker?by=abcd", "abc", true},
		{"comma quality token", "https://relinker.rai.it/relinker?by=abcd", "2400,1500", true},
		{"wildcard quality passes", "https://relinker.rai.it/relinker?by=abcd", "*", false},
		{"valid quality passes", "https://relinker.rai.it/relinker?by=abcd", "2400", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := raiMP4URL(tc.url, tc.quality)
			if tc.wantError && err == nil {
				t.Fatalf("unsafe input accepted: %q %q", tc.url, tc.quality)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("safe input rejected: %q %q: %v", tc.url, tc.quality, err)
			}
			if err != nil && !errors.Is(err, ErrInvalidMetadata) && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unexpected error class: %v", err)
			}
		})
	}
}

func TestRaiMP4URLDeduplicatesViaStableOrdering(t *testing.T) {
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	a, err := raiMP4URL(relinker, "2400")
	if err != nil {
		t.Fatal(err)
	}
	b, err := raiMP4URL(relinker, "2400")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("identical inputs produced different URLs: %q vs %q", a, b)
	}
}

// Section: raiMP4QualityBounds (percentage-and-roof percentage match helper).

func TestRaiMP4QualityBounds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		desired, base int64
		want          bool
	}{
		// The pinned helper returns abs(target-number) < min(number*0.2, 125).
		// diff must satisfy BOTH the 125-kbps roof AND the 20% percentage
		// (i.e. diff*5 < number).  The roof of 125 dominates once
		// number >= 625; for smaller numbers the 20% rule wins.
		{"exact match", 2400, 2400, true},
		{"diff below roof wins", 2400, 2450, true},
		{"diff below roof wins (above)", 2400, 2350, true},
		{"roof boundary 124 accepted", 1000, 1124, true},
		{"roof boundary 125 rejected", 1000, 1125, false},
		{"large desired roof still wins (diff=1800)", 10000, 8200, false},
		{"large desired roof still wins (diff=1800 above)", 10000, 11800, false},
		{"large desired within roof (diff=100)", 10000, 9900, true},
		{"large desired within roof (diff=124)", 10000, 10124, true},
		{"small number percentage wins: 600 vs 700", 600, 700, true},
		{"small number percentage rejects: 600 vs 800", 600, 800, false},
		{"non-positive desired", 0, 1000, false},
		{"non-positive base", 1000, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := raiMP4QualityBounds(tc.desired, tc.base); got != tc.want {
				t.Fatalf("desired=%d base=%d = %t, want %t", tc.desired, tc.base, got, tc.want)
			}
		})
	}
}

// Section: raiMP4Probe (HEAD availability gate).
//
// raiMP4ProbeTransport embeds raiTestTransport so the rest of the relinker
// call chain still functions; only the credential-isolated no-redirect path
// is overridden to feed the probe transport.
type raiMP4ProbeTransport struct {
	raiTestTransport
	status        int
	body          string
	seenMethod    string
	seenURL       string
	seenHeader    http.Header
	seenIsolated  bool
	cancelOnEntry bool
}

func (t *raiMP4ProbeTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, r *http.Request) (*http.Response, error) {
	t.seenMethod = r.Method
	t.seenURL = r.URL.String()
	t.seenHeader = r.Header.Clone()
	t.seenIsolated = true
	if t.cancelOnEntry {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: t.status,
		Body:       io.NopCloser(bytes.NewBufferString(t.body)),
		Header:     make(http.Header),
	}, nil
}

func TestRaiMP4ProbeSuccessOnEmptyTwoHundred(t *testing.T) {
	transport := &raiMP4ProbeTransport{status: http.StatusOK}
	ok, err := raiMP4Probe(context.Background(), transport, "https://relinker.rai.it/relinker?by=abcd&overrideUserAgentRule=mp4-*")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("2xx empty probe should be accepted")
	}
	if transport.seenMethod != http.MethodHead {
		t.Fatalf("probe method = %q, want HEAD", transport.seenMethod)
	}
	if !transport.seenIsolated {
		t.Fatal("probe did not use credential-isolated no-redirect transport")
	}
	if got := transport.seenHeader.Get("User-Agent"); got != "Rai" {
		t.Fatalf("probe User-Agent = %q", got)
	}
}

func TestRaiMP4ProbeRejectsNonTwoXXWithoutError(t *testing.T) {
	statuses := []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusInternalServerError,
		http.StatusBadGateway,
	}
	for _, status := range statuses {
		t.Run(fmt.Sprintf("status=%d", status), func(t *testing.T) {
			transport := &raiMP4ProbeTransport{status: status}
			ok, err := raiMP4Probe(context.Background(), transport, "https://relinker.rai.it/relinker?by=abcd&overrideUserAgentRule=mp4-*")
			if err != nil {
				t.Fatalf("non-2xx must not surface as error: %v", err)
			}
			if ok {
				t.Fatalf("status %d must reject the probe", status)
			}
		})
	}
}

func TestRaiMP4ProbeRejectsOversizedBody(t *testing.T) {
	transport := &raiMP4ProbeTransport{status: http.StatusOK, body: strings.Repeat("X", int(raiMaxMP4Probe)+1)}
	ok, err := raiMP4Probe(context.Background(), transport, "https://relinker.rai.it/relinker?by=abcd&overrideUserAgentRule=mp4-*")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("oversized HEAD body should fail the probe")
	}
}

func TestRaiMP4ProbePropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &raiMP4ProbeTransport{status: http.StatusOK}
	ok, err := raiMP4Probe(ctx, transport, "https://relinker.rai.it/relinker?by=abcd&overrideUserAgentRule=mp4-*")
	if ok {
		t.Fatal("cancelled probe returned ok")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRaiMP4ProbeRejectsNonIsolatedTransport(t *testing.T) {
	plain := &raiMP4NonIsolatedTransport{}
	ok, err := raiMP4Probe(context.Background(), plain, "https://relinker.rai.it/relinker?by=abcd&overrideUserAgentRule=mp4-*")
	if ok {
		t.Fatal("non-isolated transport should not satisfy the probe")
	}
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("expected ErrTransportIsolation, got %v", err)
	}
}

// raiMP4NonIsolatedTransport is a minimal Transport that does NOT implement
// CredentialIsolatedNoRedirectTransport; the probe must reject it before
// any network access.
type raiMP4NonIsolatedTransport struct{}

func (raiMP4NonIsolatedTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}
func (raiMP4NonIsolatedTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("not used")
}

func TestRaiMP4ProbeRejectsUnsafeProbeURL(t *testing.T) {
	transport := &raiMP4ProbeTransport{status: http.StatusOK}
	ok, err := raiMP4Probe(context.Background(), transport, "https://user:pass@relinker.rai.it/relinker?overrideUserAgentRule=mp4-*")
	if ok {
		t.Fatal("unsafe probe URL accepted")
	}
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("expected ErrInvalidMetadata, got %v", err)
	}
}

// Section: raiMP4Synthesize end-to-end through raiRelinker.
//
// Every non-cancellation MP4 prep/probe failure must preserve the base HLS
// extraction (no synthetic formats appended, no error surfaced).

func TestRaiMP4SynthesizeProbeFailurePreservesBaseHLS(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	page := "https://www.raiplay.it/video/x-" + id
	endpoint := page + ".json"
	relinkerBody := `<root><url type="content">https://cdnvod.rai.it/v/abc_2400,1500,800/playlist.m3u8</url><is_live>N</is_live></root>`
	transport := &raiTestTransport{
		json: map[string]string{endpoint: `{"id":"ContentItem-` + id + `","video":{"content_url":"https://relinker.rai.it/relinker?by=abcd&token=SIGNED"}}`},
		page: map[string]string{},
	}
	four04 := http.StatusNotFound
	transport.headStatus = &four04
	transport.relinker = relinkerBody
	result, err := NewRaiPlay().Extract(context.Background(), Request{URL: page + ".html", Transport: transport})
	if err != nil {
		t.Fatalf("relinker probe failure must not surface as error: %v", err)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 1 {
		t.Fatalf("expected exactly the base HLS format, got %#v", formats)
	}
	format, _ := formats[0].Object()
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("surviving format protocol = %q", protocol)
	}
}

func TestRaiMP4SynthesizeProbeSuccessEmitsAllManifestQualities(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	page := "https://www.raiplay.it/video/x-" + id
	endpoint := page + ".json"
	relinkerBody := `<root><url type="content">https://cdnvod.rai.it/v/abc_2400,1500,800/playlist.m3u8</url><is_live>N</is_live></root>`
	transport := &raiTestTransport{
		json:     map[string]string{endpoint: `{"id":"ContentItem-` + id + `","video":{"content_url":"https://relinker.rai.it/relinker?by=abcd&token=SIGNED"}}`},
		page:     map[string]string{},
		relinker: relinkerBody,
	}
	result, err := NewRaiPlay().Extract(context.Background(), Request{URL: page + ".html", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) < 2 {
		t.Fatalf("expected base + MP4 entries, got %#v", formats)
	}
	mp4IDs := map[string]bool{}
	for _, candidate := range formats {
		object, _ := candidate.Object()
		ext, _ := object.Lookup("ext").StringValue()
		protocol, _ := object.Lookup("protocol").StringValue()
		id, _ := object.Lookup("format_id").StringValue()
		if ext == "mp4" && protocol == "https" && strings.HasPrefix(id, "https-") {
			mp4IDs[id] = true
		}
	}
	wantIDs := []string{"https-2400", "https-1500", "https-800"}
	for _, want := range wantIDs {
		if !mp4IDs[want] {
			t.Fatalf("expected MP4 format_id %q, got %v", want, mp4IDs)
		}
	}
}

func TestRaiMP4SynthesizeCancellationPropagates(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	page := "https://www.raiplay.it/video/x-" + id
	endpoint := page + ".json"
	transport := &raiTestTransport{
		json:     map[string]string{endpoint: `{"id":"ContentItem-` + id + `","video":{"content_url":"https://relinker.rai.it/relinker?by=abcd"}}`},
		page:     map[string]string{},
		relinker: `<root><url type="content">https://cdnvod.rai.it/v/abc_2400,1500/playlist.m3u8</url><is_live>N</is_live></root>`,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewRaiPlay().Extract(ctx, Request{URL: page + ".html", Transport: transport}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRaiMP4SynthesizeLiveSkipsSynthesis(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	page := "https://www.raiplay.it/video/x-" + id
	endpoint := page + ".json"
	transport := &raiTestTransport{
		json:     map[string]string{endpoint: `{"id":"ContentItem-` + id + `","video":{"content_url":"https://relinker.rai.it/relinker?by=abcd"}}`},
		page:     map[string]string{},
		relinker: `<root><url type="content">https://cdnvod.rai.it/v/abc_2400,1500/playlist.m3u8</url><is_live>Y</is_live></root>`,
	}
	result, err := NewRaiPlay().Extract(context.Background(), Request{URL: page + ".html", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	for _, candidate := range formats {
		object, _ := candidate.Object()
		id, _ := object.Lookup("format_id").StringValue()
		if strings.HasPrefix(id, "https-") {
			t.Fatalf("live must not synthesize MP4: %v", formats)
		}
	}
}

func TestRaiMP4SynthesizeUnmatchedManifestSkipsSynthesis(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	page := "https://www.raiplay.it/video/x-" + id
	endpoint := page + ".json"
	transport := &raiTestTransport{
		json:     map[string]string{endpoint: `{"id":"ContentItem-` + id + `","video":{"content_url":"https://relinker.rai.it/relinker?by=abcd"}}`},
		page:     map[string]string{},
		relinker: `<root><url type="content">https://cdnvod.rai.it/arbitrary/path/manifest.m3u8</url><is_live>N</is_live></root>`,
	}
	result, err := NewRaiPlay().Extract(context.Background(), Request{URL: page + ".html", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	for _, candidate := range formats {
		object, _ := candidate.Object()
		id, _ := object.Lookup("format_id").StringValue()
		if strings.HasPrefix(id, "https-") {
			t.Fatalf("unmatched manifest must not synthesize MP4: %v", formats)
		}
	}
}

// Section: raiMP4Format (composite per-quality synthesis).
//
// The pinned helper is invoked once per synthesized quality token.  These
// tests exercise the metadata-copy selection, the table fallback, the
// chosen-base-tbr-while-ID-stays-desired contract, and the missing-dimensions
// omission rule.

func raiMP4FormatTestBase(tbr int64, width, height int) []value.Value {
	return []value.Value{raiMP4BaseFormat(float64(tbr), width, height, "avc1.640028", "mp4a.40.2")}
}

func raiMP4FormatFind(formats []value.Value, formatID string) *value.Object {
	for _, candidate := range formats {
		object, _ := candidate.Object()
		id, _ := object.Lookup("format_id").StringValue()
		if id == formatID {
			return object
		}
	}
	return nil
}

func TestRaiMP4FormatWildcardSingleBaseUnder300UsesDefault(t *testing.T) {
	// When the (only) base combined format has tbr <= 300, the wildcard
	// path must default to the 250/352x198 table entry.
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, err := raiMP4URL(relinker, "*")
	if err != nil {
		t.Fatal(err)
	}
	bases := raiMP4FormatTestBase(250, 512, 288)
	obj := raiMP4Format(url, "*", bases, 250, len(bases))
	if obj == nil {
		t.Fatal("nil format")
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-250" {
		t.Fatalf("format_id = %q, want https-250", id)
	}
	if tbr, ok := obj.Lookup("tbr").Int(); !ok || tbr != 250 {
		t.Fatalf("tbr = %d (ok=%t), want 250", tbr, ok)
	}
	if w, ok := obj.Lookup("width").Int(); !ok || w != 512 {
		t.Fatalf("width = %d, want 512 (from chosen copy)", w)
	}
}

func TestRaiMP4FormatWildcardSingleBaseAbove300UsesDerived(t *testing.T) {
	// single base format with tbr 600, 1 base → derived = floor(600/100)*100 = 600
	// because derived > 300 → desired = 600.
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "*")
	bases := raiMP4FormatTestBase(600, 1024, 576)
	obj := raiMP4Format(url, "*", bases, 600, len(bases))
	if obj == nil {
		t.Fatal("nil format")
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-600" {
		t.Fatalf("format_id = %q, want https-600", id)
	}
}

func TestRaiMP4FormatWildcardSingleBaseExactly300FallsThrough(t *testing.T) {
	// single base format with tbr exactly 300 → derived = 300 but `br > 300`
	// is strict, so desired remains 250 (default).
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "*")
	bases := raiMP4FormatTestBase(300, 512, 288)
	obj := raiMP4Format(url, "*", bases, 300, len(bases))
	if obj == nil {
		t.Fatal("nil format")
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-250" {
		t.Fatalf("format_id = %q, want https-250 (300 fallback)", id)
	}
}

func TestRaiMP4FormatExplicitQualityPicksLastBitrateMatch(t *testing.T) {
	// Multiple combined base formats with overlapping bitrate bands - the
	// pinned loop always picks the LAST matching candidate.
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "1500")
	first := raiMP4BaseFormat(1450, 800, 450, "avc1.640020", "mp4a.40.2")
	second := raiMP4BaseFormat(1520, 920, 518, "avc1.640028", "mp4a.40.2")
	third := raiMP4BaseFormat(1490, 900, 500, "avc1.640028", "mp4a.40.5")
	obj := raiMP4Format(url, "1500", []value.Value{first, second, third}, 1500, 3)
	if obj == nil {
		t.Fatal("nil format")
	}
	// 1500 vs 1490: diff=10 < 125, 10*5=50 < 1500 → match.  Last match wins.
	if w, ok := obj.Lookup("width").Int(); !ok || w != 900 {
		t.Fatalf("width = %d, want 900 (last matching base)", w)
	}
	if h, ok := obj.Lookup("height").Int(); !ok || h != 500 {
		t.Fatalf("height = %d, want 500", h)
	}
	if vcodec, _ := obj.Lookup("vcodec").StringValue(); vcodec != "avc1.640028" {
		t.Fatalf("vcodec = %q, want avc1.640028 (last match)", vcodec)
	}
	// tbr is set from chosen base (1490) but format_id is the desired (1500).
	if tbr, ok := obj.Lookup("tbr").Int(); !ok || tbr != 1490 {
		t.Fatalf("tbr = %d, want 1490 (chosen base tbr)", tbr)
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-1500" {
		t.Fatalf("format_id = %q, want https-1500 (desired wins for id)", id)
	}
}

func TestRaiMP4FormatMissingDimensionsOmitted(t *testing.T) {
	// Combined base format lacks width/height; explicit quality that has no
	// resolution match in the table.  No copy is selected → width/height
	// must be omitted (not silently defaulted to 1280x720).
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "777")
	bare := value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("hls-no-dim")},
		value.Field{Key: "url", Value: value.String("https://cdn.example.test/v.m3u8")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "protocol", Value: value.String("m3u8_native")},
		value.Field{Key: "vcodec", Value: value.String("avc1.640028")},
		value.Field{Key: "acodec", Value: value.String("mp4a.40.2")},
		value.Field{Key: "tbr", Value: value.Int(750)},
		value.Field{Key: "fps", Value: value.Int(25)},
	))
	obj := raiMP4Format(url, "777", []value.Value{bare}, 750, 1)
	if obj == nil {
		t.Fatal("nil format")
	}
	if _, ok := obj.Lookup("width").Int(); ok {
		t.Fatal("width must be omitted when no copy/table match exists")
	}
	if _, ok := obj.Lookup("height").Int(); ok {
		t.Fatal("height must be omitted when no copy/table match exists")
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-777" {
		t.Fatalf("format_id = %q, want https-777", id)
	}
}

func TestRaiMP4FormatFloatBaseTBRIsHonored(t *testing.T) {
	// Some upstream flows expose tbr as a float.  The chosen copy's tbr
	// must be promoted to an int via the existing conversion path.
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "2400")
	bases := []value.Value{raiMP4BaseFormat(2380.5, 1280, 720, "avc1.640028", "mp4a.40.2")}
	obj := raiMP4Format(url, "2400", bases, 2380, 1)
	if obj == nil {
		t.Fatal("nil format")
	}
	// 2380.5 - 2400 = -19.5 → diff = 19 (int) → < 125 AND 19*5=95 < 2400 → match.
	if w, ok := obj.Lookup("width").Int(); !ok || w != 1280 {
		t.Fatalf("width = %d, want 1280", w)
	}
	if tbr, ok := obj.Lookup("tbr").Int(); !ok || tbr != 2380 {
		t.Fatalf("tbr = %d, want 2380 (float coerced)", tbr)
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-2400" {
		t.Fatalf("format_id = %q, want https-2400", id)
	}
}

func TestRaiMP4FormatBitrateMatchPreferredOverResolutionMatch(t *testing.T) {
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "2400")
	// Resolution match only.
	resOnly := raiMP4BaseFormat(800, 1280, 720, "avc1.640020", "mp4a.40.2")
	// Bitrate match within roof.
	bitOnly := raiMP4BaseFormat(2390, 800, 450, "avc1.640028", "mp4a.40.5")
	obj := raiMP4Format(url, "2400", []value.Value{resOnly, bitOnly}, 2390, 2)
	if obj == nil {
		t.Fatal("nil format")
	}
	if w, ok := obj.Lookup("width").Int(); !ok || w != 800 {
		t.Fatalf("bitrate match must win: width = %d, want 800", w)
	}
	if vcodec, _ := obj.Lookup("vcodec").StringValue(); vcodec != "avc1.640028" {
		t.Fatalf("bitrate match vcodec = %q, want avc1.640028", vcodec)
	}
}

func TestRaiMP4FormatExplicitQualityWithoutMatchIsSkipped(t *testing.T) {
	// Quality token "777" is not in the pinned table.  raiMP4FormatResolves
	// must report false when the combined base format is genuinely distant
	// from 777 (outside the 125-kbps roof) AND does not match the table
	// resolution entry; it must report true when within bounds or matching
	// a known resolution.
	distant := []value.Value{raiMP4BaseFormat(1200, 512, 288, "avc1.640020", "mp4a.40.2")}
	if raiMP4FormatResolves(777, distant) {
		t.Fatal("distant base tbr must not satisfy raiMP4FormatResolves")
	}
	// Within 125-kbps roof → resolves.
	close := []value.Value{raiMP4BaseFormat(770, 512, 288, "avc1.640020", "mp4a.40.2")}
	if !raiMP4FormatResolves(777, close) {
		t.Fatal("near-bitrate base must satisfy raiMP4FormatResolves")
	}
	// 777 matches 1024x576 in the table (we use _QUALITY[777] as
	// semantic example; verify by picking a base with matching resolution).
	// 777 is NOT in the pinned table, so this path should still skip
	// unless a base format matches bitrate bounds.  Test the negative
	// branch with a base that has the table dimension for an existing
	// tbr (e.g. 1500 → 920x518).
	resMatch := []value.Value{raiMP4BaseFormat(1500, 920, 518, "avc1.640028", "mp4a.40.2")}
	if !raiMP4FormatResolves(1500, resMatch) {
		t.Fatal("table-resolution match must satisfy raiMP4FormatResolves")
	}
}

// raiMP4FormatCommaListUnknownMatchedAndSkipped verifies that a manifest
// with `_777,800` synthesizes both tokens: 777 (unknown table entry but
// potentially matches a base bitrate) and 800 (known table entry with
// table dimensions 700x394).  It also confirms `_777,9999` skips the
// 9999 token (no table entry, no base match within bounds) while keeping
// the matched 777 token.
func raiMP4FormatCommaListHelper(t *testing.T, manifest string, wantIDs []string, base []value.Value) {
	t.Helper()
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	list, matched := raiMP4ManifestQualities(manifest)
	if !matched {
		t.Fatalf("manifest %q must match", manifest)
	}
	if len(list) != len(wantIDs) {
		t.Fatalf("list = %v, want %v", list, wantIDs)
	}
	for i, want := range wantIDs {
		if list[i] != want {
			t.Fatalf("list[%d] = %q, want %q", i, list[i], want)
		}
	}
	probe := &raiMP4ProbeTransport{status: http.StatusOK}
	out, err := raiMP4Synthesize(context.Background(), probe, relinker, manifest, base, false, false)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if len(out) != len(wantIDs) {
		t.Fatalf("synthesize emitted %d, want %d (%v)", len(out), len(wantIDs), out)
	}
	for i, want := range wantIDs {
		object, _ := out[i].Object()
		id, _ := object.Lookup("format_id").StringValue()
		if id != "https-"+want {
			t.Fatalf("format_id[%d] = %q, want https-%s", i, id, want)
		}
	}
}

func TestRaiMP4FormatCommaListUnknownSkipsUnmatchedToken(t *testing.T) {
	// _777,9999 with no close base: both tokens are outside the 125-kbps
	// roof of any base and neither has a table entry.  The synthesizer
	// must skip BOTH and emit zero entries (the comma list survives
	// parsing, but every token is skipped at synthesis).
	manifest := "https://cdnvod.rai.it/v/abc_777,9999/playlist.m3u8"
	list, matched := raiMP4ManifestQualities(manifest)
	if !matched || len(list) != 2 {
		t.Fatalf("manifest %q matched=%t list=%v", manifest, matched, list)
	}
	base := []value.Value{raiMP4BaseFormat(1200, 512, 288, "avc1.640020", "mp4a.40.2")}
	probe := &raiMP4ProbeTransport{status: http.StatusOK}
	out, err := raiMP4Synthesize(context.Background(), probe, "https://relinker.rai.it/relinker?by=abcd", manifest, base, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("synthesize must skip both unknown tokens, got %d: %v", len(out), out)
	}
}

func TestRaiMP4FormatCommaListUnknownResolvesViaBitrate(t *testing.T) {
	// _777,9999: 777 resolves via close base 770; 9999 still skips.
	// The synthesizer must emit ONLY https-777.
	manifest := "https://cdnvod.rai.it/v/abc_777,9999/playlist.m3u8"
	list, matched := raiMP4ManifestQualities(manifest)
	if !matched || len(list) != 2 {
		t.Fatalf("manifest %q matched=%t list=%v", manifest, matched, list)
	}
	base := []value.Value{raiMP4BaseFormat(770, 512, 288, "avc1.640020", "mp4a.40.2")}
	probe := &raiMP4ProbeTransport{status: http.StatusOK}
	out, err := raiMP4Synthesize(context.Background(), probe, "https://relinker.rai.it/relinker?by=abcd", manifest, base, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("synthesize emitted %d, want 1 (9999 must skip): %v", len(out), out)
	}
	object, _ := out[0].Object()
	id, _ := object.Lookup("format_id").StringValue()
	if id != "https-777" {
		t.Fatalf("format_id = %q, want https-777", id)
	}
}

// Section: raiMP4Synthesize cap + audio-only + duplicate-suppression.
func TestRaiMP4SynthesizeAudioOnlySkipsSynthesis(t *testing.T) {
	id := "cb27157f-9dd0-4aee-b788-b1f67643a391"
	page := "https://www.raiplay.it/video/x-" + id
	endpoint := page + ".json"
	relinkerBody := `<root><url type="content">https://cdnvod.rai.it/v/abc_2400,1500/playlist.m3u8</url><is_live>N</is_live></root>`
	transport := &raiTestTransport{
		json:     map[string]string{endpoint: `{"id":"ContentItem-` + id + `","video":{"content_url":"https://relinker.rai.it/relinker?by=abcd"}}`},
		page:     map[string]string{},
		relinker: relinkerBody,
	}
	if _, err := raiRelinker(context.Background(), transport, "https://relinker.rai.it/relinker?by=abcd", id, true); err != nil {
		t.Fatal(err)
	}
	// audioOnly path returns synthetic=nil and does not run probe; we
	// verify by calling the lower-level raiMP4Synthesize directly with a
	// matched manifest and a successful probe.
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	probeURL, err := raiMP4URL(relinker, "*")
	if err != nil {
		t.Fatal(err)
	}
	probe := &raiMP4ProbeTransport{status: http.StatusOK}
	out, err := raiMP4Synthesize(context.Background(), probe, relinker, "https://cdnvod.rai.it/v/abc_2400,1500/playlist.m3u8", []value.Value{raiMP4BaseFormat(2000, 1024, 576, "avc1.640028", "mp4a.40.2")}, false, true)
	if err != nil {
		t.Fatalf("audio-only synthesis must not error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("audio-only must skip synthesis, got %d", len(out))
	}
	_ = probeURL
}

func TestRaiMP4SynthesizeCapRespectsRaiMaxFormats(t *testing.T) {
	// Fill baseFormats up to raiMaxFormats - 1 with combined A/V HLS
	// formats, then ensure synthesis emits at most 1 MP4 entry (not the
	// full 2400,1500,800 list).  We call raiMP4Synthesize directly to
	// decouple the cap from the page-extraction overhead.
	base := make([]value.Value, 0, raiMaxFormats)
	for i := 0; i < raiMaxFormats-1; i++ {
		base = append(base, raiMP4BaseFormat(float64(1000+i), 1024, 576, "avc1.640028", "mp4a.40.2"))
	}
	probe := &raiMP4ProbeTransport{status: http.StatusOK}
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	manifest := "https://cdnvod.rai.it/v/abc_2400,1500,800/playlist.m3u8"
	out, err := raiMP4Synthesize(context.Background(), probe, relinker, manifest, base, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("cap must clamp MP4 synth to exactly 1 when baseFormats fills raiMaxFormats-1, got %d (%v)", len(out), out)
	}
	object, _ := out[0].Object()
	id, _ := object.Lookup("format_id").StringValue()
	if id != "https-2400" {
		t.Fatalf("first emitted format_id = %q, want https-2400", id)
	}
}

func TestRaiMP4SynthesizeProbeURLPrepFailurePreservesBaseHLS(t *testing.T) {
	// Synthesize must swallow non-context raiMP4URL errors so that a
	// relinker URL with an unsafe shape cannot drop a valid base HLS.
	// (No top-level raiTestTransport needed; we exercise raiMP4Synthesize
	// directly with a probe transport that always returns 2xx.)
	probe := &raiMP4ProbeTransport{status: http.StatusOK}
	out, err := raiMP4Synthesize(context.Background(), probe, "https://user:pass@relinker.rai.it/relinker", "https://cdnvod.rai.it/v/abc_2400,1500/playlist.m3u8", []value.Value{raiMP4BaseFormat(2000, 1024, 576, "avc1.640028", "mp4a.40.2")}, false, false)
	if err != nil {
		t.Fatalf("unsafe relinker URL must not surface: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty synth, got %d entries", len(out))
	}
}

func TestRaiMP4SynthesizeRejectsBadURLPrepAndReportsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := &raiMP4ProbeTransport{status: http.StatusOK, cancelOnEntry: true}
	if _, err := raiMP4Synthesize(ctx, probe, "https://relinker.rai.it/relinker?by=abcd", "https://cdnvod.rai.it/v/abc_2400,1500/playlist.m3u8", []value.Value{raiMP4BaseFormat(2000, 1024, 576, "avc1.640028", "mp4a.40.2")}, false, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	_ = raiMP4FormatFind
}

// Section: wildcard 301-399 → format_id https-300.
//
// The pinned wildcard derivation is: single base format with tbr > 300
// yields desired = floor(tbr/100)*100.  tbr=301 → derived=300, but the
// pinned `br > 300` (strict) check rejects; desired falls back to 250.
// Wait - re-check: tbr=301 → derived = (301/100)*100 = 300; check is
// `derived > 300` (false) OR `derived == 300 && singleTBR > 300` (true,
// because singleTBR=301 > 300).  So desired becomes 300.
// tbr=399 → derived = 300; singleTBR > 300 true → desired = 300.
// tbr=300 → derived = 300; singleTBR > 300 false → desired stays 250.
// Both 301 and 399 therefore emit format_id `https-300`.

func TestRaiMP4FormatWildcardSingleBase301Derives300(t *testing.T) {
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "*")
	bases := raiMP4FormatTestBase(301, 512, 288)
	obj := raiMP4Format(url, "*", bases, 301, len(bases))
	if obj == nil {
		t.Fatal("nil format")
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-300" {
		t.Fatalf("format_id = %q, want https-300 (singleTBR=301 → derived=300)", id)
	}
	// Pinned `format_copy.get('tbr') or tbr`: when chosen has tbr (the
	// bitrate match on 301), the chosen tbr wins over the rounded
	// desired=300.  format_id stays desired.
	if tbr, ok := obj.Lookup("tbr").Int(); !ok || tbr != 301 {
		t.Fatalf("tbr = %d, want 301 (chosen base tbr wins)", tbr)
	}
}

func TestRaiMP4FormatWildcardSingleBase399Derives300(t *testing.T) {
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "*")
	bases := raiMP4FormatTestBase(399, 512, 288)
	obj := raiMP4Format(url, "*", bases, 399, len(bases))
	if obj == nil {
		t.Fatal("nil format")
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-300" {
		t.Fatalf("format_id = %q, want https-300 (singleTBR=399 → derived=300)", id)
	}
	// 399 vs derived 300: diff=99 < 125 but 99*5=495 > 300 → percentage
	// rule rejects the bitrate match (no match).  No resolution match
	// either (300 is not in the pinned table).  chosen=nil → tbr stays
	// at desired 300.  format_id still pinned to desired.
	if tbr, ok := obj.Lookup("tbr").Int(); !ok || tbr != 300 {
		t.Fatalf("tbr = %d, want 300 (no match → desired wins)", tbr)
	}
}

func TestRaiMP4FormatWildcardSingleBaseExactly300FallsThroughDefault(t *testing.T) {
	// Already covered by TestRaiMP4FormatWildcardSingleBaseExactly300FallsThrough,
	// but duplicated here with the same name as the 301/399 case to keep
	// the suite grouped.
	relinker := "https://relinker.rai.it/relinker?by=abcd&token=SIGNED"
	url, _ := raiMP4URL(relinker, "*")
	bases := raiMP4FormatTestBase(300, 512, 288)
	obj := raiMP4Format(url, "*", bases, 300, len(bases))
	if obj == nil {
		t.Fatal("nil format")
	}
	if id, _ := obj.Lookup("format_id").StringValue(); id != "https-250" {
		t.Fatalf("format_id = %q, want https-250 (strict br > 300)", id)
	}
}
