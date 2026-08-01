//go:build !darwin && !linux

package supervisor

import "os"

// Portable Go file metadata does not expose owner/DACL identity on all
// platforms. The supervisor still enforces regular-file, no-symlink, mode
// (where available), opened-identity, and optional digest checks there.
func safeHelperOwner(os.FileInfo) bool { return true }

// Parent owner/DACL trust is not available from portable FileMode metadata.
func safeHelperParents(string) bool { return true }
