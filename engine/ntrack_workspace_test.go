package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
)

func testNTrackSelections(token string) []mediaformat.Selection {
	return []mediaformat.Selection{
		{ID: "video", Ext: "mp4", VCodec: "avc1", ACodec: "none", Protocol: "https", URL: "https://media.invalid/video?token=" + token, YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001"},
		{ID: "audio", Ext: "m4a", VCodec: "none", ACodec: "mp4a", Protocol: "https", URL: "https://media.invalid/audio?token=" + token, YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001"},
	}
}

func TestNTrackWorkspaceReusesMatchingSelectionWithoutPersistingURLs(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "downloads", "video.mp4")
	first := testNTrackSelections("secret-first")
	workspace, err := prepareNTrackWorkspace(root, destination, first, false)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "keep-me")
	if err := os.WriteFile(marker, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	refreshed := testNTrackSelections("secret-refreshed")
	reused, err := prepareNTrackWorkspace(root, destination, refreshed, false)
	if err != nil {
		t.Fatal(err)
	}
	if reused != workspace {
		t.Fatalf("workspace = %q, want %q", reused, workspace)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("matching workspace was not reused: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(workspace, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), "secret-") || strings.Contains(string(manifest), "media.invalid") || strings.Contains(string(manifest), "youtube.com") {
		t.Fatalf("manifest persisted an expiring URL: %s", manifest)
	}
}

func TestNTrackWorkspaceKeepsExactURLBoundaryWithoutStableSource(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "video.mp4")
	selections := testNTrackSelections("first")
	for index := range selections {
		selections[index].YouTubeSourceURL = ""
	}
	workspace, err := prepareNTrackWorkspace(root, destination, selections, false)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "old-url-partial")
	if err := os.WriteFile(marker, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed := testNTrackSelections("second")
	for index := range refreshed {
		refreshed[index].YouTubeSourceURL = ""
	}
	if _, err := prepareNTrackWorkspace(root, destination, refreshed, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed URL reused a workspace without stable source identity: %v", err)
	}
}

func TestNTrackWorkspaceReplacesStaleSelection(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "video.mp4")
	selections := testNTrackSelections("first")
	workspace, err := prepareNTrackWorkspace(root, destination, selections, false)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "stale-partial")
	if err := os.WriteFile(marker, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := testNTrackSelections("second")
	changed[0].ID = "different-video"
	if _, err := prepareNTrackWorkspace(root, destination, changed, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale workspace content remains: %v", err)
	}
}

func TestNTrackWorkspaceReplacesDifferentSourceMedia(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "video.mp4")
	selections := testNTrackSelections("first")
	workspace, err := prepareNTrackWorkspace(root, destination, selections, false)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "wrong-media-partial")
	if err := os.WriteFile(marker, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	different := testNTrackSelections("second")
	for index := range different {
		different[index].YouTubeSourceURL = "https://www.youtube.com/watch?v=fixture0002"
	}
	if _, err := prepareNTrackWorkspace(root, destination, different, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("different-source workspace content remains: %v", err)
	}
}

func TestNTrackWorkspaceNoContinueStartsClean(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "video.mp4")
	selections := testNTrackSelections("first")
	workspace, err := prepareNTrackWorkspace(root, destination, selections, false)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "partial")
	if err := os.WriteFile(marker, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareNTrackWorkspace(root, destination, selections, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-continue retained workspace content: %v", err)
	}
}

func TestNTrackWorkspaceRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "video.mp4")
	selections := testNTrackSelections("first")
	workspace, _, err := expectedNTrackWorkspace(root, destination, selections)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "untouched")
	if err := os.WriteFile(marker, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, workspace); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := prepareNTrackWorkspace(root, destination, selections, false); err == nil {
		t.Fatal("symlink workspace was accepted")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "safe" {
		t.Fatalf("outside symlink target changed: %q, %v", data, err)
	}
}

func TestNTrackWorkspaceRejectsDestinationOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "video.mp4")
	if _, _, err := expectedNTrackWorkspace(root, outside, testNTrackSelections("first")); err == nil {
		t.Fatal("destination outside output root was accepted")
	}
}

func TestNTrackWorkspaceLockSerializesSameDestination(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), ".ytdlp-formats-fixture")
	releaseFirst := acquireNTrackWorkspace(workspace)

	started := make(chan struct{})
	acquired := make(chan struct{})
	var releaseSecond func()
	var releaseMu sync.Mutex
	go func() {
		close(started)
		release := acquireNTrackWorkspace(workspace)
		releaseMu.Lock()
		releaseSecond = release
		releaseMu.Unlock()
		close(acquired)
	}()
	<-started
	select {
	case <-acquired:
		t.Fatal("second job acquired the same workspace concurrently")
	case <-time.After(100 * time.Millisecond):
	}

	releaseFirst()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("second job did not acquire the released workspace")
	}
	releaseMu.Lock()
	releaseSecond()
	releaseMu.Unlock()
}
