package session

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func newWorkspaceRef(outputRoot, sessionID string) (WorkspaceRef, error) {
	root, err := canonicalOutputRoot(outputRoot)
	if err != nil {
		return WorkspaceRef{}, err
	}
	if !validSessionID(sessionID) {
		return WorkspaceRef{}, ErrInvalidReference
	}
	return WorkspaceRef{OutputRoot: root, SessionID: sessionID}, nil
}

// NewWorkspaceRef canonicalizes portable reference data and validates the
// session-id shape without touching the network or opening a lease.
func NewWorkspaceRef(outputRoot, sessionID string) (WorkspaceRef, error) {
	return newWorkspaceRef(outputRoot, sessionID)
}

// NewWorkspaceRefWithIdentity binds a portable workspace reference to an
// already-validated output root. Existing callers that do not carry an
// identity retain the legacy reference shape; public maintenance calls use
// this stricter constructor and fail closed if the root is replaced.
func NewWorkspaceRefWithIdentity(outputRoot, identity, sessionID string) (WorkspaceRef, error) {
	ref, err := newWorkspaceRef(outputRoot, sessionID)
	if err != nil || !validRootIdentity(identity) {
		return WorkspaceRef{}, ErrInvalidReference
	}
	ref.OutputRootIdentity = identity
	return ref, nil
}

func (ref WorkspaceRef) validate() error {
	if _, err := canonicalOutputRoot(ref.OutputRoot); err != nil {
		return ErrInvalidReference
	}
	if !validSessionID(ref.SessionID) {
		return ErrInvalidReference
	}
	if ref.OutputRootIdentity != "" && !validRootIdentity(ref.OutputRootIdentity) {
		return ErrInvalidReference
	}
	return nil
}

// Path returns the exact hidden workspace path represented by the reference.
func (ref WorkspaceRef) Path() (string, error) {
	if err := ref.validate(); err != nil {
		return "", err
	}
	root := filepath.Clean(ref.OutputRoot)
	sessions := filepath.Join(root, SessionsDirectoryName)
	workspace := filepath.Join(sessions, ref.SessionID)
	relative, err := filepath.Rel(sessions, workspace)
	if err != nil || relative != ref.SessionID || strings.Contains(relative, string(filepath.Separator)) {
		return "", ErrInvalidReference
	}
	return workspace, nil
}

func (ref WorkspaceRef) manifestPath() (string, error) {
	workspace, err := ref.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(workspace, ManifestFileName), nil
}

func (ref WorkspaceRef) leasePath() (string, error) {
	workspace, err := ref.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(workspace, LeaseFileName), nil
}

func canonicalOutputRoot(root string) (string, error) {
	if root == "" || strings.IndexByte(root, 0) >= 0 || !filepath.IsAbs(root) {
		return "", ErrUnsafePath
	}
	clean := filepath.Clean(root)
	if clean == "." || strings.IndexByte(clean, 0) >= 0 {
		return "", ErrUnsafePath
	}
	return clean, nil
}

func ensureOutputRoot(root string) error {
	canonical, err := canonicalOutputRoot(root)
	if err != nil {
		return err
	}
	return ensureDirectory(canonical, 0o755, false)
}

const (
	sharedSessionsRootWindow     = 1500 * time.Millisecond
	sharedSessionsRootBackoff    = 2 * time.Millisecond
	sharedSessionsRootMaxBackoff = 50 * time.Millisecond
)

type sharedSessionsRootOps struct {
	lstat  func(string) (os.FileInfo, error)
	ensure func(string) error
	now    func() time.Time
	sleep  func(time.Duration)
}

// ensureSharedSessionsRoot retries only the narrow race where this process
// observed a missing shared root and another creator made the directory while
// its ACL was still being hardened. A pre-existing directory is never retried
// or accepted by this path; every attempt still performs the strict owner-only
// validation in ensureDirectory.
func ensureSharedSessionsRoot(path string) error {
	return ensureSharedSessionsRootWith(path, sharedSessionsRootOps{
		lstat:  os.Lstat,
		ensure: func(path string) error { return ensureDirectory(path, 0o700, true) },
		now:    time.Now,
		sleep:  time.Sleep,
	})
}

func ensureSharedSessionsRootWith(path string, ops sharedSessionsRootOps) error {
	deadline := ops.now().Add(sharedSessionsRootWindow)
	backoff := sharedSessionsRootBackoff
	initiallyMissing := false
	for {
		_, beforeErr := ops.lstat(path)
		wasMissing := errors.Is(beforeErr, os.ErrNotExist)
		if wasMissing {
			initiallyMissing = true
		} else if beforeErr != nil && !initiallyMissing {
			return ops.ensure(path)
		}
		err := ops.ensure(path)
		if err == nil {
			return nil
		}
		if !initiallyMissing || (!errors.Is(err, ErrWorkspaceUnavailable) && !errors.Is(err, ErrUnsafePath)) {
			return err
		}
		if !ops.now().Before(deadline) {
			return err
		}
		info, afterErr := ops.lstat(path)
		if afterErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return err
		}
		remaining := deadline.Sub(ops.now())
		if remaining <= 0 {
			return err
		}
		if backoff > remaining {
			backoff = remaining
		}
		ops.sleep(backoff)
		if backoff < sharedSessionsRootMaxBackoff {
			backoff *= 2
			if backoff > sharedSessionsRootMaxBackoff {
				backoff = sharedSessionsRootMaxBackoff
			}
		}
	}
}

func ensureDirectory(path string, mode fs.FileMode, ownerOnly bool) error {
	if path == "" || !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return ErrUnsafePath
	}
	path = filepath.Clean(path)
	var missing []string
	current := path
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || (ownerOnly && current == path && !ownerOnlyDirectoryAt(current, info)) {
				return ErrUnsafePath
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return ErrWorkspaceUnavailable
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ErrWorkspaceUnavailable
		}
		missing = append(missing, current)
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		created := missing[index]
		if err := os.Mkdir(created, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return ErrWorkspaceUnavailable
		}
		info, err := os.Lstat(created)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrUnsafePath
		}
		if ownerOnly && created == path {
			if err := os.Chmod(created, 0o700); err != nil || secureDirectoryPath(created) != nil || !ownerOnlyDirectoryFromPath(created) {
				return ErrUnsafePath
			}
		}
	}
	return nil
}

func validateExistingWorkspace(ref WorkspaceRef) (string, error) {
	if err := ref.validate(); err != nil {
		return "", err
	}
	if err := validateWorkspaceRootIdentity(ref); err != nil {
		return "", err
	}
	root := filepath.Clean(ref.OutputRoot)
	if err := validateDirectoryChain(root, false); err != nil {
		return "", err
	}
	sessions := filepath.Join(root, SessionsDirectoryName)
	if err := validateDirectoryChain(sessions, true); err != nil {
		return "", err
	}
	workspace, err := ref.Path()
	if err != nil {
		return "", err
	}
	if err := validateDirectoryChain(workspace, true); err != nil {
		return "", err
	}
	return workspace, nil
}

func validateDirectory(path string, ownerOnly bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrWorkspaceUnavailable
	}
	if err != nil {
		return ErrWorkspaceUnavailable
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || (ownerOnly && !ownerOnlyDirectoryAt(path, info)) {
		return ErrUnsafePath
	}
	return nil
}

func validateDirectoryChain(path string, ownerOnlyFinal bool) error {
	// The declared output root itself and each workspace directory are
	// validated as non-symlink directories. System path prefixes may be
	// symlinks on supported hosts (for example /var on macOS); rejecting those
	// would make an otherwise safe absolute output root unusable.
	return validateDirectory(path, ownerOnlyFinal)
}

// validateDestination validates a manifest-derived path without creating or
// following any output path. Existing path components, including the final
// destination, may not be symlinks.
func validateDestination(outputRoot, relative string) error {
	root, err := canonicalOutputRoot(outputRoot)
	if err != nil || !isSafeRelativePath(relative) {
		return ErrUnsafePath
	}
	if err := validateDirectory(root, false); err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	contained, err := filepath.Rel(root, target)
	if err != nil || contained != filepath.FromSlash(relative) || filepath.IsAbs(contained) || strings.HasPrefix(contained, ".."+string(filepath.Separator)) || contained == ".." {
		return ErrUnsafePath
	}
	return validateExistingComponents(root, target)
}

func validateExistingComponents(root, target string) error {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	rootInfo, rootErr := os.Lstat(root)
	if rootErr != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return ErrUnsafePath
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrUnsafePath
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
	}
	return nil
}
