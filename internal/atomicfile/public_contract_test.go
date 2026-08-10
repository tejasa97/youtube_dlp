package atomicfile_test

import (
	"errors"
	"testing"

	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
	"github.com/tejasa97/youtube_dlp/pkg/ytdlp"
)

func TestCommitErrorStructurallyImplementsPublicContract(t *testing.T) {
	err := atomicfile.Write("unused", 0o600, nil)
	var engineError engine.AtomicCommitError
	var publicError ytdlp.AtomicCommitError
	if !errors.As(err, &engineError) || !errors.As(err, &publicError) {
		t.Fatalf("error %T does not implement ytdlp.AtomicCommitError", err)
	}
	if publicError.Committed() || publicError.Indeterminate() {
		t.Fatalf("nil encoder outcome = committed %v, indeterminate %v", publicError.Committed(), publicError.Indeterminate())
	}
}
