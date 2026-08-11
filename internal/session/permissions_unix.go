//go:build !windows

package session

import "os"

func ownerOnlyDirectory(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}

func ownerOnlyDirectoryAt(_ string, info os.FileInfo) bool { return ownerOnlyDirectory(info) }

func ownerOnlyDirectoryFromPath(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && ownerOnlyDirectory(info)
}

func ownerOnlyFile(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 == 0
}

func ownerOnlyFileAt(_ string, info os.FileInfo) bool { return ownerOnlyFile(info) }

func secureDirectoryPath(string) error { return nil }

func secureFilePath(string) error { return nil }
