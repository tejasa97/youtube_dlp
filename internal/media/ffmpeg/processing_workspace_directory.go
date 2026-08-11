package ffmpeg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func ensureProcessingWorkspaceDirectory(root, directory string) error {
	// The caller brackets this path walk with stable-root checks. Portable
	// path APIs cannot hold the root identity across one mkdir syscall, so a
	// same-user replacement in that narrow window can leave one component in
	// the replacement tree; the caller's post-walk check then fails closed.
	if err := inspectProcessingRoot(root); err != nil {
		return err
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return processingFailure(ErrInvalidProcessingWorkspace, "workspace is not a dedicated output-root child", err)
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return processingFailure(ErrInvalidProcessingWorkspace, "workspace directory chain is invalid", nil)
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if mkdirErr := createProtectedProcessingDirectory(current); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return mkdirErr
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return processingFailure(ErrInvalidProcessingWorkspace, "workspace chain contains a symlink or non-directory", nil)
		}
		if reparse, err := processingPathIsReparse(current); err != nil || reparse {
			return processingFailure(ErrInvalidProcessingWorkspace, "workspace chain contains a reparse point", err)
		}
		protected, protectErr := processingDirectoryProtected(current, info)
		if protectErr != nil {
			return protectErr
		}
		if !protected {
			return processingFailure(ErrInvalidProcessingWorkspace, "workspace directory chain is not owner-only", nil)
		}
	}
	return nil
}

func inspectProcessingParentChain(root, directory string) error {
	if err := inspectProcessingRoot(root); err != nil {
		return err
	}
	relative, err := filepath.Rel(root, filepath.Dir(directory))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return processingFailure(ErrInvalidProcessingWorkspace, "workspace parent escapes output root", err)
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return processingFailure(ErrInvalidProcessingWorkspace, "workspace parent chain is invalid", nil)
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return processingFailure(ErrInvalidProcessingWorkspace, "workspace parent chain contains a symlink or non-directory", nil)
		}
		if reparse, reparseErr := processingPathIsReparse(current); reparseErr != nil || reparse {
			return processingFailure(ErrInvalidProcessingWorkspace, "workspace parent chain contains a reparse point", reparseErr)
		}
	}
	return nil
}

func ensureProcessingGuardDirectory(root, workspace string) error {
	// Root identity is checked immediately before and after this create by
	// preflight; the path-based syscall itself is not a continuous handle
	// authority on every supported platform.
	guard := processingGuardPath(workspace)
	if err := inspectProcessingParentChain(root, guard); err != nil {
		return err
	}
	info, err := os.Lstat(guard)
	if errors.Is(err, os.ErrNotExist) {
		if err := createProtectedProcessingDirectory(guard); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(guard)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return processingFailure(ErrInvalidProcessingWorkspace, "processing guard is a symlink or non-directory", nil)
	}
	if reparse, err := processingPathIsReparse(guard); err != nil || reparse {
		return processingFailure(ErrInvalidProcessingWorkspace, "processing guard is a reparse point", err)
	}
	protected, err := processingDirectoryProtected(guard, info)
	if err != nil {
		return err
	}
	if !protected {
		return processingFailure(ErrInvalidProcessingWorkspace, "processing guard is not owner-only", nil)
	}
	return nil
}
