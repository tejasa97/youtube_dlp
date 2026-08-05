package ytdlp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/testserver"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func fixtureMediaInfo(extra ...value.Field) value.Info {
	fields := []value.Field{
		{Key: "id", Value: value.String("fixture-direct")},
		{Key: "title", Value: value.String("Deterministic Fixture")},
		{Key: "ext", Value: value.String("mp4")},
		{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("best")},
			value.Field{Key: "url", Value: value.String("https://fixture.invalid/media.mp4")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		)))},
	}
	return value.NewInfo(value.NewObject(append(fields, extra...)...))
}

func runMediaOperation(t *testing.T, request Request, info value.Info) Result {
	t.Helper()
	transport, _ := network.New(network.Config{})
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{client: NewClient(), request: request, transport: transport, compatibility: compatibility}
	result, err := operation.processMedia(context.Background(), extractor.Media(info), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOperationSimpleFilterRejections(t *testing.T) {
	info := fixtureMediaInfo(
		value.Field{Key: "upload_date", Value: value.String("20240101")},
		value.Field{Key: "view_count", Value: value.Int(50)},
		value.Field{Key: "age_limit", Value: value.Int(19)},
	)
	for _, test := range []struct {
		name    string
		filters SimpleFilterOptions
		want    string
	}{
		{
			name:    "date after",
			filters: SimpleFilterOptions{DateAfter: "20240201"},
			want:    "2024-01-01 upload date is not in range 2024-02-01 to 9999-12-31",
		},
		{
			name:    "date before",
			filters: SimpleFilterOptions{DateBefore: "20231231"},
			want:    "2024-01-01 upload date is not in range 0001-01-01 to 2023-12-31",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runMediaOperation(t, Request{OutputDir: t.TempDir(), Simulate: true, SimpleFilters: test.filters}, info)
			if !result.Skipped || result.SkipReason != test.want {
				t.Fatalf("result = %#v want reason %q", result, test.want)
			}
		})
	}
	minViews := int64(100)
	result := runMediaOperation(t, Request{OutputDir: t.TempDir(), Simulate: true, SimpleFilters: SimpleFilterOptions{MinViews: &minViews}}, info)
	if !result.Skipped || result.SkipReason != "Skipping Deterministic Fixture, because it has not reached minimum view count (50/100)" {
		t.Fatalf("min views result = %#v", result)
	}
	ageLimit := int64(18)
	result = runMediaOperation(t, Request{OutputDir: t.TempDir(), Simulate: true, SimpleFilters: SimpleFilterOptions{AgeLimit: &ageLimit}}, info)
	if !result.Skipped || result.SkipReason != `Skipping "Deterministic Fixture" because it is age restricted` {
		t.Fatalf("age limit result = %#v", result)
	}
}

func TestOperationAbsentFilterFieldsNeverReject(t *testing.T) {
	minViews := int64(100)
	ageLimit := int64(18)
	result := runMediaOperation(t, Request{
		OutputDir: t.TempDir(), Simulate: true,
		SimpleFilters: SimpleFilterOptions{DateAfter: "20240101", MinViews: &minViews, AgeLimit: &ageLimit},
	}, fixtureMediaInfo())
	if result.Skipped {
		t.Fatalf("absent fields must not reject: %#v", result)
	}
}

func TestOperationSimpleFiltersPrecedeGenericFilters(t *testing.T) {
	// The simple title filter rejects even though the generic filter would
	// accept, proving simple filters run first (check_filter order).
	result := runMediaOperation(t, Request{
		OutputDir: t.TempDir(), Simulate: true,
		SimpleFilters: SimpleFilterOptions{MatchTitle: "^Native"},
		MatchFilters:  []string{"title=Deterministic Fixture"},
	}, fixtureMediaInfo())
	if !result.Skipped || result.SkipReason != `"Deterministic Fixture" title did not match pattern "^Native"` {
		t.Fatalf("simple filter must precede generic filters: %#v", result)
	}
	// The generic filter still rejects after the simple filter passes.
	result = runMediaOperation(t, Request{
		OutputDir: t.TempDir(), Simulate: true,
		SimpleFilters: SimpleFilterOptions{MatchTitle: "^Deterministic"},
		MatchFilters:  []string{"title=other"},
	}, fixtureMediaInfo())
	if !result.Skipped || !strings.Contains(result.SkipReason, "does not pass filter") {
		t.Fatalf("generic filter must run after simple filters pass: %#v", result)
	}
}

func TestFiltersSeePreprocessMetadataBeforeTransformedOutput(t *testing.T) {
	info := fixtureMediaInfo()
	info.Set("title", value.String("Original"))
	result := runMediaOperation(t, Request{
		OutputDir: t.TempDir(), Simulate: true,
		SimpleFilters:   SimpleFilterOptions{MatchTitle: "^Original"},
		MatchFilters:    []string{"title=Original"},
		ReplaceMetadata: []string{"title:Original:Transformed"},
	}, info)
	if result.Skipped {
		t.Fatalf("pre-transform filters rejected the original metadata: %#v", result)
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.InfoJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["title"] != "Transformed" {
		t.Fatalf("downstream metadata = %#v, want transformed title", decoded["title"])
	}
}

func TestClientBreakOnRejectStops(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, test := range []struct {
		name    string
		request Request
	}{
		{
			name:    "generic filter",
			request: Request{URL: server.URL + "/page", OutputDir: t.TempDir(), SkipDownload: true, MatchFilters: []string{"title=other"}, BreakOnReject: true},
		},
		{
			name:    "simple title filter",
			request: Request{URL: server.URL + "/page", OutputDir: t.TempDir(), SkipDownload: true, SimpleFilters: SimpleFilterOptions{MatchTitle: "^Native"}, BreakOnReject: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewClient().Run(context.Background(), test.request)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Stopped || result.StopKind != StopBreakOnReject || !result.Skipped {
				t.Fatalf("result = %#v want StopBreakOnReject", result)
			}
		})
	}
}

func TestClientBreakMatchFilterAlwaysStops(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: t.TempDir(), SkipDownload: true,
		BreakMatchFilters: []string{"title=other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Stopped || result.StopKind != StopBreakMatchFilter {
		t.Fatalf("result = %#v want StopBreakMatchFilter", result)
	}
}

func TestClientArchivePrecedesFilters(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.txt")
	// The first run actually downloads so the archive record is written.
	base := Request{URL: server.URL + "/page", OutputDir: root, DownloadArchive: archivePath}
	if _, err := NewClient().Run(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	// The second run's filter would reject, but the archive match takes
	// precedence: the result reports Archived, never Skipped.
	second, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, DownloadArchive: archivePath, SkipDownload: true,
		MatchFilters: []string{"title=other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Archived || second.Skipped || second.Stopped {
		t.Fatalf("archive precedence result = %#v", second)
	}
	// --break-on-existing additionally stops the run.
	third, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, DownloadArchive: archivePath, SkipDownload: true,
		MatchFilters:    []string{"title=other"},
		BreakOnExisting: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !third.Archived || !third.Stopped || third.StopKind != StopBreakOnExisting || third.StopReason != "fixture-direct: Deterministic Fixture has already been recorded in the archive" {
		t.Fatalf("break-on-existing result = %#v", third)
	}
}

func TestClientMaxDownloadsCountsPassingEntries(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	// One video completes; with a cap of 1 the run stops after it finishes.
	result, err := NewClient().Run(context.Background(), Request{URL: server.URL + "/page", OutputDir: root, SkipDownload: true, MaxDownloads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloads != 1 || !result.Stopped || result.StopKind != StopMaxDownloads {
		t.Fatalf("max-downloads 1 result = %#v", result)
	}
	// A cap of 2 leaves a single video alone.
	result, err = NewClient().Run(context.Background(), Request{URL: server.URL + "/page", OutputDir: root, SkipDownload: true, MaxDownloads: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloads != 1 || result.Stopped {
		t.Fatalf("max-downloads 2 result = %#v", result)
	}
	// Simulated downloads count toward the cap.
	result, err = NewClient().Run(context.Background(), Request{URL: server.URL + "/page", OutputDir: root, Simulate: true, MaxDownloads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloads != 1 || !result.Stopped || result.StopKind != StopMaxDownloads {
		t.Fatalf("simulate max-downloads result = %#v", result)
	}
}

func TestClientMaxDownloadsDoesNotCountArchiveOrRejectedEntries(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.txt")
	if _, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, DownloadArchive: archivePath,
	}); err != nil {
		t.Fatal(err)
	}
	archived, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true, DownloadArchive: archivePath, MaxDownloads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Downloads != 0 || archived.Stopped {
		t.Fatalf("archived entry consumed max-downloads: %#v", archived)
	}
	rejected, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true, MaxDownloads: 1,
		MatchFilters: []string{"title=other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Downloads != 0 || rejected.Stopped {
		t.Fatalf("rejected entry consumed max-downloads: %#v", rejected)
	}
}

func TestClientReturnsSelectedAttemptAccountingWithCategorizedFailure(t *testing.T) {
	mediaURL := ""
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			info := value.NewObject(
				value.Field{Key: "id", Value: value.String("counted-failure")},
				value.Field{Key: "title", Value: value.String("Counted failure")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
					value.Field{Key: "format_id", Value: value.String("direct")},
					value.Field{Key: "url", Value: value.String(mediaURL)},
					value.Field{Key: "ext", Value: value.String("mp4")},
				)))},
			)
			_ = json.NewEncoder(writer).Encode(value.ObjectValue(info))
		case "/media":
			http.Error(writer, "fixture failure", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	mediaURL = server.URL + "/media"
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: t.TempDir(), MaxDownloads: 1,
		Downloader: DownloaderOptions{Attempts: 1},
	})
	if err == nil {
		t.Fatal("media failure unexpectedly succeeded")
	}
	if !IsCategory(err, ErrorNetwork) {
		t.Fatalf("media failure category = %v, want network", err)
	}
	if result.Downloads != 1 || !result.Stopped || result.StopKind != StopMaxDownloads {
		t.Fatalf("selected media failure result = %#v", result)
	}
}

func TestClientForceWriteArchiveInSimulationAndSkipDownload(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, test := range []struct {
		name     string
		simulate bool
		skip     bool
	}{
		{name: "simulate", simulate: true},
		{name: "skip-download", skip: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			archivePath := filepath.Join(root, "archive.txt")
			result, err := NewClient().Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: root, DownloadArchive: archivePath,
				Simulate: test.simulate, SkipDownload: test.skip, ForceWriteArchive: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(archivePath)
			if err != nil || !strings.Contains(string(data), "fixture fixture-direct") {
				t.Fatalf("archive=%q err=%v result=%#v", data, err, result)
			}
			archived, err := NewClient().Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: root, SkipDownload: true, DownloadArchive: archivePath,
			})
			if err != nil || !archived.Archived {
				t.Fatalf("forced archive was not observable on next run: result=%#v err=%v", archived, err)
			}
		})
	}
}

func TestOperationMaxDownloadsStopsPlaylist(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{OutputDir: t.TempDir(), MaxDownloads: 1}, transport: transport,
		registry: extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || !result.Stopped || result.StopKind != StopMaxDownloads || result.Downloads != 1 {
		t.Fatalf("playlist max-downloads result = %#v", result)
	}
}

func TestClientFilesizeAbortSkipsEntry(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		Downloader: DownloaderOptions{MinFilesize: 1 << 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || !strings.Contains(result.SkipReason, "File is smaller than min-filesize") || result.Downloaded {
		t.Fatalf("filesize abort result = %#v", result)
	}
	if _, statErr := os.Stat(filepath.Join(root, "Deterministic Fixture.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("filesize abort wrote media: %v", statErr)
	}
}

func TestClientFilesizeAbortNotRecordedInArchive(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.txt")
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, DownloadArchive: archivePath,
		ForceWriteArchive: true, Downloader: DownloaderOptions{MaxFilesize: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || !strings.Contains(result.SkipReason, "File is larger than max-filesize") {
		t.Fatalf("filesize abort result = %#v", result)
	}
	if data, readErr := os.ReadFile(archivePath); readErr == nil && len(data) != 0 {
		t.Fatalf("filesize-aborted entry recorded in archive: %q", data)
	}
}
