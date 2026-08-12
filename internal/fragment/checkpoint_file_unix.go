//go:build !windows

package fragment

import (
	"io"
	"os"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
)

func writeProtectedCheckpointArtifactPlatform(path string, mode os.FileMode, encode func(io.Writer) error) error {
	return atomicfile.Write(path, mode, encode)
}

func secureCheckpointFile(string) error { return nil }

func checkpointFileProtected(_ string, info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0
}
