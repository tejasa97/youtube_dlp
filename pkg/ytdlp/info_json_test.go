package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/archive"
	cookiesnapshot "github.com/tejasa97/youtube_dlp/internal/cookies/snapshot"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/testserver"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestLoadInfoJSONDownloadsBoundedMetadataWithoutAmbientCredentials(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	input := filepath.Join(t.TempDir(), "fixture.info.json")
	data, err := json.Marshal(map[string]any{
		"_type": "video", "id": "loaded", "title": "Loaded fixture",
		"webpage_url": server.URL + "/page",
		"formats": []any{map[string]any{
			"format_id": "direct", "url": server.URL + "/media", "ext": "bin",
			"http_headers": map[string]string{"X-Fixture": "kept", "Cookie": "must-not-load"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true,
	})
	if err != nil || result.Extractor != "loaded-info-json" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "loaded.bin")); err != nil {
		t.Fatalf("loaded output: %v", err)
	}
	if strings.Contains(string(result.InfoJSON), "must-not-load") {
		t.Fatalf("credential-bearing header survived loaded metadata: %s", result.InfoJSON)
	}
	_, err = NewClient().Run(context.Background(), Request{LoadInfoJSON: input, CookieFile: input})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, errInvalidRequestOptions) {
		t.Fatalf("ambient credential error=%v", err)
	}
}

func TestLoadInfoJSONArchiveIdentityUsesExtractorMetadata(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, field := range []string{"extractor_key", "ie_key"} {
		t.Run(field, func(t *testing.T) {
			input := writeInfoFixture(t, `{"id":"loaded","title":"Loaded","`+field+`":"FixtureLoaded","url":"`+server.URL+`/media","ext":"bin"}`)
			archivePath := filepath.Join(t.TempDir(), "archive.txt")
			if err := os.WriteFile(archivePath, []byte("fixtureloaded loaded\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := NewClient().Run(context.Background(), Request{
				LoadInfoJSON: input, DownloadArchive: archivePath, SkipDownload: true,
			})
			if err != nil || !result.Archived || result.Skipped {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestLoadInfoJSONPreservesAcceptedAutonumberOnOutputFailure(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	input := writeInfoFixture(t, `{"id":"accepted","title":"Accepted","url":"`+server.URL+`/media","ext":"bin"}`)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "occupied.bin"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "occupied.bin",
	})
	if err == nil || result.AutonumberCount != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestLoadInfoJSONFallsBackFromDirectURLToWebpage(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	input := writeInfoFixture(t, `{"id":"loaded","title":"Loaded","url":"`+server.URL+`/missing","webpage_url":"`+server.URL+`/page","ext":"bin"}`)
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true,
	})
	if err != nil || !result.Downloaded || result.Extractor != "fixture" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(result.Filename); err != nil {
		t.Fatalf("webpage fallback output: %v", err)
	}
}

func TestLoadInfoJSONRejectsUnsafeShapesBoundsAndCancellation(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		target := writeInfoFixture(t, `{"id":"x","title":"X","url":"https://example.invalid/x"}`)
		link := filepath.Join(t.TempDir(), "fixture.info.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := loadInfoJSON(context.Background(), link)
		if !errors.Is(err, ErrInvalidInfoJSON) {
			t.Fatalf("symlink error=%v", err)
		}
	})
	t.Run("path swapped before open", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "fixture.info.json")
		replacement := filepath.Join(directory, "replacement.info.json")
		original := filepath.Join(directory, "original.info.json")
		for name, data := range map[string]string{
			path:        `{"id":"original","title":"Original","url":"https://example.invalid/original"}`,
			replacement: `{"id":"replacement","title":"Replacement","url":"https://example.invalid/replacement"}`,
		} {
			if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_, err := loadInfoJSONWithOpen(context.Background(), path, func(name string) (*os.File, error) {
			if err := os.Rename(name, original); err != nil {
				return nil, err
			}
			if err := os.Rename(replacement, name); err != nil {
				return nil, err
			}
			return os.Open(name)
		})
		if !errors.Is(err, ErrInvalidInfoJSON) {
			t.Fatalf("swapped path error=%v", err)
		}
	})
	t.Run("path swapped to symlink before open", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "fixture.info.json")
		target := filepath.Join(directory, "target.info.json")
		original := filepath.Join(directory, "original.info.json")
		for name, data := range map[string]string{
			path:   `{"id":"original","title":"Original","url":"https://example.invalid/original"}`,
			target: `{"id":"target","title":"Target","url":"https://example.invalid/target"}`,
		} {
			if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		_, err := loadInfoJSONWithOpen(context.Background(), path, func(name string) (*os.File, error) {
			if err := os.Rename(name, original); err != nil {
				return nil, err
			}
			if err := os.Symlink(target, name); err != nil {
				return nil, err
			}
			return cookiesnapshot.OpenReadOnlyNoFollow(name)
		})
		if !errors.Is(err, ErrInvalidInfoJSON) {
			t.Fatalf("swapped symlink error=%v", err)
		}
	})
	t.Run("wrong root", func(t *testing.T) {
		path := writeInfoFixture(t, `[]`)
		_, err := NewClient().Run(context.Background(), Request{LoadInfoJSON: path})
		if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, ErrInvalidInfoJSON) {
			t.Fatalf("wrong root error=%v", err)
		}
	})
	t.Run("path field", func(t *testing.T) {
		path := writeInfoFixture(t, `{"id":"x","title":"X","filepath":"/outside","url":"https://example.invalid/x"}`)
		_, err := NewClient().Run(context.Background(), Request{LoadInfoJSON: path})
		if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, ErrInvalidInfoJSON) {
			t.Fatalf("path field error=%v", err)
		}
	})
	t.Run("unsafe URL", func(t *testing.T) {
		path := writeInfoFixture(t, `{"id":"x","title":"X","url":"file:///outside"}`)
		_, err := NewClient().Run(context.Background(), Request{LoadInfoJSON: path})
		if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, ErrInvalidInfoJSON) {
			t.Fatalf("unsafe URL error=%v", err)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "large.info.json")
		if err := os.WriteFile(path, []byte(`{"id":"x","title":"`+strings.Repeat("x", maxLoadedInfoJSONBytes)+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewClient().Run(context.Background(), Request{LoadInfoJSON: path})
		if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, ErrInvalidInfoJSON) {
			t.Fatalf("oversize error=%v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		path := writeInfoFixture(t, `{"id":"x","title":"X","url":"https://example.invalid/x"}`)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewClient().Run(ctx, Request{LoadInfoJSON: path})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled error=%v", err)
		}
	})
}

func writeInfoFixture(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.info.json")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type autonumberLifecycleExtractor struct{}

func (autonumberLifecycleExtractor) Name() string { return "autonumber-lifecycle" }

func (autonumberLifecycleExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Host == "autonumber.invalid"
}

func (autonumberLifecycleExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	if parsed.Path == "/playlist" {
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "id", Value: value.String("lifecycle")},
			value.Field{Key: "title", Value: value.String("Autonumber lifecycle")},
		))
		return extractor.Playlist(info, extractor.StaticEntries(
			extractor.Entry{URL: "https://autonumber.invalid/accepted-one", ExtractorKey: "autonumber-lifecycle"},
			extractor.Entry{URL: "https://autonumber.invalid/rejected", ExtractorKey: "autonumber-lifecycle"},
			extractor.Entry{URL: "https://autonumber.invalid/archived", ExtractorKey: "autonumber-lifecycle"},
			extractor.Entry{URL: "https://autonumber.invalid/error", ExtractorKey: "autonumber-lifecycle"},
			extractor.Entry{URL: "https://autonumber.invalid/accepted-two", ExtractorKey: "autonumber-lifecycle"},
		))
	}
	if parsed.Path == "/error" {
		return extractor.Extraction{}, extractor.ErrUnavailable
	}
	id := strings.TrimPrefix(parsed.Path, "/")
	title := strings.ReplaceAll(id, "-", " ")
	if id == "rejected" {
		title = "Reject"
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "url", Value: value.String("https://example.invalid/" + id + ".mp4")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	return extractor.Media(info), nil
}

func TestAutonumberCountsOnlyAcceptedPlaylistEntries(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.txt")
	if err := os.WriteFile(archivePath, []byte("autonumber-lifecycle archived\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(context.Background(), archivePath, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(),
		request: Request{
			SkipDownload: true, MatchFilters: []string{`title != "Reject"`},
			AutonumberStart: 4, AutonumberSize: 3,
			Playlist: PlaylistOptions{ErrorPolicy: PlaylistErrorContinue},
			PrintRules: []PrintRule{
				{Stage: PrintPreProcess, Template: "%(autonumber)s|%(autonumber)03d"},
				{Stage: PrintAfterFilter, Template: "%(autonumber)s|%(autonumber)03d"},
			},
		},
		transport: transport, archive: store,
		registry: extractor.NewRegistry(autonumberLifecycleExtractor{}),
	}
	operation.compatibility, err = prepareCompatibility(operation.request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := operation.process(context.Background(), "https://autonumber.invalid/playlist", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.AutonumberCount != 2 || len(result.Entries) != 4 || result.SuppressedFailures != 1 {
		t.Fatalf("playlist autonumber result=%#v", result)
	}
	wantAutonumbers := []any{float64(4), nil, nil, float64(5)}
	for index, child := range result.Entries {
		var metadata map[string]any
		if err := json.Unmarshal(child.InfoJSON, &metadata); err != nil {
			t.Fatal(err)
		}
		if got := metadata["autonumber"]; got != wantAutonumbers[index] {
			t.Fatalf("entry %d metadata=%#v want autonumber=%v", index, metadata, wantAutonumbers[index])
		}
	}
	if !result.Entries[1].Skipped || !result.Entries[2].Archived {
		t.Fatalf("rejected/archive lifecycle=%#v", result.Entries)
	}
	if got := result.Entries[0].Prints; len(got) != 2 || got[0].Text != "003|003" || got[1].Text != "003|003" {
		t.Fatalf("first accepted provisional prints=%#v", got)
	}
	if got := result.Entries[1].Prints; len(got) != 1 || got[0].Text != "004|004" {
		t.Fatalf("rejected provisional prints=%#v", got)
	}
	if got := result.Entries[2].Prints; len(got) != 2 || got[0].Text != "004|004" || got[1].Text != "004|004" {
		t.Fatalf("archived provisional prints=%#v", got)
	}
}
