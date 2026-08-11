package session

import "errors"

// RootRef is a stable, no-follow identity for a caller-selected output root.
// Identity is opaque and platform-specific; callers compare it only for exact
// equality and must never derive filesystem paths from it.
type RootRef struct {
	CanonicalPath string
	Identity      string
}

// ValidateOutputRoot validates an existing output root without following a
// symlink or Windows reparse point and returns its stable directory identity.
func ValidateOutputRoot(path string) (RootRef, error) {
	root, err := canonicalOutputRoot(path)
	if err != nil {
		return RootRef{}, err
	}
	if err := validateDirectory(root, false); err != nil {
		return RootRef{}, err
	}
	identity, err := directoryIdentity(root)
	if err != nil || !validRootIdentity(identity) {
		return RootRef{}, ErrUnsafePath
	}
	return RootRef{CanonicalPath: root, Identity: identity}, nil
}

// ValidateRootRef revalidates the declared root with no-follow semantics and
// proves that its stable identity has not changed. Unix uses an Lstat identity
// snapshot; Windows uses a no-follow directory handle.
func ValidateRootRef(ref RootRef) error {
	if ref.CanonicalPath == "" || !validRootIdentity(ref.Identity) {
		return ErrInvalidReference
	}
	observed, err := ValidateOutputRoot(ref.CanonicalPath)
	if err != nil {
		return err
	}
	if observed.CanonicalPath != ref.CanonicalPath || observed.Identity != ref.Identity {
		return errors.Join(ErrUnsafePath, ErrWorkspaceUnavailable)
	}
	return nil
}

func validateWorkspaceRootIdentity(ref WorkspaceRef) error {
	if ref.OutputRootIdentity == "" {
		return nil
	}
	return ValidateRootRef(RootRef{CanonicalPath: ref.OutputRoot, Identity: ref.OutputRootIdentity})
}
