package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
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

func TestLoadInfoJSONRejectsUnsafeShapesBoundsAndCancellation(t *testing.T) {
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

func TestAutonumberTracksPlaylistEntriesAndRejectedMedia(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(),
		request: Request{
			SkipDownload: true, MatchFilters: []string{"title=never"},
			AutonumberStart: 4, AutonumberSize: 3,
		},
		transport: transport,
		registry:  extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	operation.compatibility, err = prepareCompatibility(operation.request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.AutonumberCount != 2 || len(result.Entries) != 2 {
		t.Fatalf("playlist autonumber result=%#v", result)
	}
	leafs := []Result{result.Entries[0], result.Entries[1].Entries[0]}
	for index, child := range leafs {
		var metadata map[string]any
		if err := json.Unmarshal(child.InfoJSON, &metadata); err != nil {
			t.Fatal(err)
		}
		if got := metadata["autonumber"]; got != float64(index+4) || !child.Skipped {
			t.Fatalf("entry %d metadata=%#v skipped=%v", index, metadata, child.Skipped)
		}
	}
}
