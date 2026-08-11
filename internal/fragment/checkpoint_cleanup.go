package fragment

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type checkpointCleanupOps struct {
	readDir       func(string) ([]os.DirEntry, error)
	lstat         func(string) (os.FileInfo, error)
	remove        func(string) error
	syncDirectory func(string) error
}

var productionCheckpointCleanupOps = checkpointCleanupOps{
	readDir:       os.ReadDir,
	lstat:         os.Lstat,
	remove:        os.Remove,
	syncDirectory: syncCheckpointDirectory,
}

// cleanupCommittedCheckpoint removes non-authority artifacts first and keeps
// publication.json until all of those removals are durably settled. Successful
// removal of publication.json is the cleanup commit point. Directory removal
// after that point is best-effort and cannot turn committed publication into a
// retryable failure.
func cleanupCommittedCheckpoint(workDir string, ops checkpointCleanupOps) error {
	markerPath := filepath.Join(workDir, publicationMarker)
	markerInfo, err := ops.lstat(markerPath)
	if err != nil {
		return fmt.Errorf("inspect durable publication marker: %w", err)
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return checkpointFailure(ErrInvalidCheckpoint, "durable publication marker is a symlink or non-regular file", nil)
	}
	entries, err := ops.readDir(workDir)
	if err != nil {
		return fmt.Errorf("list committed checkpoint cleanup: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == publicationMarker {
			continue
		}
		if name != "state.json" {
			if _, valid := fragmentIndexFromName(name); !valid {
				return checkpointFailure(ErrCheckpointReconciliation, "unknown checkpoint cleanup artifact retained", nil)
			}
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return checkpointFailure(ErrInvalidCheckpoint, "checkpoint cleanup artifact is a symlink or non-regular file", nil)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ops.remove(filepath.Join(workDir, name)); err != nil {
			return fmt.Errorf("remove checkpoint artifact %s: %w", name, err)
		}
	}
	if err := ops.syncDirectory(workDir); err != nil {
		return fmt.Errorf("sync checkpoint artifacts before cleanup commit: %w", err)
	}
	if err := ops.remove(markerPath); err != nil {
		return fmt.Errorf("remove durable publication marker: %w", err)
	}
	// Marker removal is the explicit commit point. A failed durability sync can
	// only make the marker reappear after a crash, which fails closed on reopen;
	// it must not invite a second publication attempt in the current process.
	_ = ops.syncDirectory(workDir)
	_ = ops.remove(workDir)
	return nil
}
