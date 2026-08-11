//go:build !windows

package fragment

import "os"

func createProtectedCheckpointDirectory(path string) error {
	return os.Mkdir(path, 0o700)
}

func checkpointDirectoryProtected(_ string, info os.FileInfo) (bool, error) {
	return info.Mode().Perm()&0o077 == 0, nil
}
