package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
)

const nTrackWorkspaceManifestVersion = 1

var nTrackWorkspaceLocks = struct {
	sync.Mutex
	entries map[string]*nTrackWorkspaceLock
}{entries: make(map[string]*nTrackWorkspaceLock)}

type nTrackWorkspaceLock struct {
	mutex sync.Mutex
	users int
}

type nTrackWorkspaceManifest struct {
	Version     int      `json:"version"`
	Destination string   `json:"destination"`
	Tracks      []string `json:"tracks"`
}

type nTrackSelectionIdentity struct {
	ID       string `json:"id"`
	Ext      string `json:"ext"`
	VCodec   string `json:"vcodec"`
	ACodec   string `json:"acodec"`
	Protocol string `json:"protocol"`
	Source   string `json:"source"`
}

func nTrackResumeIdentity(selection mediaformat.Selection) string {
	source := selection.YouTubeSourceURL
	if source == "" {
		source = selection.YouTubeSABRVideoID
	}
	if source == "" {
		// Hash the exact media URL for providers without a stable source-media
		// identity. This preserves the existing exact-URL resume boundary while
		// keeping credentials out of the workspace manifest and partial state.
		source = selection.URL
	}
	encoded, _ := json.Marshal(nTrackSelectionIdentity{
		ID:       selection.ID,
		Ext:      selection.Ext,
		VCodec:   selection.VCodec,
		ACodec:   selection.ACodec,
		Protocol: selection.Protocol,
		Source:   source,
	})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func expectedNTrackWorkspace(
	outputRoot, destination string,
	selections []mediaformat.Selection,
) (string, nTrackWorkspaceManifest, error) {
	root, err := filepath.Abs(outputRoot)
	if err != nil {
		return "", nTrackWorkspaceManifest{}, fmt.Errorf("resolve selected-format output root: %w", err)
	}
	target, err := filepath.Abs(destination)
	if err != nil {
		return "", nTrackWorkspaceManifest{}, fmt.Errorf("resolve selected-format destination: %w", err)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || !isNTrackDestinationRelative(relative) {
		return "", nTrackWorkspaceManifest{}, fmt.Errorf("selected-format destination escapes output root")
	}
	identities := make([]string, len(selections))
	for index, selection := range selections {
		identities[index] = nTrackResumeIdentity(selection)
	}
	manifest := nTrackWorkspaceManifest{
		Version:     nTrackWorkspaceManifestVersion,
		Destination: filepath.ToSlash(relative),
		Tracks:      identities,
	}
	digest := sha256.Sum256([]byte(manifest.Destination))
	workspace := filepath.Join(root, ".ytdlp-formats-"+hex.EncodeToString(digest[:12]))
	return workspace, manifest, nil
}

func isNTrackDestinationRelative(relative string) bool {
	if relative == "." || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func acquireNTrackWorkspace(workspace string) func() {
	nTrackWorkspaceLocks.Lock()
	entry := nTrackWorkspaceLocks.entries[workspace]
	if entry == nil {
		entry = &nTrackWorkspaceLock{}
		nTrackWorkspaceLocks.entries[workspace] = entry
	}
	entry.users++
	nTrackWorkspaceLocks.Unlock()

	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		nTrackWorkspaceLocks.Lock()
		entry.users--
		if entry.users == 0 {
			delete(nTrackWorkspaceLocks.entries, workspace)
		}
		nTrackWorkspaceLocks.Unlock()
	}
}

func prepareNTrackWorkspace(
	outputRoot, destination string,
	selections []mediaformat.Selection,
	noContinue bool,
) (string, error) {
	workspace, expected, err := expectedNTrackWorkspace(outputRoot, destination, selections)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
		return "", fmt.Errorf("create selected-format output root: %w", err)
	}
	if noContinue {
		if err := removeNTrackWorkspace(workspace); err != nil {
			return "", err
		}
	}
	if info, statErr := os.Lstat(workspace); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("selected-format workspace is not a directory")
		}
		manifest, readErr := readNTrackWorkspaceManifest(workspace)
		if readErr == nil && manifest.Version == expected.Version &&
			manifest.Destination == expected.Destination && slices.Equal(manifest.Tracks, expected.Tracks) {
			return workspace, nil
		}
		if err := removeNTrackWorkspace(workspace); err != nil {
			return "", err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect selected-format workspace: %w", statErr)
	}
	if err := os.Mkdir(workspace, 0o700); err != nil {
		return "", fmt.Errorf("create selected-format workspace: %w", err)
	}
	if err := writeNTrackWorkspaceManifest(workspace, expected); err != nil {
		_ = removeNTrackWorkspace(workspace)
		return "", err
	}
	return workspace, nil
}

func readNTrackWorkspaceManifest(workspace string) (nTrackWorkspaceManifest, error) {
	encoded, err := os.ReadFile(filepath.Join(workspace, "manifest.json"))
	if err != nil {
		return nTrackWorkspaceManifest{}, err
	}
	var manifest nTrackWorkspaceManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return nTrackWorkspaceManifest{}, err
	}
	return manifest, nil
}

func writeNTrackWorkspaceManifest(workspace string, manifest nTrackWorkspaceManifest) error {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(workspace, ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create selected-format manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(encoded)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write selected-format manifest: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(workspace, "manifest.json")); err != nil {
		return fmt.Errorf("publish selected-format manifest: %w", err)
	}
	return nil
}

func removeNTrackWorkspace(workspace string) error {
	info, err := os.Lstat(workspace)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect selected-format workspace: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("selected-format workspace is not a directory")
	}
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("remove selected-format workspace: %w", err)
	}
	return nil
}

func reusableNTrack(track string) (int64, bool, error) {
	info, err := os.Lstat(track)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("inspect selected-format track: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, false, fmt.Errorf("selected-format track is not a regular file")
	}
	if info.Size() <= 0 {
		return 0, false, nil
	}
	return info.Size(), true, nil
}
