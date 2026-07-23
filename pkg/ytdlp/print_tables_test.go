package ytdlp

import (
	"errors"
	"strings"
	"testing"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestRenderPrintTablePinnedCorpus(t *testing.T) {
	for _, test := range []struct {
		name      string
		headers   []string
		rows      [][]string
		hideEmpty bool
		delimiter string
		extraGap  int
		want      string
	}{
		{
			name: "basic", headers: []string{"a", "empty", "bcd"},
			rows: [][]string{{"123", "", "4"}, {"9999", "", "51"}},
			want: "a    empty bcd\n123        4\n9999       51",
		},
		{
			name: "hide empty", headers: []string{"a", "empty", "bcd"},
			rows: [][]string{{"123", "", "4"}, {"9999", "", "51"}}, hideEmpty: true,
			want: "a    bcd\n123  4\n9999 51",
		},
		{
			name: "right align", headers: []string{"\ta", "bcd"},
			rows: [][]string{{"1\t23", "4"}, {"\t9999", "51"}},
			want: "   a bcd\n1 23 4\n9999 51",
		},
		{
			name: "delimiter", headers: []string{"a", "bcd"},
			rows: [][]string{{"123", "4"}, {"9999", "51"}}, delimiter: "-",
			want: "a    bcd\n--------\n123  4\n9999 51",
		},
		{
			name: "wide gap", headers: []string{"a", "bcd"},
			rows: [][]string{{"123", "4"}, {"9999", "51"}}, delimiter: "-", extraGap: 2,
			want: "a      bcd\n----------\n123    4\n9999   51",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := renderPrintTable(test.headers, test.rows, test.hideEmpty, test.delimiter, test.extraGap)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("table = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRenderSyntheticPrintTables(t *testing.T) {
	info := syntheticPrintTableInfo()
	formats, err := renderFormatsTable(info)
	if err != nil {
		t.Fatal(err)
	}
	thumbnails, err := renderThumbnailsTable(info.Lookup("thumbnails"))
	if err != nil {
		t.Fatal(err)
	}
	subtitles, err := renderSubtitlesTable(info.Lookup("subtitles"))
	if err != nil {
		t.Fatal(err)
	}
	automatic, err := renderSubtitlesTable(info.Lookup("automatic_captions"))
	if err != nil {
		t.Fatal(err)
	}
	wantFormats := `ID    EXT RESOLUTION FPS HDR CH | FILESIZE   TBR PROTO | VCODEC                   VBR ACODEC     ABR ASR MORE INFO
-----------------------------------------------------------------------------------------------------------------------
audio m4a audio only            |  1.00MiB       https | audio only                   mp4a.40.2 128k 48k [en]
video mp4 1920x1080   30 10   2 | ≈2.00MiB 2500k dash  | avc1.640028.extra.tail 2300k mp4a.40.2 200k 44k Main, mp4_dash`
	wantThumbnails := `ID    Width Height  URL
small 320   unknown https://example.test/s.jpg
large 1920  1080    https://example.test/l.jpg`
	wantSubtitles := `Language Name    Formats
en       English srt, vtt
fr               vtt`
	wantAutomatic := `Language Name    Formats
es       Spanish vtt, json3`
	for name, gotWant := range map[string][2]string{
		"formats": {formats, wantFormats}, "thumbnails": {thumbnails, wantThumbnails},
		"subtitles": {subtitles, wantSubtitles}, "automatic": {automatic, wantAutomatic},
	} {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s table = %q, want %q", name, gotWant[0], gotWant[1])
		}
	}
}

func TestSyntheticPrintFieldsAreAvailableAtLifecycleStages(t *testing.T) {
	info := syntheticPrintTableInfo()
	operation := operation{request: Request{PrintRules: []PrintRule{
		{Stage: PrintVideo, Template: "%(formats_table)s"},
		{Stage: PrintVideo, Template: "%(thumbnails_table)s"},
		{Stage: PrintVideo, Template: "%(subtitles_table)s"},
		{Stage: PrintVideo, Template: "%(automatic_captions_table)s"},
	}}}
	prints, err := operation.capturePrints(t.Context(), PrintVideo, info, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(prints) != 4 {
		t.Fatalf("prints = %#v", prints)
	}
	for index, header := range []string{"ID ", "ID ", "Language ", "Language "} {
		if !strings.HasPrefix(prints[index].Text, header) || prints[index].Text == "NA" {
			t.Fatalf("print %d = %q", index, prints[index].Text)
		}
	}
}

func TestRenderFormatsTableDirectFallbackAndNonVideoOmission(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("direct")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "url", Value: value.String("https://example.test/video.mp4")},
	))
	rendered, err := renderFormatsTable(info)
	if err != nil || !strings.Contains(rendered, "direct mp4") {
		t.Fatalf("direct table = %q, %v", rendered, err)
	}
	info.Set("_type", value.String("playlist"))
	if rendered, err = renderFormatsTable(info); err != nil || rendered != "" {
		t.Fatalf("playlist table = %q, %v", rendered, err)
	}
}

func TestRenderPrintTablesLimitsAndMalformedRows(t *testing.T) {
	if _, err := renderPrintTable([]string{"one"}, [][]string{{"one", "two"}}, false, "", 0); err == nil {
		t.Fatal("malformed row accepted")
	}
	large := strings.Repeat("x", maxPrintTableBytes)
	if _, err := renderPrintTable([]string{"one"}, [][]string{{large}}, false, "", 0); !errors.Is(err, outputtemplate.ErrInvalidTemplate) {
		t.Fatalf("oversized table error = %v", err)
	}
	if rendered, err := renderPrintTable([]string{"empty"}, [][]string{{""}}, true, "-", 0); err != nil || rendered != "" {
		t.Fatalf("all-hidden table = %q, %v", rendered, err)
	}
	manyFormats := make([]value.Value, maxPrintTableRows+1)
	for index := range manyFormats {
		manyFormats[index] = value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("format")},
		))
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(manyFormats...)}))
	if _, err := renderFormatsTable(info); !errors.Is(err, outputtemplate.ErrInvalidTemplate) {
		t.Fatalf("oversized formats error = %v", err)
	}
	if got := formatTableBytes(256 * 1024); got != "256.00KiB" {
		t.Fatalf("byte label = %q", got)
	}
}

func FuzzRenderPrintTable(f *testing.F) {
	f.Add("ID", "value", false, "-")
	f.Add("\ta", "1\t23", true, "─")
	f.Add("Language", "English", false, "")
	f.Fuzz(func(t *testing.T, header, cell string, hideEmpty bool, delimiter string) {
		if delimiter != "" {
			delimiter = "-"
		}
		rendered, err := renderPrintTable(
			[]string{header, "fixed"},
			[][]string{{cell, ""}, {"second", "value"}},
			hideEmpty,
			delimiter,
			0,
		)
		if err != nil {
			return
		}
		if len(rendered) > maxPrintTableBytes || strings.Contains(rendered, "\x00") && !strings.Contains(header+cell, "\x00") {
			t.Fatalf("invalid bounded output: %q", rendered)
		}
	})
}

func syntheticPrintTableInfo() value.Info {
	format := func(fields ...value.Field) value.Value {
		return value.ObjectValue(value.NewObject(fields...))
	}
	subtitle := func(extension, name string) value.Value {
		fields := []value.Field{{Key: "ext", Value: value.String(extension)}}
		if name != "" {
			fields = append(fields, value.Field{Key: "name", Value: value.String(name)})
		}
		return format(fields...)
	}
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture-1")},
		value.Field{Key: "duration", Value: value.Int(60)},
		value.Field{Key: "formats", Value: value.List(
			format(
				value.Field{Key: "format_id", Value: value.String("audio")},
				value.Field{Key: "ext", Value: value.String("m4a")},
				value.Field{Key: "vcodec", Value: value.String("none")},
				value.Field{Key: "acodec", Value: value.String("mp4a.40.2")},
				value.Field{Key: "abr", Value: value.Int(128)},
				value.Field{Key: "asr", Value: value.Int(48000)},
				value.Field{Key: "filesize", Value: value.Int(1048576)},
				value.Field{Key: "protocol", Value: value.String("https")},
				value.Field{Key: "language", Value: value.String("en")},
			),
			format(
				value.Field{Key: "format_id", Value: value.String("video")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "width", Value: value.Int(1920)},
				value.Field{Key: "height", Value: value.Int(1080)},
				value.Field{Key: "fps", Value: value.Float(29.5)},
				value.Field{Key: "dynamic_range", Value: value.String("HDR10")},
				value.Field{Key: "audio_channels", Value: value.Int(2)},
				value.Field{Key: "filesize_approx", Value: value.Int(2097152)},
				value.Field{Key: "tbr", Value: value.Float(2500.5)},
				value.Field{Key: "protocol", Value: value.String("http_dash_segments")},
				value.Field{Key: "vcodec", Value: value.String("avc1.640028.extra.tail.more")},
				value.Field{Key: "vbr", Value: value.Int(2300)},
				value.Field{Key: "acodec", Value: value.String("mp4a.40.2")},
				value.Field{Key: "abr", Value: value.Int(200)},
				value.Field{Key: "asr", Value: value.Int(44100)},
				value.Field{Key: "format_note", Value: value.String("Main")},
				value.Field{Key: "container", Value: value.String("mp4_dash")},
			),
			format(
				value.Field{Key: "format_id", Value: value.String("hidden")},
				value.Field{Key: "preference", Value: value.Int(-1001)},
			),
		)},
		value.Field{Key: "thumbnails", Value: value.List(
			format(
				value.Field{Key: "id", Value: value.String("small")},
				value.Field{Key: "width", Value: value.Int(320)},
				value.Field{Key: "height", Value: value.Int(0)},
				value.Field{Key: "url", Value: value.String("https://example.test/s.jpg")},
			),
			format(
				value.Field{Key: "id", Value: value.String("large")},
				value.Field{Key: "width", Value: value.Int(1920)},
				value.Field{Key: "height", Value: value.Int(1080)},
				value.Field{Key: "url", Value: value.String("https://example.test/l.jpg")},
			),
		)},
		value.Field{Key: "subtitles", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "en", Value: value.List(subtitle("vtt", "English"), subtitle("srt", "English"))},
			value.Field{Key: "fr", Value: value.List(subtitle("vtt", ""))},
		))},
		value.Field{Key: "automatic_captions", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "es", Value: value.List(subtitle("json3", "Spanish"), subtitle("vtt", "Spanish"))},
		))},
	))
}
