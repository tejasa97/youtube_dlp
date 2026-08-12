package fragment

import (
	"io"
	"os"
)

// checkpointCommitError preserves the atomic replacement outcome when the
// committed checkpoint file cannot be ACL-hardened or revalidated.
type checkpointArtifactCommitError struct {
	cause         error
	committed     bool
	indeterminate bool
}

func (err *checkpointArtifactCommitError) Error() string {
	return "fragment checkpoint file commit failed"
}
func (err *checkpointArtifactCommitError) Unwrap() error       { return err.cause }
func (err *checkpointArtifactCommitError) Committed() bool     { return err.committed }
func (err *checkpointArtifactCommitError) Indeterminate() bool { return err.indeterminate }

func writeProtectedCheckpointArtifact(path string, mode os.FileMode, encode func(io.Writer) error) error {
	err := writeProtectedCheckpointArtifactPlatform(path, mode, encode)
	if err == nil {
		return nil
	}
	return err
}
