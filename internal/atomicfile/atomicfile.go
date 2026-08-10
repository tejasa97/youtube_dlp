// Package atomicfile provides the low-level, platform-specific commit
// primitive used by durable engine state. It deliberately does not validate
// higher-level path authority; callers must do that before invoking it.
package atomicfile

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CommitError reports whether the replacement commit point was crossed.
// When Committed returns true, callers must adopt the new disk image even
// though its durability could not be confirmed.
type CommitError interface {
	error
	Committed() bool
}

type commitError struct {
	operation string
	err       error
	committed bool
}

func (err *commitError) Error() string {
	return fmt.Sprintf("atomic file: %s: %v", err.operation, err.err)
}

func (err *commitError) Unwrap() error { return err.err }

func (err *commitError) Committed() bool { return err.committed }

func failure(operation string, err error, committed bool) error {
	if err == nil {
		return nil
	}
	return &commitError{operation: operation, err: err, committed: committed}
}

type fileOps struct {
	createTemp  func(string, string) (*os.File, error)
	writeTemp   func(io.Writer, func(io.Writer) error) error
	syncFile    func(*os.File) error
	replaceFile func(string, string) error
	syncParent  func(string) error
}

var productionOps = fileOps{
	createTemp: os.CreateTemp,
	writeTemp: func(writer io.Writer, encode func(io.Writer) error) error {
		return encode(writer)
	},
	syncFile:    (*os.File).Sync,
	replaceFile: platformReplace,
	syncParent:  platformSyncParent,
}

// Write creates and syncs a temporary file beside path, then atomically
// installs it and syncs the containing directory where the platform supports
// that operation. Every failure implements CommitError.
func Write(path string, perm fs.FileMode, encode func(io.Writer) error) error {
	return write(path, perm, encode, productionOps)
}

func write(path string, perm fs.FileMode, encode func(io.Writer) error, ops fileOps) error {
	if encode == nil {
		return failure("write temporary file", fmt.Errorf("nil encoder"), false)
	}
	directory := filepath.Dir(path)
	temporary, err := ops.createTemp(directory, ".atomic-*")
	if err != nil {
		return failure("create temporary file", err, false)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(perm); err != nil {
		return failure("set temporary file mode", err, false)
	}
	if err := ops.writeTemp(temporary, encode); err != nil {
		return failure("write temporary file", err, false)
	}
	if err := ops.syncFile(temporary); err != nil {
		return failure("sync temporary file", err, false)
	}
	if err := temporary.Close(); err != nil {
		return failure("close temporary file", err, false)
	}
	if err := ops.replaceFile(temporaryPath, path); err != nil {
		return failure("replace destination", err, false)
	}
	committed = true
	if err := ops.syncParent(directory); err != nil {
		return failure("sync destination directory", err, true)
	}
	return nil
}

// Replace syncs source, atomically moves it over destination, and syncs the
// affected parent directories where supported. Every failure implements
// CommitError. A cross-filesystem move fails before the commit point.
func Replace(source, destination string) error {
	return replace(source, destination, productionOps)
}

func replace(source, destination string, ops fileOps) error {
	file, err := os.Open(source)
	if err != nil {
		return failure("open source", err, false)
	}
	if err := ops.syncFile(file); err != nil {
		_ = file.Close()
		return failure("sync source", err, false)
	}
	if err := file.Close(); err != nil {
		return failure("close source", err, false)
	}
	if err := ops.replaceFile(source, destination); err != nil {
		return failure("replace destination", err, false)
	}

	destinationParent := filepath.Dir(destination)
	if err := ops.syncParent(destinationParent); err != nil {
		return failure("sync destination directory", err, true)
	}
	sourceParent := filepath.Dir(source)
	if filepath.Clean(sourceParent) != filepath.Clean(destinationParent) {
		if err := ops.syncParent(sourceParent); err != nil {
			return failure("sync source directory", err, true)
		}
	}
	return nil
}
