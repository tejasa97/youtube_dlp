//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func openDiscardRoot(path, expectedIdentity string) (*discardDirectory, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !discardOwnerOnlyDirectoryHandle(file, info) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	identity, err := unixDiscardIdentity(info)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if expectedIdentity != "" && identity != expectedIdentity {
		_ = file.Close()
		return nil, ErrNeedsReconciliation
	}
	return &discardDirectory{file: file, path: path, identity: identity}, nil
}

func openDiscardEntry(parent *discardDirectory, name string, expected os.FileInfo, wantDirectory bool) (*discardEntryHandle, error) {
	if parent == nil || parent.file == nil || !validDiscardEntryName(name) {
		return nil, ErrUnsafePath
	}
	flags := unix.O_CLOEXEC | unix.O_NOFOLLOW
	if wantDirectory {
		flags |= unix.O_RDONLY | unix.O_DIRECTORY
	} else {
		flags |= unix.O_RDONLY
	}
	fd, err := unix.Openat(int(parent.file.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(parent.path, name))
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (wantDirectory && !info.IsDir()) || (!wantDirectory && !info.Mode().IsRegular()) || !discardOwnerOnlyFileHandle(file, info) && !wantDirectory {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	if wantDirectory && !discardOwnerOnlyDirectoryHandle(file, info) {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	if expected != nil && !sameUnixObject(expected, info) {
		_ = file.Close()
		return nil, ErrNeedsReconciliation
	}
	if !wantDirectory && unixDiscardLinkCount(info) != 1 {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	identity, err := unixDiscardIdentity(info)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &discardEntryHandle{
		file: file, parent: parent, path: filepath.Join(parent.path, name), name: name,
		expected: info, identity: identity, directory: wantDirectory,
	}, nil
}

func (entry *discardEntryHandle) remove() error {
	if entry == nil || entry.file == nil || entry.parent == nil || entry.parent.file == nil {
		return ErrWorkspaceClosed
	}
	var current unix.Stat_t
	if err := unix.Fstatat(int(entry.parent.file.Fd()), entry.name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return os.ErrNotExist
		}
		return err
	}
	if !sameUnixObjectWithStat(entry.expected, &current) || unixStatIsSymlink(&current) ||
		(entry.directory != unixStatIsDirectory(&current)) ||
		(!entry.directory && (!unixStatIsRegular(&current) || uint64(current.Nlink) != 1)) {
		return ErrNeedsReconciliation
	}
	flags := 0
	if entry.directory {
		flags = unix.AT_REMOVEDIR
	}
	if err := unix.Unlinkat(int(entry.parent.file.Fd()), entry.name, flags); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return os.ErrNotExist
		}
		return err
	}
	return nil
}

func syncDiscardDirectoryHandle(directory *discardDirectory) error {
	if directory == nil || directory.file == nil {
		return ErrWorkspaceClosed
	}
	return directory.file.Sync()
}

func discardOwnerOnlyDirectoryHandle(_ *os.File, info os.FileInfo) bool {
	return ownerOnlyDirectory(info)
}

func discardOwnerOnlyFileHandle(_ *os.File, info os.FileInfo) bool {
	return ownerOnlyFile(info)
}

func unixDiscardIdentity(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return "", ErrWorkspaceUnavailable
	}
	return fmt.Sprintf("unix:%x:%x", uint64(stat.Dev), uint64(stat.Ino)), nil
}

func unixDiscardLinkCount(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Nlink)
}

func sameUnixObject(left, right os.FileInfo) bool {
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && leftStat.Dev == rightStat.Dev && leftStat.Ino == rightStat.Ino
}

func sameUnixObjectWithStat(expected os.FileInfo, current *unix.Stat_t) bool {
	stat, ok := expected.Sys().(*syscall.Stat_t)
	return ok && stat.Dev == current.Dev && stat.Ino == current.Ino
}

func unixStatIsDirectory(stat *unix.Stat_t) bool {
	return stat.Mode&syscall.S_IFMT == syscall.S_IFDIR
}
func unixStatIsRegular(stat *unix.Stat_t) bool { return stat.Mode&syscall.S_IFMT == syscall.S_IFREG }
func unixStatIsSymlink(stat *unix.Stat_t) bool { return stat.Mode&syscall.S_IFMT == syscall.S_IFLNK }
