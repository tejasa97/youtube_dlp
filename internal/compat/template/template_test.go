package template

import (
	"errors"
	"math"
	"path/filepath"
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
