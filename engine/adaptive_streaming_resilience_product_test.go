package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
)

func TestProductHLSSplitDiscontinuitySelectsOnlySelectedRepresentation(t *testing.T) {
	var lowManifestRequests, highManifestRequests, segmentRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/low.m3u8":
			lowManifestRequests.Add(1)
			http.Error(writer, "unselected rendition must not be fetched", http.StatusInternalServerError)
		case "/high.m3u8":
			highManifestRequests.Add(1)
			writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(writer, `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-DISCONTINUITY-SEQUENCE:7
#EXTINF:1,
first.ts
#EXT-X-DISCONTINUITY
#EXTINF:1,
second.ts
#EXT-X-ENDLIST
`)
		case "/first.ts":
			segmentRequests.Add(1)
			_, _ = io.WriteString(writer, "first")
		case "/second.ts":
			segmentRequests.Add(1)
			_, _ = io.WriteString(writer, "second")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	input := filepath.Join(t.TempDir(), "selected.info.json")
	data, err := json.Marshal(map[string]any{
		"_type": "video", "id": "selected", "title": "Selected fixture",
		"webpage_url": server.URL + "/page",
		"formats": []any{
			map[string]any{"format_id": "low", "url": server.URL + "/low.m3u8", "ext": "mp4", "protocol": "m3u8_native"},
			map[string]any{"format_id": "high", "url": server.URL + "/high.m3u8", "ext": "mp4", "protocol": "m3u8_native"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := newBroadTestClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: t.TempDir(), Format: "high",
		HLSSplitDiscontinuity: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBytes, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if lowManifestRequests.Load() != 0 {
		t.Fatalf("unselected HLS rendition was fetched %d times", lowManifestRequests.Load())
	}
	if highManifestRequests.Load() != 1 {
		t.Fatalf("selected HLS manifest requests=%d, want one discovery/initial-load response", highManifestRequests.Load())
	}
	if string(resultBytes) != "first" || strings.Contains(filepath.Base(result.Filename), ".d") {
		t.Fatalf("result path=%q bytes=%q info=%s, want one unsuffixed first group", result.Filename, resultBytes, result.InfoJSON)
	}
	if got := segmentRequests.Load(); got != 1 {
		t.Fatalf("segment requests=%d, want one selected group", got)
	}
}

func TestProductHLSSplitDiscontinuityRejectsMergedHLSRepresentations(t *testing.T) {
	var videoManifestRequests, audioManifestRequests, segmentRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/video.m3u8":
			videoManifestRequests.Add(1)
			_, _ = io.WriteString(writer, `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXT-X-DISCONTINUITY-SEQUENCE:70
#EXTINF:1,
video.ts
#EXT-X-ENDLIST
`)
		case "/audio.m3u8":
			audioManifestRequests.Add(1)
			_, _ = io.WriteString(writer, `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXT-X-DISCONTINUITY-SEQUENCE:80
#EXTINF:1,
audio.ts
#EXT-X-ENDLIST
`)
		case "/video.ts", "/audio.ts":
			segmentRequests.Add(1)
			_, _ = io.WriteString(writer, "must not download")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	input := filepath.Join(t.TempDir(), "merged.info.json")
	data, err := json.Marshal(map[string]any{
		"_type": "video", "id": "merged", "title": "Merged fixture",
		"webpage_url": server.URL + "/page",
		"formats": []any{
			map[string]any{"format_id": "video", "url": server.URL + "/video.m3u8", "ext": "mp4", "protocol": "m3u8_native", "vcodec": "avc1", "acodec": "none", "height": 720},
			map[string]any{"format_id": "audio", "url": server.URL + "/audio.m3u8", "ext": "m4a", "protocol": "m3u8_native", "vcodec": "none", "acodec": "aac"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.txt")
	result, err := newBroadTestClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, Format: "video+audio",
		DownloadArchive: archivePath, HLSSplitDiscontinuity: true,
	})
	if err == nil || !IsCategory(err, ErrorUnsupported) || !errors.Is(err, extractor.ErrUnsupported) ||
		!strings.Contains(err.Error(), "multiple HLS representations") {
		t.Fatalf("error=%v, want unsupported multi-HLS representation error", err)
	}
	if result.Downloaded || len(result.Artifacts) != 0 {
		t.Fatalf("result=%#v, want no downloaded artifacts", result)
	}
	if videoManifestRequests.Load() != 0 || audioManifestRequests.Load() != 0 || segmentRequests.Load() != 0 {
		t.Fatalf("manifest/segment requests=%d/%d/%d, want zero pre-download requests", videoManifestRequests.Load(), audioManifestRequests.Load(), segmentRequests.Load())
	}
	entries, readDirErr := os.ReadDir(root)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	if len(entries) != 0 {
		t.Fatalf("output directory entries=%v, want none", entries)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr == nil && len(archive) != 0 {
		t.Fatalf("archive changed after selection rejection: %q", archive)
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
}

func TestProductHLSSplitDiscontinuityRollbackArchiveAndRedactedProgress(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.txt")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/split.m3u8":
			writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			if request.Method == http.MethodHead {
				return
			}
			_, _ = io.WriteString(writer, `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXT-X-DISCONTINUITY-SEQUENCE:40
#EXTINF:1,
fail.ts
#EXT-X-DISCONTINUITY
#EXTINF:1,
ok.ts
#EXT-X-ENDLIST
`)
		case "/ok.ts":
			_, _ = io.WriteString(writer, "ok")
		case "/fail.ts":
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	var messages []string
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Message != "" {
			messages = append(messages, event.Message)
		}
		if strings.Contains(event.URL, "fixture-secret") || strings.Contains(event.Path, "fixture-secret") {
			return fmt.Errorf("signed query leaked in event")
		}
		return nil
	}))
	_, err := client.Run(context.Background(), Request{
		URL: server.URL + "/split.m3u8?sig=fixture-secret", OutputDir: root,
		DownloadArchive: archivePath, HLSSplitDiscontinuity: true,
	})
	if err == nil {
		t.Fatal("expected selected group failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, "split.mp4")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed selected group artifact remains: %v", statErr)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr == nil && len(archive) != 0 {
		t.Fatalf("archive changed after rollback: %q", archive)
	}
	sawGroup := false
	for _, message := range messages {
		if strings.Contains(message, "HLS discontinuity group 40") {
			sawGroup = true
		}
		if strings.Contains(message, "fixture-secret") || strings.Contains(message, "sig=") {
			t.Fatalf("secret in progress message %q", message)
		}
	}
	if !sawGroup {
		t.Fatalf("progress messages did not identify the selected HLS group: %q err=%v", messages, err)
	}
}

func TestProductDASHDynamicMPDPolicyCategories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/dynamic.mpd" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/dash+xml")
		if request.Method == http.MethodHead {
			return
		}
		_, _ = io.WriteString(writer, `<MPD type="dynamic"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1"><BaseURL>segment.bin</BaseURL></Representation></AdaptationSet></Period></MPD>`)
	}))
	defer server.Close()
	root := t.TempDir()
	result, err := newBroadTestClient().Run(context.Background(), Request{
		URL: server.URL + "/dynamic.mpd", OutputDir: root, DenyDynamicMPD: true,
	})
	if !IsCategory(err, ErrorUnsupported) {
		t.Fatalf("error=%v, want unsupported category", err)
	}
	if result.Downloaded {
		t.Fatalf("result=%#v, want no downloaded artifact", result)
	}
	if _, statErr := os.Stat(filepath.Join(root, "dynamic.mp4")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dynamic artifact exists: %v", statErr)
	}
	if !errors.Is(err, extractor.ErrUnsupported) && !strings.Contains(err.Error(), "dynamic DASH MPD unsupported") {
		t.Fatalf("error=%v, want exact dynamic unsupported cause", err)
	}
}

func TestProductHLSExplicitDiscontinuitySequencesFanOutInPlaylistOrder(t *testing.T) {
	var manifestRequests, segmentRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/selected.m3u8":
			manifestRequests.Add(1)
			_, _ = io.WriteString(writer, explicitHLSGroupManifest())
		case "/five.ts":
			segmentRequests.Add(1)
			_, _ = io.WriteString(writer, "five")
		case "/seven.ts":
			segmentRequests.Add(1)
			_, _ = io.WriteString(writer, "seven")
		case "/ad.ts":
			t.Fatalf("ad-only group was fetched")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	input := writeExplicitHLSInfoJSON(t, server.URL+"/selected.m3u8")
	root := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.txt")
	result, err := newBroadTestClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "groups.%(ext)s", Format: "selected",
		HLSDiscontinuitySequences: []int64{7, 5, 7}, DownloadArchive: archivePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	media := mediaArtifactsOnly(result.Artifacts)
	if len(media) != 2 {
		t.Fatalf("media artifacts=%#v, want two", media)
	}
	wantNames := []string{"groups.d5.mp4", "groups.d7.mp4"}
	wantBodies := []string{"five", "seven"}
	for index, artifact := range media {
		if filepath.Base(artifact.Path) != wantNames[index] {
			t.Fatalf("media[%d] path=%q, want %q", index, artifact.Path, wantNames[index])
		}
		body, readErr := os.ReadFile(artifact.Path)
		if readErr != nil || string(body) != wantBodies[index] {
			t.Fatalf("media[%d] body=%q err=%v, want %q", index, body, readErr, wantBodies[index])
		}
	}
	if result.Filename != media[0].Path {
		t.Fatalf("result filename=%q, want first plan %q", result.Filename, media[0].Path)
	}
	if manifestRequests.Load() != 1 || segmentRequests.Load() != 2 {
		t.Fatalf("requests manifest=%d segments=%d, want one discovery/initial-load response and two selected downloads", manifestRequests.Load(), segmentRequests.Load())
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil || strings.Count(string(archive), "\n") != 1 {
		t.Fatalf("archive=%q err=%v, want one committed entry", archive, err)
	}
}

func TestProductHLSExplicitSingleDiscontinuityKeepsDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/selected.m3u8":
			_, _ = io.WriteString(writer, explicitHLSGroupManifest())
		case "/seven.ts":
			_, _ = io.WriteString(writer, "seven")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	input := writeExplicitHLSInfoJSON(t, server.URL+"/selected.m3u8")
	root := t.TempDir()
	result, err := newBroadTestClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "group.%(ext)s", Format: "selected",
		HLSDiscontinuitySequences: []int64{7},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(result.Filename) != "group.mp4" {
		t.Fatalf("single explicit filename=%q, want existing destination", result.Filename)
	}
}

func TestProductHLSExplicitDiscontinuityRejectsBeforeMediaArtifactAndArchiveMutation(t *testing.T) {
	tests := []struct {
		name      string
		manifest  string
		sequences []int64
		want      error
	}{
		{name: "missing", manifest: explicitHLSGroupManifest(), sequences: []int64{99}, want: ErrHLSDiscontinuityGroupMissing},
		{name: "ad-only", manifest: explicitHLSGroupManifest(), sequences: []int64{6}, want: ErrHLSDiscontinuityGroupAdOnly},
		{name: "empty", manifest: "#EXTM3U\n#EXT-X-ENDLIST\n", sequences: []int64{5}, want: ErrHLSDiscontinuityPlaylistEmpty},
		{name: "malformed", manifest: "#EXTM3U\n#EXTINF:not-a-duration,\nsegment.ts\n", sequences: []int64{5}, want: ErrHLSDiscontinuityPlaylistMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var segmentRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/selected.m3u8" {
					_, _ = io.WriteString(writer, test.manifest)
					return
				}
				segmentRequests.Add(1)
				t.Fatalf("segment request %s after selection rejection", request.URL.Path)
			}))
			defer server.Close()
			input := writeExplicitHLSInfoJSON(t, server.URL+"/selected.m3u8")
			root := t.TempDir()
			archivePath := filepath.Join(t.TempDir(), "archive.txt")
			result, err := newBroadTestClient().Run(context.Background(), Request{
				LoadInfoJSON: input, OutputDir: root, OutputTemplate: "group.%(ext)s", Format: "selected",
				HLSDiscontinuitySequences: test.sequences, DownloadArchive: archivePath,
			})
			if err == nil || !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want invalid_input and %v", err, test.want)
			}
			if result.Downloaded || len(result.Artifacts) != 0 || segmentRequests.Load() != 0 {
				t.Fatalf("result=%#v segment requests=%d, want no media or artifacts", result, segmentRequests.Load())
			}
			entries, readDirErr := os.ReadDir(root)
			if readDirErr != nil {
				t.Fatal(readDirErr)
			}
			if len(entries) != 0 {
				t.Fatalf("output entries=%v, want none", entries)
			}
			archive, readErr := os.ReadFile(archivePath)
			if readErr == nil && len(archive) != 0 {
				t.Fatalf("archive changed after selection rejection: %q", archive)
			}
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal(readErr)
			}
		})
	}
}

func TestProductHLSExplicitDiscontinuityRollsBackPartialFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/selected.m3u8":
			_, _ = io.WriteString(writer, explicitHLSGroupManifest())
		case "/five.ts":
			_, _ = io.WriteString(writer, "five")
		case "/seven.ts":
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	input := writeExplicitHLSInfoJSON(t, server.URL+"/selected.m3u8")
	root := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.txt")
	result, err := newBroadTestClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "groups.%(ext)s", Format: "selected",
		HLSDiscontinuitySequences: []int64{5, 7}, DownloadArchive: archivePath,
	})
	if err == nil {
		t.Fatal("expected second explicit group failure")
	}
	if result.Downloaded || len(result.Artifacts) != 0 {
		t.Fatalf("result=%#v, want rolled-back outputs", result)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("output entries=%v, want none after rollback", entries)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr == nil && len(archive) != 0 {
		t.Fatalf("archive changed after partial failure: %q", archive)
	}
}

func TestProductHLSExplicitDiscontinuityPreflightsCollisionsBeforeMedia(t *testing.T) {
	var segmentRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/selected.m3u8" {
			_, _ = io.WriteString(writer, explicitHLSGroupManifest())
			return
		}
		segmentRequests.Add(1)
		t.Fatalf("segment request %s after collision", request.URL.Path)
	}))
	defer server.Close()
	input := writeExplicitHLSInfoJSON(t, server.URL+"/selected.m3u8")
	root := t.TempDir()
	existing := filepath.Join(root, "groups.d5.mp4")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newBroadTestClient().Run(context.Background(), Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "groups.%(ext)s", Format: "selected",
		HLSDiscontinuitySequences: []int64{5, 7},
	})
	if err == nil || !errors.Is(err, downloader.ErrDestinationExists) || segmentRequests.Load() != 0 {
		t.Fatalf("error=%v segments=%d, want destination collision before media", err, segmentRequests.Load())
	}
	body, readErr := os.ReadFile(existing)
	if readErr != nil || string(body) != "keep" {
		t.Fatalf("existing collision target=%q err=%v", body, readErr)
	}
}

func TestProductHLSExplicitDiscontinuityCancellationCleansScratchAndArchive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/selected.m3u8":
			_, _ = io.WriteString(writer, explicitHLSGroupManifest())
		case "/five.ts":
			_, _ = io.WriteString(writer, "five")
		case "/seven.ts":
			cancel()
			<-request.Context().Done()
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	input := writeExplicitHLSInfoJSON(t, server.URL+"/selected.m3u8")
	root := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "archive.txt")
	result, err := newBroadTestClient().Run(ctx, Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "groups.%(ext)s", Format: "selected",
		HLSDiscontinuitySequences: []int64{5, 7}, DownloadArchive: archivePath,
		Downloader: DownloaderOptions{FragmentConcurrency: 1},
	})
	if err == nil || !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want cancelled explicit fan-out", err)
	}
	if result.Downloaded || len(result.Artifacts) != 0 {
		t.Fatalf("result=%#v, want no committed outputs", result)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("output entries=%v, want scratch cleanup", entries)
	}
	archive, readErr := os.ReadFile(archivePath)
	if readErr == nil && len(archive) != 0 {
		t.Fatalf("archive changed after cancellation: %q", archive)
	}
}

func TestProductHLSPreservePartialOnCancelKeepsFragmentLedger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/selected.m3u8":
			_, _ = io.WriteString(writer, explicitHLSGroupManifest())
		case "/five.ts":
			_, _ = io.WriteString(writer, "five")
		case "/seven.ts":
			cancel()
			<-request.Context().Done()
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	input := writeExplicitHLSInfoJSON(t, server.URL+"/selected.m3u8")
	root := t.TempDir()
	result, err := newBroadTestClient().Run(ctx, Request{
		LoadInfoJSON: input, OutputDir: root, OutputTemplate: "groups.%(ext)s", Format: "selected",
		HLSDiscontinuitySequences: []int64{5, 7},
		Downloader:                DownloaderOptions{FragmentConcurrency: 1},
		Filesystem:                FilesystemOptions{PreservePartialOnCancel: true},
	})
	if err == nil || !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want cancelled explicit fan-out", err)
	}
	if result.Downloaded || len(result.Artifacts) != 0 {
		t.Fatalf("result=%#v, want no committed outputs", result)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var foundLedger bool
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".fragments") {
			foundLedger = true
		}
	}
	if !foundLedger {
		t.Fatalf("output entries=%v, want a preserved fragment ledger", entries)
	}
}

func TestProductHLSExplicitDiscontinuityRejectsInvalidAPIValueBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		http.Error(writer, "must not fetch", http.StatusInternalServerError)
	}))
	defer server.Close()
	_, err := newBroadTestClient().Run(context.Background(), Request{
		URL: server.URL + "/selected.m3u8", HLSDiscontinuitySequences: []int64{-1},
	})
	if err == nil || !IsCategory(err, ErrorInvalidInput) || requests.Load() != 0 {
		t.Fatalf("error=%v requests=%d, want invalid input before network", err, requests.Load())
	}
}

func FuzzDeduplicateHLSDiscontinuitySequences(f *testing.F) {
	f.Add([]byte{7, 5, 7, 5})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		sequences := make([]int64, len(input))
		for index, value := range input {
			sequences[index] = int64(value)
		}
		got := deduplicateHLSDiscontinuitySequences(sequences)
		seen := make(map[int64]struct{}, len(got))
		for _, sequence := range got {
			if _, exists := seen[sequence]; exists {
				t.Fatalf("duplicate sequence %d in %#v", sequence, got)
			}
			seen[sequence] = struct{}{}
		}
	})
}

func explicitHLSGroupManifest() string {
	return `#EXTM3U
#EXT-X-TARGETDURATION:1
#EXT-X-MEDIA-SEQUENCE:1
#EXT-X-DISCONTINUITY-SEQUENCE:5
#EXTINF:1,
five.ts
#EXT-X-DISCONTINUITY
#EXT-X-CUE-OUT
#EXTINF:1,
ad.ts
#EXT-X-CUE-IN
#EXT-X-DISCONTINUITY
#EXTINF:1,
seven.ts
#EXT-X-ENDLIST
`
}

func writeExplicitHLSInfoJSON(t *testing.T, manifestURL string) string {
	t.Helper()
	input := filepath.Join(t.TempDir(), "selected.info.json")
	data, err := json.Marshal(map[string]any{
		"_type": "video", "id": "explicit", "title": "Explicit", "extractor_key": "fixture",
		"webpage_url": manifestURL, "formats": []any{
			map[string]any{"format_id": "selected", "url": manifestURL, "ext": "mp4", "protocol": "m3u8_native"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return input
}
