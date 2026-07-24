package template

import (
	"errors"
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func fixtureInfo() value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture-1")},
		value.Field{Key: "title", Value: value.String("A: video?")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "none", Value: value.Null()},
		value.Field{Key: "empty_object", Value: value.ObjectValue(value.NewObject())},
		value.Field{Key: "uploader", Value: value.String("alice")},
		value.Field{Key: "view_count", Value: value.Int(42)},
		value.Field{Key: "rating", Value: value.Float(4.25)},
		value.Field{Key: "upload_date", Value: value.String("20260717")},
		value.Field{Key: "chapters", Value: value.List(
			value.ObjectValue(value.NewObject(value.Field{Key: "title", Value: value.String("first")})),
			value.ObjectValue(value.NewObject(value.Field{Key: "title", Value: value.String("last")})),
		)},
	))
}

func TestRenderSubset(t *testing.T) {
	got, err := Render("%% %(title)s.%(ext)s %(missing)s %(none)s", fixtureInfo())
	if err != nil {
		t.Fatal(err)
	}
	if got != "% A: video?.mp4 NA NA" {
		t.Fatalf("Render() = %q", got)
	}
}

func TestRenderTraversalAlternativesDefaultsAndReplacement(t *testing.T) {
	pattern := "%(missing,uploader|anonymous)s %(missing|anonymous)s %(uploader&by {}|unknown)s %(chapters.-1.title)s"
	got, err := Render(pattern, fixtureInfo())
	if err != nil {
		t.Fatal(err)
	}
	if got != "alice anonymous by alice last" {
		t.Fatalf("Render() = %q", got)
	}
	got, err = Render("%(missing&00{})j", fixtureInfo())
	if err != nil {
		t.Fatal(err)
	}
	if got != `"00NA"` {
		t.Fatalf("two-byte replacement = %q", got)
	}
}

func TestRenderNumericAndDateFormatting(t *testing.T) {
	got, err := Render("%(view_count)08d %(rating).1f %(upload_date>%Y-%m-%d %% done)s", fixtureInfo())
	if err != nil {
		t.Fatal(err)
	}
	if got != "00000042 4.2 2026-07-17 % done" {
		t.Fatalf("Render() = %q", got)
	}
}

func TestRenderJSON(t *testing.T) {
	got, err := Render("%(chapters)j", fixtureInfo())
	if err != nil {
		t.Fatal(err)
	}
	if got != `[{"title": "first"}, {"title": "last"}]` {
		t.Fatalf("Render JSON = %q", got)
	}
}

func TestRenderObjectProjectionAndDiagnosticJSON(t *testing.T) {
	got, err := Render("%(.{id,title,missing,none,empty_object,title.invalid})j", fixtureInfo())
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"id": "fixture-1", "title": "A: video?"}` {
		t.Fatalf("projection = %q", got)
	}
	got, err = Render("title = %(title)#j\nobject = %(.{id,chapters.0.title})#j", fixtureInfo())
	if err != nil {
		t.Fatal(err)
	}
	want := "title = \"A: video?\"\nobject = {\n" +
		"    \"id\": \"fixture-1\",\n" +
		"    \"chapters.0.title\": \"first\"\n}"
	if got != want {
		t.Fatalf("diagnostic JSON = %q, want %q", got, want)
	}
}

func TestRenderJSONMatchesPinnedEscaping(t *testing.T) {
	info := fixtureInfo()
	info.Set("unicode", value.String("á 𝐀 <tag>& literal \\u003c \"quote\""))
	got, err := Render("%(unicode)j", info)
	if err != nil {
		t.Fatal(err)
	}
	if got != `"\u00e1 \ud835\udc00 <tag>& literal \\u003c \"quote\""` {
		t.Fatalf("JSON escaping = %q", got)
	}
}

func TestRenderJSONUnicodeFlag(t *testing.T) {
	info := fixtureInfo()
	info.Set("unicode", value.String("á 𝐀 <tag>& literal \\u003c \"quote\""))
	info.Set("separators", value.String("a\u2028b\u2029c"))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{
			pattern: "%(.{unicode,id})j",
			want:    `{"unicode": "\u00e1 \ud835\udc00 <tag>& literal \\u003c \"quote\"", "id": "fixture-1"}`,
		},
		{
			pattern: "%(.{unicode,id})#j",
			want: "{\n" +
				`    "unicode": "\u00e1 \ud835\udc00 <tag>& literal \\u003c \"quote\"",` + "\n" +
				`    "id": "fixture-1"` + "\n}",
		},
		{
			pattern: "%(.{unicode,id})+j",
			want:    `{"unicode": "á 𝐀 <tag>& literal \\u003c \"quote\"", "id": "fixture-1"}`,
		},
		{
			pattern: "%(.{unicode,id})+#j",
			want: "{\n" +
				`    "unicode": "á 𝐀 <tag>& literal \\u003c \"quote\"",` + "\n" +
				`    "id": "fixture-1"` + "\n}",
		},
		{
			pattern: "%(.{unicode,id})#+j",
			want: "{\n" +
				`    "unicode": "á 𝐀 <tag>& literal \\u003c \"quote\"",` + "\n" +
				`    "id": "fixture-1"` + "\n}",
		},
		{
			pattern: "%(.{separators})j",
			want:    `{"separators": "a\u2028b\u2029c"}`,
		},
		{
			pattern: "%(.{separators})#j",
			want:    "{\n    \"separators\": \"a\\u2028b\\u2029c\"\n}",
		},
		{
			pattern: "%(.{separators})+j",
			want:    "{\"separators\": \"a\u2028b\u2029c\"}",
		},
		{
			pattern: "%(.{separators})+#j",
			want:    "{\n    \"separators\": \"a\u2028b\u2029c\"\n}",
		},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderRejectsUnsupportedJSONFormats(t *testing.T) {
	for _, pattern := range []string{
		"%(title)0+j",
		"%(title)10j",
		"%(title)+.2j",
		"%(title)-j",
		"%(title) j",
	} {
		if _, err := Render(pattern, fixtureInfo()); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
	}
}

func TestRenderListAndHTMLConversions(t *testing.T) {
	info := fixtureInfo()
	info.Set("formats", value.List(
		value.ObjectValue(value.NewObject(value.Field{Key: "id", Value: value.String("id 1")})),
		value.ObjectValue(value.NewObject(value.Field{Key: "id", Value: value.String("id 2")})),
		value.ObjectValue(value.NewObject(value.Field{Key: "id", Value: value.String("id 3")})),
	))
	info.Set("numbers", value.List(
		value.Int(1), value.Float(2.5), value.Float(1), value.Bool(true), value.Bool(false), value.Null()))
	info.Set("html", value.String(`&<>"' &amp;`))
	info.Set("boolean_true", value.Bool(true))
	info.Set("boolean_false", value.Bool(false))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{"%(formats.:.id)l", "id 1, id 2, id 3"},
		{"%(formats.:.id)#l", "id 1\nid 2\nid 3"},
		{"%(formats.:.id) 18l", "  id 1, id 2, id 3"},
		{"%(formats.:.id).4l", "id 1"},
		{"%(ext)l", "mp4"},
		{"%(ext)+05l", "  mp4"},
		{"%(numbers)l", "1, 2.5, 1.0, True, False, None"},
		{"%(none)h", "NA"},
		{"%(boolean_true)h", "True"},
		{"%(boolean_false)h", "False"},
		{"%(html)h", "&amp;&lt;&gt;&quot;&#39; &amp;amp;"},
		{"%(html).4h", "&amp"},
		{"%(missing|<&>)h", "&lt;&amp;&gt;"},
		{"%(title&<{}>)h", "&lt;A: video?&gt;"},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderUnicodeConversions(t *testing.T) {
	info := fixtureInfo()
	info.Set("unicode_conversion", value.String("áéí 𝐀"))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{"%(unicode_conversion)U", "áéí 𝐀"},
		{"%(unicode_conversion)#U", "a\u0301e\u0301i\u0301 𝐀"},
		{"%(unicode_conversion)+U", "áéí A"},
		{"%(unicode_conversion)+#U", "a\u0301e\u0301i\u0301 A"},
		{"%(unicode_conversion)#+U", "a\u0301e\u0301i\u0301 A"},
		{"%(unicode_conversion)+.4U", "áéí "},
		{"%(unicode_conversion)+8U", "   áéí A"},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderDecimalSuffixConversion(t *testing.T) {
	info := fixtureInfo()
	info.Set("zero", value.Int(0))
	info.Set("nine99", value.Int(999))
	info.Set("thousand", value.Int(1000))
	info.Set("ten23", value.Int(1023))
	info.Set("ten24", value.String("1024"))
	info.Set("height", value.Int(1080))
	info.Set("huge_decimal", value.Float(1e30))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{"%(zero)D", "0"},
		{"%(nine99)D", "999"},
		{"%(thousand)D", "1k"},
		{"%(ten23)D", "1k"},
		{"%(ten23)#D", "1023"},
		{"%(ten24)#D", "1Ki"},
		{"%(height)D", "1k"},
		{"%(height)5.2D", " 1.08k"},
		{"%(height)+6.2D", " +1.08k"},
		{"%(height)06.2D", "001.08k"},
		{"%(huge_decimal)D", "1000000Y"},
		{"%(missing,zero)D", "0"},
		{"%(missing|1000)D", "1k"},
		{"%(view_count&{}0)D", "420"},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderFirstRuneConversion(t *testing.T) {
	info := fixtureInfo()
	info.Set("title5", value.String("áéí 𝐀"))
	info.Set("true_value", value.Bool(true))
	info.Set("false_value", value.Bool(false))
	info.Set("zero_int", value.Int(0))
	info.Set("empty_string", value.String(""))
	info.Set("number", value.Float(7.5))
	info.Set("height", value.Int(1080))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{"%(title)c", "A"},
		{"%(title)3c", "  A"},
		{"%(title5)c", "á"},
		{"%(id)c", "f"},
		{"%(true_value)c", "T"},
		// Falsy values still get string-formatted; the default/replacement
		// are bypassed because the selected value is present.
		{"%(false_value)c", "False"},
		{"%(zero_int)c", "0"},
		{"%(empty_string)c", ""},
		{"%(number)c", "7"},
		// None is replaced upstream with the "NA" placeholder (a string),
		// so c takes the first rune of that placeholder.
		{"%(none)c", "N"},
		{"%(missing|u)c", "u"},
		// Pinned vectors from test/test_YoutubeDL.py: %(height)c == "1",
		// %(ext)c == "m".
		{"%(height)c", "1"},
		{"%(ext)c", "m"},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderFirstRuneRejectsInvalidUTF8(t *testing.T) {
	info := fixtureInfo()
	info.Set("invalid_utf8", value.String(string([]byte{0xff})))
	if _, err := Render("%(invalid_utf8)c", info); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("Render invalid UTF-8 error = %v", err)
	}
}

func TestRenderBytesConversion(t *testing.T) {
	info := fixtureInfo()
	info.Set("title5", value.String("áéí 𝐀"))
	info.Set("empty_string", value.String(""))
	// Bare invalid byte; decode-ignore drops it.
	info.Set("invalid_utf8", value.String(string([]byte{0xff})))
	// Bytes sequences chosen to exercise skip-and-continue decode-ignore.
	// bytes([97, 255, 98])            -> "ab"   (invalid byte between runes)
	// bytes([195, 169, 255, 195, 169]) -> "éé"   (invalid between complete runes)
	// bytes([255, 254, 97, 98])        -> "ab"   (leading invalid, then ASCII)
	// bytes([195, 169, 128, 195, 169]) -> "éé"   (stray continuation then valid)
	// bytes([195, 169, 195])           -> "é"    (incomplete trailing rune)
	// bytes([97, 195, 98])             -> "ab"   (bad c3 continuation)
	// bytes([195, 255, 169, 97])       -> "a"    (c3+ff skip, lone 0xa9 skip, ASCII a)
	info.Set("embed_invalid", value.String(string([]byte{97, 0xff, 98})))
	info.Set("between_runes", value.String(string([]byte{0xc3, 0xa9, 0xff, 0xc3, 0xa9})))
	info.Set("leading_invalid", value.String(string([]byte{0xff, 0xfe, 97, 98})))
	info.Set("stray_continuation", value.String(string([]byte{0xc3, 0xa9, 0x80, 0xc3, 0xa9})))
	info.Set("trailing_fragment", value.String(string([]byte{0xc3, 0xa9, 0xc3})))
	info.Set("bad_continuation", value.String(string([]byte{97, 0xc3, 98})))
	info.Set("c3_ff_a9_a", value.String(string([]byte{0xc3, 0xff, 0xa9, 97})))
	info.Set("padding_run", value.String("ééé"))
	info.Set("false_value", value.Bool(false))
	info.Set("zero_int", value.Int(0))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		// Pinned vector from test/test_YoutubeDL.py: %(title5).3B == "á"
		{"%(title5).3B", "á"},
		// ASCII passthrough.
		{"%(id)B", "fixture-1"},
		// Multibyte precision: byte counts, decode-ignore on cut runes.
		{"%(title5).1B", ""},
		{"%(title5).2B", "á"},
		{"%(title5).4B", "áé"},
		{"%(title5)B", "áéí 𝐀"},
		// Width right-pads with ASCII spaces (default).
		{"%(id)10B", " fixture-1"},
		// Width left-pads with the "-" flag.
		{"%(id)-10B", "fixture-1 "},
		// For byte %s the "0" flag is silently ignored: width 10 still
		// pads with a single ASCII space (matches b"%010s" % b"fixture-1").
		{"%(id)010B", " fixture-1"},
		// "0" combined with "-" is still just left-justified with spaces.
		{"%(id)0-10B", "fixture-1 "},
		// The "+", " ", and "#" flags are also ignored for %s; only "-"
		// changes the padding direction.
		{"%(id)+10B", " fixture-1"},
		{"%(id)#10B", " fixture-1"},
		{"%(id) 10B", " fixture-1"},
		// Mid-rune width truncation: "ééé" is c3 a9 c3 a9 c3 a9 (6 bytes);
		// width 7 → one leading space then all 6 bytes (7 total).
		{"%(padding_run)7B", " ééé"},
		// Mid-rune right padding: width 8 left-justify → 6 bytes + 2 spaces.
		{"%(padding_run)-8B", "ééé  "},
		// Mid-rune precision + width: precision 3 yields 3 bytes (c3 a9 c3
		// = "é" + start of next é), width 5 default-pads with 2 leading
		// spaces, then decode-ignore discards the incomplete c3.
		{"%(padding_run)5.3B", "  é"},
		// Precision 0 yields an empty byte buffer; width left-pads only.
		{"%(title5)5.0B", "     "},
		// Empty input passes through.
		{"%(empty_string)B", ""},
		// Falsy values still go through (string format of the value).
		{"%(false_value)B", "False"},
		{"%(zero_int)B", "0"},
		// Missing/None bypass to the default/NA string path.
		{"%(none)B", "NA"},
		{"%(missing|fallback)B", "fallback"},
		// The single invalid byte is dropped by decode-ignore.
		{"%(invalid_utf8)B", ""},
		{"%(invalid_utf8)0B", ""},
		// Skip-and-continue: invalid bytes are dropped one at a time
		// and decoding resumes (matches Python bytes.decode("utf-8",
		// "ignore")).
		{"%(embed_invalid)B", "ab"},
		{"%(embed_invalid)10B", "       ab"},
		{"%(between_runes)B", "éé"},
		{"%(leading_invalid)B", "ab"},
		{"%(stray_continuation)B", "éé"},
		{"%(trailing_fragment)B", "é"},
		{"%(bad_continuation)B", "ab"},
		{"%(c3_ff_a9_a)B", "a"},
		// Precision also skips invalid bytes; the width pad (here none)
		// is independent.
		{"%(embed_invalid).1B", "a"},
		{"%(embed_invalid).2B", "a"},
		{"%(embed_invalid).3B", "ab"},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderBytesConversionRejectsOversized(t *testing.T) {
	info := fixtureInfo()
	for _, pattern := range []string{
		"%(title)5000B",
		"%(title).5000B",
	} {
		if _, err := Render(pattern, info); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
	}
}

func TestRenderQuoteSanitizeAndReprConversions(t *testing.T) {
	info := fixtureInfo()
	info.Set("title4", value.String(`foo "bar" test`))
	info.Set("title5", value.String("áéí 𝐀"))
	info.Set("height", value.Int(1080))
	info.Set("quote", value.String("it's ready"))
	info.Set("formats", value.List(value.String("id 1"), value.String("id 2"), value.String("id 3")))
	info.Set("object", value.ObjectValue(value.NewObject(
		value.Field{Key: "id", Value: value.String("1234")},
		value.Field{Key: "ok", Value: value.Bool(true)},
	)))
	tests := []struct {
		pattern string
		want    string
	}{
		{"%(title4)#S", "foo_bar_test"},
		{"%(title4).10S", "foo ＂bar＂ "},
		{"%(id)r", "'fixture-1'"},
		{"%(height)r", "1080"},
		{"%(title5)a", `'\xe1\xe9\xed \U0001d400'`},
		{"%(formats)r", "['id 1', 'id 2', 'id 3']"},
		{"%(object)r", "{'id': '1234', 'ok': True}"},
		{"%(quote)r", `'it\'s ready'`},
	}
	if runtime.GOOS == "windows" {
		tests = append(tests,
			struct{ pattern, want string }{"%(title4)q", `"foo ""bar"" test"`},
			struct{ pattern, want string }{"%(formats)#q", `"id 1" "id 2" "id 3"`},
		)
	} else {
		tests = append(tests,
			struct{ pattern, want string }{"%(title4)q", `'foo "bar" test'`},
			struct{ pattern, want string }{"%(formats)#q", `'id 1' 'id 2' 'id 3'`},
			struct{ pattern, want string }{"%(quote)q", `'it'"'"'s ready'`},
		)
	}
	for _, test := range tests {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderRejectsInvalidCustomConversions(t *testing.T) {
	info := fixtureInfo()
	info.Set("nested_list", value.List(value.List(value.Int(1))))
	info.Set("object_list", value.List(value.ObjectValue(value.NewObject())))
	info.Set("bytes_list", value.List(value.Bytes([]byte("bytes"))))
	info.Set("invalid_utf8", value.String(string([]byte{0xff})))
	info.Set("negative", value.Int(-1))
	info.Set("infinite", value.Float(math.Inf(1)))
	info.Set("not_number", value.String("twelve"))
	info.Set("huge_decimal", value.Float(math.MaxFloat64))
	info.Set("oversized_unicode", value.String(strings.Repeat("x", maxScalarBytes+1)))
	info.Set("expanding_html", value.String(strings.Repeat("'", maxScalarBytes)))
	largeParts := make([]value.Value, 5)
	for index := range largeParts {
		largeParts[index] = value.String(strings.Repeat("x", maxScalarBytes))
	}
	info.Set("large_parts", value.List(largeParts...))
	tooMany := make([]value.Value, maxTraversalItems+1)
	info.Set("too_many", value.List(tooMany...))
	for _, pattern := range []string{
		"%(nested_list)l",
		"%(object_list)l",
		"%(bytes_list)l",
		"%(chapters)h",
		"%(invalid_utf8)U",
		"%(view_count)U",
		"%(negative)D",
		"%(infinite)D",
		"%(not_number)D",
		"%(huge_decimal)D",
		"%(oversized_unicode)U",
		"%(expanding_html)h",
		"%(large_parts)l",
		"%(too_many)l",
		"%(title)5000l",
		"%(title).5000h",
		"%(title)+.5000U",
		"%(view_count)5000D",
		"%(title)10.2.3l",
		"%(title)#.D",
	} {
		if _, err := Render(pattern, info); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
	}
}

func TestRenderWholeInfoJSON(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "first", Value: value.Int(1)},
		value.Field{Key: "omitted", Value: value.Missing()},
		value.Field{Key: "last", Value: value.String("á")},
	))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{"%()j", `{"first": 1, "last": "\u00e1"}`},
		{"%()+j", `{"first": 1, "last": "á"}`},
		{"%()#+j", "{\n    \"first\": 1,\n    \"last\": \"á\"\n}"},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
	if _, err := Render("%()s", info); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("Render empty scalar error = %v", err)
	}
}

func TestRenderAdvancedTraversal(t *testing.T) {
	info := fixtureInfo()
	info.Set("numbers", value.List(value.Int(0), value.Int(1), value.Int(2), value.Int(3), value.Int(4)))
	info.Set("unicode_path", value.String("aé𝐀z"))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{"%(numbers.1)j", "1"},
		{"%(numbers.-1)j", "4"},
		{"%(numbers.:)j", "[0, 1, 2, 3, 4]"},
		{"%(numbers.1:4:2)j", "[1, 3]"},
		{"%(numbers.::-1)j", "[4, 3, 2, 1, 0]"},
		{"%(unicode_path.1)s", "é"},
		{"%(unicode_path.1:3)s", "é𝐀"},
		{"%(unicode_path.::-1)s", "z𝐀éa"},
		{"%(chapters.:.title)j", `["first", "last"]`},
		{"%(chapters.:.{title})j", `[{"title": "first"}, {"title": "last"}]`},
		{"%(chapters.0.{title})j", `{"title": "first"}`},
		{"%(chapters.9.title|fallback)s", "fallback"},
		{"%(chapters.:.missing|fallback)s", "fallback"},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderExtremeSlicesAndHeterogeneousMapping(t *testing.T) {
	info := fixtureInfo()
	info.Set("numbers", value.List(value.Int(0), value.Int(1)))
	maximum := int(^uint(0) >> 1)
	minimum := -maximum - 1
	for _, test := range []struct {
		slice string
		want  string
	}{
		{"1::" + strconv.Itoa(maximum), "[1]"},
		{"1::" + strconv.Itoa(minimum), "[1]"},
		{strconv.Itoa(minimum) + ":" + strconv.Itoa(maximum) + ":" + strconv.Itoa(maximum), "[0]"},
		{strconv.Itoa(maximum) + ":" + strconv.Itoa(minimum) + ":" + strconv.Itoa(minimum), "[1]"},
	} {
		pattern := "%(numbers." + test.slice + ")j"
		got, err := Render(pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", pattern, got, test.want)
		}
	}

	info.Set("mixed", value.List(
		value.ObjectValue(value.NewObject(value.Field{Key: "title", Value: value.String("first")})),
		value.String("incompatible"),
		value.ObjectValue(value.NewObject()),
		value.Null(),
		value.ObjectValue(value.NewObject(value.Field{Key: "title", Value: value.String("last")})),
	))
	got, err := Render("%(mixed.:.title)j", info)
	if err != nil {
		t.Fatal(err)
	}
	if got != `["first", "last"]` {
		t.Fatalf("heterogeneous map = %q", got)
	}
	got, err = Render("%(mixed.:.missing|fallback)s", info)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Fatalf("missing heterogeneous map = %q", got)
	}
}

func TestRenderRejectsInvalidOrOversizedTraversal(t *testing.T) {
	info := fixtureInfo()
	for _, pattern := range []string{
		"%(chapters.::0)j",
		"%(chapters.1:x)j",
		"%(chapters.1:2:3:4)j",
		"%(chapters.0.{title)j",
		"%(chapters.0.{})j",
		"%(chapters.:.{}|fallback)s",
		"%(chapters.:.{title,,id}|fallback)s",
		"%(chapters.:.{title.::0}|fallback)s",
	} {
		if _, err := Render(pattern, info); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
	}
	items := make([]value.Value, maxTraversalItems+1)
	for index := range items {
		items[index] = value.Int(int64(index))
	}
	info.Set("many", value.List(items...))
	for _, pattern := range []string{"%(many.:)j", "%(many.:.missing)j"} {
		if _, err := Render(pattern, info); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) bound error = %v", pattern, err)
		}
	}
	inner := make([]value.Value, 65)
	for index := range inner {
		inner[index] = value.Int(int64(index))
	}
	outer := make([]value.Value, 65)
	for index := range outer {
		outer[index] = value.List(inner...)
	}
	info.Set("nested", value.List(outer...))
	if _, err := Render("%(nested.:.:)j", info); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("nested traversal budget error = %v", err)
	}
	oversized := make([]value.Value, maxTraversalItems+1)
	info.Set("oversized_nested", value.List(value.List(oversized...)))
	if _, err := Render("%(oversized_nested.:.:|fallback)s", info); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("nested slice limit error = %v", err)
	}
	info.Set("oversized_string", value.String(strings.Repeat("x", maxScalarBytes+1)))
	if _, err := Render("%(oversized_string.0:1+1|fallback)s", info); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("arithmetic traversal limit error = %v", err)
	}
}

func TestRenderBoundedArithmetic(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("1234")},
		value.Field{Key: "height", Value: value.String("1080")},
		value.Field{Key: "filesize", Value: value.Int(1024)},
		value.Field{Key: "left", Value: value.Int(5)},
		value.Field{Key: "right", Value: value.Float(2.5)},
		value.Field{Key: "numbers", Value: value.List(value.Int(10), value.Int(20))},
	))
	for _, test := range []struct {
		pattern string
		want    string
	}{
		{"%(id+1-height+3)05d", "00158"},
		{"%(filesize*8)d", "8192"},
		{"%(filesize-1)d", "1023"},
		{"%(-height.0)04d", "-001"},
		{"%(-9223372036854775808)d", "-9223372036854775808"},
		{"%(left+right).1f", "7.5"},
		{"%(left+right)d", "7"},
		// Arithmetic is deliberately left-to-right, so this is (10 + 2) * 3.
		{"%(10+2*3)d", "36"},
		{"%(missing+1,height+20)d", "1100"},
		{"%(height.missing+1,left+1)d", "6"},
		{"%(missing+1|fallback)s", "fallback"},
		{"%(numbers.-1+1)d", "21"},
		{"%(numbers.-1+numbers.0)d", "30"},
	} {
		got, err := Render(test.pattern, info)
		if err != nil {
			t.Fatalf("Render(%q) error = %v", test.pattern, err)
		}
		if got != test.want {
			t.Fatalf("Render(%q) = %q, want %q", test.pattern, got, test.want)
		}
	}
}

func TestRenderArithmeticFailures(t *testing.T) {
	info := fixtureInfo()
	info.Set("maximum", value.Int(math.MaxInt64))
	info.Set("huge_float", value.Float(1e308))
	info.Set("not_finite", value.String("Inf"))
	for _, pattern := range []string{
		"%(view_count+)d",
		"%(view_count++1)d",
		"%(view_count--1)d",
		"%(view_count**2)d",
		"%(view_count/2)d",
		"%(maximum+1)d",
		"%(9223372036854775808+1)d",
		"%(huge_float*huge_float)f",
		"%(huge_float*1)d",
		"%(not_finite+1)f",
	} {
		if _, err := Render(pattern, info); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
	}
	got, err := Render("%(title+1|fallback)s", info)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Fatalf("nonnumeric arithmetic fallback = %q", got)
	}
}

func TestRenderProjectionRejectsInvalidAndOversizedExpressions(t *testing.T) {
	fields := make([]string, maxProjectionFields+1)
	for index := range fields {
		fields[index] = "id"
	}
	for _, pattern := range []string{
		"%(.{})j",
		"%(.{id,,title})j",
		"%(.{" + strings.Join(fields, ",") + "})j",
	} {
		if _, err := Render(pattern, fixtureInfo()); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
	}
}

func TestRenderRejectsExpansionAndFormatAllocation(t *testing.T) {
	for _, pattern := range []string{"%(view_count)5000d", "%(rating).5000f"} {
		if _, err := Render(pattern, fixtureInfo()); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) = %v", pattern, err)
		}
	}
	info := fixtureInfo()
	info.Set("title", value.String(strings.Repeat("x", 128)))
	if _, err := Render("%(title&"+strings.Repeat("{}", 4096)+")s", info); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("replacement expansion = %v", err)
	}
	info.Set("huge", value.String(strings.Repeat("x", maxScalarBytes+1)))
	if _, err := Render("%(huge)j", info); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("JSON estimate = %v", err)
	}
	deep := value.String("leaf")
	for range maxJSONDepth + 1 {
		deep = value.List(deep)
	}
	info.Set("deep", deep)
	if _, err := Render("%(deep)#j", info); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("JSON depth = %v", err)
	}
}

func TestResolveSanitizesAndConfines(t *testing.T) {
	root := t.TempDir()
	got, err := Resolve(root, "videos/%(title)s.%(ext)s", fixtureInfo())
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "videos", "A_ video_.mp4")
	if got != want {
		t.Fatalf("Resolve() = %q, want %q", got, want)
	}
}

func TestResolveRejectsTraversalAndAbsolutePaths(t *testing.T) {
	for _, pattern := range []string{"../escape.%(ext)s", "/absolute.%(ext)s", `..\escape.%(ext)s`} {
		if _, err := Resolve(t.TempDir(), pattern, fixtureInfo()); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Resolve(%q) error = %v", pattern, err)
		}
	}
}

func TestRenderRejectsUnsupportedSyntax(t *testing.T) {
	for _, pattern := range []string{"%(title)", "%(title)d", "%title", "%(title.upper)s", "%(uploader&prefix)s", "%(upload_date>%Q)s"} {
		if _, err := Render(pattern, fixtureInfo()); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
	}
}

func TestRenderSyntaxErrorReportsSourceSpan(t *testing.T) {
	_, err := Render("%(title.upper)s", fixtureInfo())
	var syntaxError *SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("Render() error = %v", err)
	}
	if syntaxError.Start != 2 || syntaxError.End != 13 {
		t.Fatalf("syntax span = %d:%d, want 2:13", syntaxError.Start, syntaxError.End)
	}

	_, err = Render("prefix %title", fixtureInfo())
	if !errors.As(err, &syntaxError) || syntaxError.Start != 7 || syntaxError.End != 9 {
		t.Fatalf("percent syntax error = %#v, %v", syntaxError, err)
	}
}

func TestReservedFilename(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("CON")}))
	got, err := Resolve(t.TempDir(), "%(title)s.txt", info)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, string(filepath.Separator)+"_CON.txt") {
		t.Fatalf("reserved path = %q", got)
	}
}

func FuzzResolve(f *testing.F) {
	root := f.TempDir()
	f.Add("%(title)s.%(ext)s")
	f.Add("../%(title)s")
	f.Add("%%")
	f.Add("%(.{id,title})j")
	f.Add("%(.{id,title})#j")
	f.Add("%(.{id,title})+j")
	f.Add("%(.{id,title})+#j")
	f.Add("%(.{id,title})#+j")
	f.Add("%()j")
	f.Add("%(chapters.::-1)j")
	f.Add("%(chapters.:.title)j")
	f.Add("%(chapters.:.{title})j")
	f.Add("%(title.::-1)s")
	f.Add("%(missing&00{})j")
	f.Add("%(view_count+1)d")
	f.Add("%(view_count+1*2)d")
	f.Add("%(chapters.-1.title+1|fallback)s")
	f.Add("%(-rating).2f")
	f.Add("%(chapters.1::9223372036854775807)j")
	f.Add("%(chapters.1::-9223372036854775808)j")
	f.Add("%(chapters.:.missing|fallback)s")
	f.Add("%(chapters.:.title)l")
	f.Add("%(chapters.:.title)#l")
	f.Add("%(title)h")
	f.Add("%(title)+#U")
	f.Add("%(view_count)D")
	f.Add("%(view_count)5.2D")
	f.Fuzz(func(t *testing.T, pattern string) {
		resolved, err := Resolve(root, pattern, fixtureInfo())
		if err != nil {
			return
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("resolved path escaped root: %q", resolved)
		}
	})
}
