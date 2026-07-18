package template

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func fixtureInfo() value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("A: video?")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "none", Value: value.Null()},
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
	if got != `[{"title":"first"},{"title":"last"}]` {
		t.Fatalf("Render JSON = %q", got)
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
