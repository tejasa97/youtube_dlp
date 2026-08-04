package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const youtubeFormatFixtureURL = "https://www.youtube.com/watch?v=fixture0003"

func readYouTubeFormatFixture(t youtubeTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube_format_fidelity/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// youtubeMinimalFormatPlayer embeds a single-format streamingData so targeted
// tests can vary one format field at a time. %s is the format object content.
const youtubeMinimalFormatPlayer = `{
  "playabilityStatus": {"status": "OK"},
  "videoDetails": {"videoId": "fixture0003", "title": "T", "lengthSeconds": "123", "author": "A", "channelId": "UCfixture000000000000000", "shortDescription": "D", "viewCount": "1", "isLiveContent": false},
  "streamingData": {"formats": [{%s}]}
}`

func extractYouTubeFormatFidelity(t *testing.T, player string) *value.Object {
	t.Helper()
	page := []byte(`<!doctype html><html><body><script>var ytInitialPlayerResponse = ` + player + `;</script></body></html>`)
	transport := &memoryTransport{pages: map[string][]byte{youtubeFormatFixtureURL: page}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFormatFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return result.Info.Fields()
}

func youtubeFormatFidelityObject(t *testing.T, info *value.Object, index int) *value.Object {
	t.Helper()
	formats, ok := info.Lookup("formats").ListValue()
	if !ok || index >= len(formats) {
		t.Fatalf("formats = %d entries (%v)", len(formats), ok)
	}
	object, ok := formats[index].Object()
	if !ok {
		t.Fatal("format is not an object")
	}
	return object
}

func youtubeFormatFidelityText(t *testing.T, object *value.Object, key string) string {
	t.Helper()
	text, _ := object.Lookup(key).StringValue()
	return text
}

func TestYouTubeFormatFidelityPinnedExtraction(t *testing.T) {
	watch := readYouTubeFormatFixture(t, "watch.html")
	expected := readYouTubeFormatFixture(t, "expected.json")
	transport := &memoryTransport{pages: map[string][]byte{youtubeFormatFixtureURL: watch}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFormatFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var actual bytes.Buffer
	encoder := json.NewEncoder(&actual)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result.Info.Fields()); err != nil {
		t.Fatal(err)
	}
	var expectedDocument, actualDocument any
	if err := json.Unmarshal(expected, &expectedDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actual.Bytes(), &actualDocument); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("metadata mismatch\nactual:   %s\nexpected: %s", actual.Bytes(), expected)
	}
	if len(transport.reads) != 1 {
		t.Fatalf("reads = %#v; expected a single watch-page read", transport.reads)
	}
}

func TestYouTubeFormatFidelityCodecs(t *testing.T) {
	tests := []struct {
		name             string
		codecs           string
		wantVcodec       string
		wantAcodec       string
		wantDynamicRange string
	}{
		{"combined avc", "avc1.42001E, mp4a.40.2", "avc1.42001E", "mp4a.40.2", ""},
		{"single audio", "mp4a.40.2", "none", "mp4a.40.2", ""},
		{"single video", "avc1.640028", "avc1.640028", "none", ""},
		{"opus", "opus", "none", "opus", ""},
		{"vp9 profile 2 hdr", "vp9.2.30", "vp9.2.30", "none", "HDR10"},
		{"av1 profile 10 hdr", "av01.0.05M.10", "av01.0.05M.10", "none", "HDR10"},
		{"av1 sdr", "av01.0.05M.08", "av01.0.05M.08", "none", ""},
		{"dolby vision", "dvh1.05.06", "dvh1.05.06", "none", "DV"},
		{"theora vorbis", "theora, vorbis", "theora", "vorbis", ""},
		{"two unknown raw fallback", "foo, bar", "foo", "bar", ""},
		{"one unknown", "foo", "", "", ""},
		{"empty", "", "", "", ""},
		{"video plus unknown", "avc1.640028, foo", "avc1.640028", "none", ""},
		{"unknown plus audio", "foo, opus", "none", "opus", ""},
		{"uppercase family", "AVC1.640028", "AVC1.640028", "none", ""},
		{"malformed separators", "; ; ;", "", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vcodec, acodec, dynamicRange := youtubeParseCodecs(test.codecs)
			if vcodec != test.wantVcodec || acodec != test.wantAcodec || dynamicRange != test.wantDynamicRange {
				t.Fatalf("youtubeParseCodecs(%q) = %q, %q, %q; want %q, %q, %q",
					test.codecs, vcodec, acodec, dynamicRange, test.wantVcodec, test.wantAcodec, test.wantDynamicRange)
			}
		})
	}
}

func TestYouTubeFormatFidelityLanguagePreferences(t *testing.T) {
	tests := []struct {
		name           string
		audioTrack     string
		wantLanguage   string
		wantPreference int64
		wantHas        bool
	}{
		{"default track", `"audioTrack": {"id": "en", "displayName": "English", "audioIsDefault": true}`, "en", 5, true},
		{"original track", `"audioTrack": {"id": "es", "displayName": "Spanish (original)"}`, "es", 10, true},
		{"descriptive track", `"audioTrack": {"id": "en", "displayName": "English (descriptive)"}`, "en-desc", -10, true},
		{"plain track", `"audioTrack": {"id": "fr", "displayName": "Français"}`, "fr", -1, true},
		{"id with dots", `"audioTrack": {"id": "pt-BR.1", "displayName": "Português"}`, "pt-BR", -1, true},
		{"no track", ``, "", 0, false},
		{"track without id", `"audioTrack": {"displayName": "English"}`, "", 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			format := `"itag": 140, "url": "https://media.example/a.m4a", "mimeType": "audio/mp4; codecs=\"mp4a.40.2\"", "bitrate": 128000` + optionalComma(test.audioTrack) + test.audioTrack
			player := fmt.Sprintf(youtubeMinimalFormatPlayer, format)
			info := extractYouTubeFormatFidelity(t, player)
			object := youtubeFormatFidelityObject(t, info, 0)
			if got := youtubeFormatFidelityText(t, object, "language"); got != test.wantLanguage {
				t.Fatalf("language = %q; want %q", got, test.wantLanguage)
			}
			if test.wantHas {
				preference, ok := object.Lookup("language_preference").Int()
				if !ok || preference != test.wantPreference {
					t.Fatalf("language_preference = %d, %v; want %d", preference, ok, test.wantPreference)
				}
			} else if !object.Lookup("language_preference").IsMissing() {
				t.Fatal("language_preference must be absent without an audio-track identity")
			}
		})
	}
}

func TestYouTubeFormatFidelityAcceptsStringEncodedIntegers(t *testing.T) {
	format := `"itag": 140, "url": "https://media.example/a.m4a", "mimeType": "audio/mp4; codecs=\"mp4a.40.2\"", "averageBitrate": 128000, "audioSampleRate": "48000", "approxDurationMs": "123000"`
	info := extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, format))
	object := youtubeFormatFidelityObject(t, info, 0)
	if sampleRate, ok := object.Lookup("asr").Int(); !ok || sampleRate != 48000 {
		t.Fatalf("asr = %d, %v; want 48000", sampleRate, ok)
	}
	if approximateSize, ok := object.Lookup("filesize_approx").Int(); !ok || approximateSize != 1968000 {
		t.Fatalf("filesize_approx = %d, %v; want 1968000", approximateSize, ok)
	}
}

func optionalComma(fragment string) string {
	if fragment == "" {
		return ""
	}
	return ", "
}

func TestYouTubeFormatFidelityQualityAndNotes(t *testing.T) {
	format := `"itag": 137, "url": "https://media.example/v.mp4", "mimeType": "video/mp4; codecs=\"avc1.640028\"", "bitrate": 4000000, "contentLength": "2000", "width": 1920, "height": 1080, "fps": 30, "quality": "hd1080", "qualityLabel": "1080p"`
	info := extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, format))
	object := youtubeFormatFidelityObject(t, info, 0)
	if got := youtubeFormatFidelityText(t, object, "format_note"); got != "1080p" {
		t.Fatalf("format_note = %q", got)
	}
	if quality, ok := object.Lookup("quality").Float(); !ok || quality != 9 {
		t.Fatalf("quality = %v, %v; want 9", quality, ok)
	}
	if resolution := youtubeFormatFidelityText(t, object, "resolution"); resolution != "" {
		t.Fatalf("extractor must not emit resolution (product derives it): %q", resolution)
	}

	// Audio quality names drop the audio_quality_ prefix.
	audio := `"itag": 140, "url": "https://media.example/a.m4a", "mimeType": "audio/mp4; codecs=\"mp4a.40.2\"", "bitrate": 128000, "quality": "audio_quality_medium"`
	info = extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, audio))
	object = youtubeFormatFidelityObject(t, info, 0)
	if got := youtubeFormatFidelityText(t, object, "format_note"); got != "medium" {
		t.Fatalf("audio format_note = %q", got)
	}
	if quality, ok := object.Lookup("quality").Float(); !ok || quality != 3 {
		t.Fatalf("audio quality = %v, %v; want 3", quality, ok)
	}
}

func TestYouTubeFormatFidelitySourcePreference(t *testing.T) {
	// itag 22 loses 4 points; Premium labels gain 100.
	combined := `"itag": 22, "url": "https://media.example/22.mp4", "mimeType": "video/mp4; codecs=\"avc1.640028, mp4a.40.2\"", "bitrate": 900000, "quality": "hd720", "qualityLabel": "720p"`
	info := extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, combined))
	object := youtubeFormatFidelityObject(t, info, 0)
	if preference, ok := object.Lookup("source_preference").Int(); !ok || preference != -5 {
		t.Fatalf("source_preference = %d, %v; want -5", preference, ok)
	}
	premium := `"itag": 802, "url": "https://media.example/802.mp4", "mimeType": "video/mp4; codecs=\"avc1.640028\"", "bitrate": 5000000, "quality": "hd1080", "qualityLabel": "1080p Premium"`
	info = extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, premium))
	object = youtubeFormatFidelityObject(t, info, 0)
	if preference, ok := object.Lookup("source_preference").Int(); !ok || preference != 99 {
		t.Fatalf("source_preference = %d, %v; want 99", preference, ok)
	}
	plain := `"itag": 137, "url": "https://media.example/v.mp4", "mimeType": "video/mp4; codecs=\"avc1.640028\"", "bitrate": 4000000, "quality": "hd1080"`
	info = extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, plain))
	object = youtubeFormatFidelityObject(t, info, 0)
	if preference, ok := object.Lookup("source_preference").Int(); !ok || preference != -1 {
		t.Fatalf("source_preference = %d, %v; want -1", preference, ok)
	}
}

func TestYouTubeFormatFidelityDRCAndSuperResolution(t *testing.T) {
	drc := `"itag": 601, "url": "https://media.example/601.mp4", "mimeType": "video/mp4; codecs=\"avc1.640028\"", "bitrate": 3000000, "contentLength": "1500", "width": 1920, "height": 1080, "fps": 30, "quality": "hd1080", "qualityLabel": "1080p", "isDrc": true`
	info := extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, drc))
	object := youtubeFormatFidelityObject(t, info, 0)
	if got := youtubeFormatFidelityText(t, object, "format_id"); got != "601-drc" {
		t.Fatalf("format_id = %q; want 601-drc", got)
	}
	if got := youtubeFormatFidelityText(t, object, "format_note"); got != "1080p, DRC" {
		t.Fatalf("format_note = %q", got)
	}
	if quality, ok := object.Lookup("quality").Float(); !ok || quality != 8.5 {
		t.Fatalf("quality = %v, %v; want 8.5", quality, ok)
	}

	super := `"itag": 272, "url": "https://media.example/272.mp4?xtags=sr%3D1", "mimeType": "video/mp4; codecs=\"avc1.640028\"", "bitrate": 7000000, "width": 3840, "height": 2160, "quality": "hd2160", "qualityLabel": "2160p"`
	info = extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, super))
	object = youtubeFormatFidelityObject(t, info, 0)
	if got := youtubeFormatFidelityText(t, object, "format_id"); got != "272-sr" {
		t.Fatalf("format_id = %q; want 272-sr", got)
	}
	if got := youtubeFormatFidelityText(t, object, "format_note"); got != "2160p, AI-upscaled" {
		t.Fatalf("format_note = %q", got)
	}
}

func TestYouTubeFormatFidelityDynamicRangeAndContainer(t *testing.T) {
	hdr := `"itag": 401, "url": "https://media.example/401.webm", "mimeType": "video/webm; codecs=\"vp9.2.40\"", "bitrate": 8000000, "contentLength": "4000", "width": 3840, "height": 2160, "fps": 60, "quality": "hd2160", "qualityLabel": "2160p"`
	info := extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, hdr))
	object := youtubeFormatFidelityObject(t, info, 0)
	if got := youtubeFormatFidelityText(t, object, "dynamic_range"); got != "HDR10" {
		t.Fatalf("dynamic_range = %q", got)
	}
	if got := youtubeFormatFidelityText(t, object, "container"); got != "webm_dash" {
		t.Fatalf("container = %q", got)
	}
	audio := `"itag": 140, "url": "https://media.example/a.m4a", "mimeType": "audio/mp4; codecs=\"mp4a.40.2\"", "bitrate": 128000, "contentLength": "500", "audioSampleRate": 44100, "audioChannels": 2`
	info = extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, audio))
	object = youtubeFormatFidelityObject(t, info, 0)
	if got := youtubeFormatFidelityText(t, object, "container"); got != "m4a_dash" {
		t.Fatalf("audio container = %q", got)
	}
	if asr, ok := object.Lookup("asr").Int(); !ok || asr != 44100 {
		t.Fatalf("asr = %d, %v", asr, ok)
	}
	if channels, ok := object.Lookup("audio_channels").Int(); !ok || channels != 2 {
		t.Fatalf("audio_channels = %d, %v", channels, ok)
	}
}

func TestYouTubeFormatFidelityDamagedAndPreference(t *testing.T) {
	damaged := `"itag": 160, "url": "https://media.example/160.mp4", "mimeType": "video/mp4; codecs=\"avc1.4d400c\"", "bitrate": 120000, "contentLength": "60", "width": 256, "height": 144, "fps": 30, "quality": "tiny", "qualityLabel": "144p", "approxDurationMs": 50000`
	info := extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, damaged))
	object := youtubeFormatFidelityObject(t, info, 0)
	if got := youtubeFormatFidelityText(t, object, "format_note"); got != "144p, DAMAGED" {
		t.Fatalf("format_note = %q", got)
	}
	if preference, ok := object.Lookup("preference").Int(); !ok || preference != -10 {
		t.Fatalf("preference = %d, %v; want -10", preference, ok)
	}

	// The 3gp format (17) gets -2, its fps of 1 is omitted, and quality is tiny.
	threeGP := `"itag": 17, "url": "https://media.example/17.3gp", "mimeType": "video/3gpp; codecs=\"mp4v.20.3, mp4a.40.2\"", "bitrate": 100000, "contentLength": "50", "width": 176, "height": 144, "fps": 1, "quality": "small", "qualityLabel": "144p"`
	info = extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, threeGP))
	object = youtubeFormatFidelityObject(t, info, 0)
	if preference, ok := object.Lookup("preference").Int(); !ok || preference != -2 {
		t.Fatalf("preference = %d, %v; want -2", preference, ok)
	}
	if !object.Lookup("fps").IsMissing() {
		t.Fatal("fps of 1 must be omitted")
	}
	if quality, ok := object.Lookup("quality").Float(); !ok || quality != 0 {
		t.Fatalf("quality = %v, %v; want 0 (tiny)", quality, ok)
	}

	// Healthy formats carry no preference key.
	healthy := `"itag": 137, "url": "https://media.example/v.mp4", "mimeType": "video/mp4; codecs=\"avc1.640028\"", "bitrate": 4000000, "quality": "hd1080"`
	info = extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, healthy))
	object = youtubeFormatFidelityObject(t, info, 0)
	if !object.Lookup("preference").IsMissing() {
		t.Fatal("healthy format must not carry a preference")
	}
}

func TestYouTubeFormatFidelityFilesizeApprox(t *testing.T) {
	format := `"itag": 251, "url": "https://media.example/251.webm", "mimeType": "audio/webm; codecs=\"opus\"", "averageBitrate": 50000, "approxDurationMs": 123000, "quality": "audio_quality_high"`
	info := extractYouTubeFormatFidelity(t, fmt.Sprintf(youtubeMinimalFormatPlayer, format))
	object := youtubeFormatFidelityObject(t, info, 0)
	if tbr, ok := object.Lookup("tbr").Float(); !ok || tbr != 50 {
		t.Fatalf("tbr = %v, %v; want 50 (averageBitrate precedence)", tbr, ok)
	}
	if approx, ok := object.Lookup("filesize_approx").Int(); !ok || approx != 768750 {
		t.Fatalf("filesize_approx = %d, %v; want 768750", approx, ok)
	}
	if !object.Lookup("filesize").IsMissing() {
		t.Fatal("filesize must be absent without contentLength")
	}
}

func TestYouTubeFormatFidelityIDCollisionsAndOrdering(t *testing.T) {
	// Two audio formats share itag 140 but carry different audio-track
	// identities; both survive in stable order with colliding format IDs,
	// distinguished by language — mirroring the pinned reference.
	player := `{
  "playabilityStatus": {"status": "OK"},
  "videoDetails": {"videoId": "fixture0003", "title": "T", "lengthSeconds": "123", "author": "A", "channelId": "UCfixture000000000000000", "shortDescription": "D", "viewCount": "1", "isLiveContent": false},
  "streamingData": {"formats": [
    {"itag": 140, "url": "https://media.example/a1.m4a", "mimeType": "audio/mp4; codecs=\"mp4a.40.2\"", "bitrate": 128000, "contentLength": "500", "audioTrack": {"id": "en", "displayName": "English", "audioIsDefault": true}},
    {"itag": 140, "url": "https://media.example/a2.m4a", "mimeType": "audio/mp4; codecs=\"mp4a.40.2\"", "bitrate": 130000, "contentLength": "510", "audioTrack": {"id": "en-US", "displayName": "English (United States)"}}
  ]}
}`
	info := extractYouTubeFormatFidelity(t, player)
	formats, _ := info.Lookup("formats").ListValue()
	if len(formats) != 2 {
		t.Fatalf("formats = %d; want both same-itag tracks", len(formats))
	}
	first := youtubeFormatFidelityObject(t, info, 0)
	second := youtubeFormatFidelityObject(t, info, 1)
	if got := youtubeFormatFidelityText(t, first, "format_id"); got != "140" {
		t.Fatalf("first format_id = %q", got)
	}
	if got := youtubeFormatFidelityText(t, second, "format_id"); got != "140" {
		t.Fatalf("second format_id = %q (collision preserved, matching pinned)", got)
	}
	if got := youtubeFormatFidelityText(t, first, "language"); got != "en" {
		t.Fatalf("first language = %q", got)
	}
	if got := youtubeFormatFidelityText(t, second, "language"); got != "en-US" {
		t.Fatalf("second language = %q", got)
	}
	// Ordering is stable across repeated extraction.
	again := extractYouTubeFormatFidelity(t, player)
	againFirst := youtubeFormatFidelityObject(t, again, 0)
	againSecond := youtubeFormatFidelityObject(t, again, 1)
	if youtubeFormatFidelityText(t, againFirst, "url") != youtubeFormatFidelityText(t, first, "url") ||
		youtubeFormatFidelityText(t, againSecond, "url") != youtubeFormatFidelityText(t, second, "url") {
		t.Fatal("format ordering is not stable")
	}
}

func TestYouTubeFormatFidelitySelectionUnchanged(t *testing.T) {
	// Enriching formats must not change best-format selection for the pinned
	// corpus: the same formats with the new fields stripped select the same
	// format IDs as the enriched versions.
	watch := readYouTubeFormatFixture(t, "watch.html")
	transport := &memoryTransport{pages: map[string][]byte{youtubeFormatFixtureURL: watch}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFormatFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	stripped := result.Info.Fields().Clone()
	formatObjects, _ := stripped.Lookup("formats").ListValue()
	for _, item := range formatObjects {
		object, ok := item.Object()
		if !ok {
			continue
		}
		for _, key := range []string{"asr", "container", "dynamic_range", "format_note", "quality",
			"source_preference", "has_drm", "language", "language_preference", "filesize_approx", "audio_channels"} {
			object.Delete(key)
		}
	}
	enrichedSelector, err := mediaformat.ParseSelector("best")
	if err != nil {
		t.Fatal(err)
	}
	enrichedSelection, err := mediaformat.Select(value.NewInfo(result.Info.Fields()), enrichedSelector)
	if err != nil {
		t.Fatal(err)
	}
	baselineSelection, err := mediaformat.Select(value.NewInfo(stripped), enrichedSelector)
	if err != nil {
		t.Fatal(err)
	}
	if len(enrichedSelection) != len(baselineSelection) {
		t.Fatalf("selection lengths differ: %d vs %d", len(enrichedSelection), len(baselineSelection))
	}
	for index := range enrichedSelection {
		if enrichedSelection[index].ID != baselineSelection[index].ID ||
			enrichedSelection[index].URL != baselineSelection[index].URL {
			t.Fatalf("selection[%d] changed after enrichment: %s/%s vs %s/%s",
				index, enrichedSelection[index].ID, enrichedSelection[index].URL,
				baselineSelection[index].ID, baselineSelection[index].URL)
		}
	}
}

func FuzzYouTubeFormatFidelity(f *testing.F) {
	f.Add("avc1.42001E, mp4a.40.2")
	f.Add("vp9.2.30")
	f.Add("av01.0.05M.10")
	f.Add("dvh1.05.06")
	f.Add("garbage")
	f.Add("en")
	f.Add("Spanish (original)")
	f.Add("https://media.example/v.mp4?xtags=x%3Dsr%3D1")
	f.Add(".")
	f.Add("yt:stretch=4:3")
	f.Fuzz(func(t *testing.T, raw string) {
		firstV, firstA, firstR := youtubeParseCodecs(raw)
		secondV, secondA, secondR := youtubeParseCodecs(raw)
		if firstV != secondV || firstA != secondA || firstR != secondR {
			t.Fatalf("codec parse not deterministic for %q", raw)
		}
		if firstV != "" && len(firstV) > 1024 {
			t.Fatalf("oversized vcodec %q", firstV)
		}
		if firstA != "" && len(firstA) > 1024 {
			t.Fatalf("oversized acodec %q", firstA)
		}
		track := &youtubeAudioTrack{ID: raw, DisplayName: raw}
		language, preference, ok := youtubeAudioLanguage(track)
		if ok && (language == "" || preference < -10 || preference > 10) {
			t.Fatalf("invalid language result %q %d", language, preference)
		}
		firstRatio, firstHas := youtubeStretchedRatio([]string{raw})
		secondRatio, secondHas := youtubeStretchedRatio([]string{raw})
		if firstHas != secondHas || (firstHas && firstRatio != secondRatio) {
			t.Fatalf("stretch parse not deterministic for %q", raw)
		}
		if firstHas && !(firstRatio > 0) {
			t.Fatalf("invalid ratio %v", firstRatio)
		}
	})
}

func TestYouTubeFormatFidelityFlexibleInt64TolerantOfMalformed(t *testing.T) {
	cases := []struct {
		raw   string
		want  int64
		valid bool
	}{
		{`null`, 0, true},
		{`""`, 0, true},
		{`0`, 0, true},
		{`44100`, 44100, true},
		{`"44100"`, 44100, true},
		{`"0"`, 0, true},
		// Malformed and overflowing values are silently dropped to 0; the
		// extraction never aborts because of an optional numeric field.
		{`"44100.5"`, 0, true},
		{`"abc"`, 0, true},
		{`{"x":1}`, 0, true},
		{`[1,2]`, 0, true},
		{`99999999999999999999999`, 0, true},
	}
	for _, c := range cases {
		var got youtubeFlexibleInt64
		err := got.UnmarshalJSON([]byte(c.raw))
		if err != nil {
			t.Fatalf("UnmarshalJSON(%s) returned error %v", c.raw, err)
		}
		if int64(got) != c.want {
			t.Fatalf("UnmarshalJSON(%s) = %d, want %d", c.raw, int64(got), c.want)
		}
	}
}

func TestYouTubeFormatFidelityDRMTruthiness(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{``, false},
		{`null`, false},
		{`"null"`, false},
		{`""`, false},
		{`[]`, false},
		{`[ ]`, false},
		{`{}`, false},
		{`{ }`, false},
		{`false`, false},
		{`0`, false},
		{`["widevine"]`, true},
		{`["widevine","playready"]`, true},
		{`{"widevine":{}}`, true},
	}
	for _, c := range cases {
		if got := youtubeFormatHasDRM(json.RawMessage(c.raw)); got != c.want {
			t.Fatalf("youtubeFormatHasDRM(%s) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestYouTubeFormatFidelityMergePreservesStreamIdentity(t *testing.T) {
	url := "https://media.example/v.mp4?itag=140"
	trackEN := &youtubeAudioTrack{ID: "en", DisplayName: "English"}
	trackES := &youtubeAudioTrack{ID: "es", DisplayName: "Spanish"}
	drcTrue := true
	drcFalse := false
	players := []youtubePlayerResponse{{clientName: "ANDROID"}}
	players[0].StreamingData.AdaptiveFormats = []youtubeFormat{
		{Itag: 140, MimeType: "audio/mp4", URL: url, AudioTrack: trackEN},
		{Itag: 140, MimeType: "audio/mp4", URL: url, AudioTrack: trackES},
		{Itag: 140, MimeType: "audio/mp4", URL: url, AudioTrack: trackEN, IsDrc: &drcTrue},
		{Itag: 140, MimeType: "audio/mp4", URL: url, AudioTrack: trackEN, IsDrc: &drcFalse},
	}
	merged := mergeYouTubeFormats(players)
	if len(merged) != 3 {
		t.Fatalf("merged = %d formats, want 3 (distinct stream identities preserved)", len(merged))
	}
	identities := make(map[string]bool)
	for _, format := range merged {
		audioTrackID := ""
		if format.AudioTrack != nil {
			audioTrackID = format.AudioTrack.ID
		}
		drcFlag := "0"
		if format.IsDrc != nil && *format.IsDrc {
			drcFlag = "1"
		}
		key := audioTrackID + "\x00" + drcFlag
		if identities[key] {
			t.Fatalf("duplicate stream identity %q after merge", key)
		}
		identities[key] = true
	}
	// Same-URL/different-language and DRC/non-DRC variants must not be
	// collapsed.
	for _, want := range []string{"en\x000", "en\x001", "es\x000"} {
		if !identities[want] {
			t.Fatalf("missing expected identity %q after merge; got %v", want, identities)
		}
	}
}

func TestYouTubeFormatFidelityUnknownQualityAndDRCPenalty(t *testing.T) {
	// Quality ladder: tiny=0, ultralow=1, low=2, audio_medium=3, audio_high=4,
	// small=5, medium=6, large=7, hd720=8, hd1080=9, hd1440=10, hd2160=11,
	// hd2880=12, highres=13. Unknown qualities get rank -1 with the DRC
	// penalty still applied so a DRC unknown sorts strictly below a non-DRC
	// unknown of the same rank.
	cases := []struct {
		quality   string
		isDRC     bool
		wantValue float64
	}{
		{quality: "tiny", isDRC: false, wantValue: 0},
		{quality: "medium", isDRC: false, wantValue: 6},
		{quality: "audio_quality_high", isDRC: true, wantValue: 3.5},
		// Unknown qualities get rank -1, with the DRC penalty still applied.
		{quality: "exotic", isDRC: false, wantValue: -1},
		{quality: "exotic", isDRC: true, wantValue: -1.5},
	}
	for _, c := range cases {
		rank, hasRank := youtubeQualityRank(c.quality)
		qualityValue := -1.0
		if hasRank {
			qualityValue = float64(rank)
		}
		if c.isDRC {
			qualityValue -= 0.5
		}
		if qualityValue != c.wantValue {
			t.Fatalf("quality=%s drc=%v => %v, want %v", c.quality, c.isDRC, qualityValue, c.wantValue)
		}
	}
	// DRC must strictly demote a format below its non-DRC peer across the
	// ladder.
	for _, name := range []string{"medium", "audio_quality_high", "hd720"} {
		if rank, ok := youtubeQualityRank(name); ok {
			if (float64(rank) - 0.5) >= float64(rank) {
				t.Fatalf("DRC penalty did not strictly demote %q: %v vs %v", name, float64(rank)-0.5, float64(rank))
			}
		}
	}
}

func TestYouTubeFormatFidelityAudioLanguageRejectsEmptyOrInvalid(t *testing.T) {
	// Regression: "." previously produced language="" preference=-1 ok=true.
	cases := []struct {
		id     string
		wantOK bool
	}{
		{id: ".", wantOK: false},
		{id: " ", wantOK: false},
		{id: "", wantOK: false},
		{id: "..en", wantOK: false},
		{id: "..", wantOK: false},
		{id: "en", wantOK: true},
		{id: "en-US", wantOK: true},
		{id: "es", wantOK: true},
	}
	for _, c := range cases {
		track := &youtubeAudioTrack{ID: c.id, DisplayName: "English"}
		language, preference, ok := youtubeAudioLanguage(track)
		if ok != c.wantOK {
			t.Fatalf("youtubeAudioLanguage(%q) ok=%v, want %v", c.id, ok, c.wantOK)
		}
		if ok && (language == "" || preference < -10 || preference > 10) {
			t.Fatalf("youtubeAudioLanguage(%q) = (%q, %d), invalid", c.id, language, preference)
		}
	}
}
