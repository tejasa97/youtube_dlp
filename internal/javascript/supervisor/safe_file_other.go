//go:build !darwin && !linux

package supervisor

import "os"

// Portable Go file metadata does not expose owner/DACL identity on all
// platforms. The supervisor still enforces regular-file, no-symlink, mode
// (where available), opened-identity, and optional digest checks there.
func safeHelperOwner(os.FileInfo) bool { return true }
