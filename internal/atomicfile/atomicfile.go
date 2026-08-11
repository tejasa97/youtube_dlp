// Package atomicfile provides the low-level, platform-specific commit
// primitive used by durable engine state. It deliberately does not validate
// higher-level path authority; callers must do that before invoking it.
package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// CommitError reports the replacement outcome. Committed and Indeterminate
// are mutually exclusive: when Committed returns true, callers must adopt the
// new disk image; when Indeterminate returns true, neither old nor new
// authority could be established and callers must reconcile before acting.
// Both false identifies an ordinary pre-commit failure.
type CommitError interface {
	error
	Committed() bool
	Indeterminate() bool
}

type commitError struct {
	operation     string
	err           error
	committed     bool
	indeterminate bool
}

func (err *commitError) Error() string {
	return fmt.Sprintf("atomic file: %s: %v", err.operation, err.err)
}

func (err *commitError) Unwrap() error { return err.err }

func (err *commitError) Committed() bool { return err.committed }

func (err *commitError) Indeterminate() bool { return err.indeterminate }

func failure(operation string, err error, committed bool) error {
	if err == nil {
		return nil
	}
	return &commitError{operation: operation, err: err, committed: committed}
}

func indeterminateFailure(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &commitError{operation: operation, err: err, indeterminate: true}
}

type fileOps struct {
	createTemp  func(string, string) (*os.File, error)
	openForSync func(string) (*os.File, error)
	writeTemp   func(io.Writer, func(io.Writer) error) error
	syncFile    func(*os.File) error
	replaceFile func(string, string) error
	syncParent  func(string) error
	cleanupTemp func(string) error
}

var productionOps = fileOps{
	createTemp:  os.CreateTemp,
	openForSync: platformOpenForSync,
	writeTemp: func(writer io.Writer, encode func(io.Writer) error) error {
		return encode(writer)
	},
	syncFile:    (*os.File).Sync,
	replaceFile: platformReplace,
	syncParent:  platformSyncParent,
	cleanupTemp: platformCleanupTemp,
}

// Write creates and syncs a temporary file beside path, then atomically
// installs it and syncs the containing directory where the platform supports
// that operation. Every failure implements CommitError.
func Write(path string, perm fs.FileMode, encode func(io.Writer) error) error {
	return write(path, perm, encode, productionOps)
}

func write(path string, perm fs.FileMode, encode func(io.Writer) error, ops fileOps) (result error) {
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
	retainTemporary := false
	defer func() {
		_ = temporary.Close()
		if committed || retainTemporary {
			return
		}
		if cleanupErr := ops.cleanupTemp(temporaryPath); cleanupErr != nil {
			result = failure(
				"clean temporary file after pre-commit failure",
				errors.Join(result, cleanupErr),
				false,
			)
		}
	}()

	if err := ops.writeTemp(temporary, encode); err != nil {
		return failure("write temporary file", err, false)
	}
	if err := temporary.Chmod(perm); err != nil {
		return failure("set temporary file mode", err, false)
	}
	if err := ops.syncFile(temporary); err != nil {
		return failure("sync temporary file", err, false)
	}
	if err := temporary.Close(); err != nil {
		return failure("close temporary file", err, false)
	}
	if err := ops.replaceFile(temporaryPath, path); err != nil {
		classified := classifyReplacementError(err)
		var commitErr CommitError
		if errors.As(classified, &commitErr) {
			retainTemporary = commitErr.Indeterminate()
			committed = commitErr.Committed()
		}
		return classified
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
	file, err := ops.openForSync(source)
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
		return classifyReplacementError(err)
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

func classifyReplacementError(err error) error {
	var commitErr CommitError
	if errors.As(err, &commitErr) {
		return err
	}
	return failure("replace destination", err, false)
}

const (
	errorUnableToMoveReplacement  = syscall.Errno(1176)
	errorUnableToMoveReplacement2 = syscall.Errno(1177)
)

type replaceRecoveryOps struct {
	backupExists      func(string) (bool, error)
	destinationExists func(string) (bool, error)
	restoreBackup     func(string, string) error
	removeBackup      func(string) error
}

// handleExistingReplaceResult converts ReplaceFileW's backup-aware result
// into the commit contract. A failed replacement is pre-commit only after the
// old destination is known to remain in place or has been restored.
func handleExistingReplaceResult(
	replaceErr error,
	backup, destination string,
	ops replaceRecoveryOps,
) error {
	if replaceErr == nil {
		if err := ops.removeBackup(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			return failure("remove committed replacement backup", err, true)
		}
		return nil
	}

	backupExists, inspectErr := ops.backupExists(backup)
	if inspectErr != nil {
		return indeterminateFailure(
			"inspect failed replacement backup",
			errors.Join(replaceErr, inspectErr),
		)
	}
	if errors.Is(replaceErr, errorUnableToMoveReplacement2) {
		if !backupExists {
			return indeterminateFailure(
				"locate failed replacement backup",
				errors.Join(replaceErr, errors.New("replacement backup is missing")),
			)
		}
		if recoveryErr := ops.restoreBackup(backup, destination); recoveryErr != nil {
			return indeterminateFailure(
				"restore failed replacement backup",
				errors.Join(replaceErr, recoveryErr),
			)
		}
		return failure("replace destination", replaceErr, false)
	}

	// ReplaceFileW documents that 1176 and all other failures retain the
	// original names. An unexpected backup contradicts that authority model;
	// never move it over the destination.
	if backupExists {
		return indeterminateFailure(
			"unexpected failed replacement backup",
			errors.Join(replaceErr, errors.New("replacement backup unexpectedly exists")),
		)
	}
	destinationExists, destinationErr := ops.destinationExists(destination)
	if destinationErr != nil {
		return indeterminateFailure(
			"inspect destination after failed replacement",
			errors.Join(replaceErr, destinationErr),
		)
	}
	if !destinationExists {
		return indeterminateFailure(
			"locate destination after failed replacement",
			errors.Join(replaceErr, errors.New("original destination is missing")),
		)
	}
	return failure("replace destination", replaceErr, false)
}
