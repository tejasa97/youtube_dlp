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
	for _, pattern := range []string{"%(title)", "%(title)d", "%title", "%(title.upper)s"} {
		if _, err := Render(pattern, fixtureInfo()); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("Render(%q) error = %v", pattern, err)
		}
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
