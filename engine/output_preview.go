package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/engine/value"
	outputtemplate "github.com/tejasa97/youtube_dlp/internal/compat/template"
)

// OutputPreviewRequest contains the already-analyzed metadata and resolved
// final extension needed to render one primary artifact. It performs no
// extraction, network access, or filesystem mutation.
type OutputPreviewRequest struct {
	Template       string
	Metadata       value.Info
	Extension      string
	Filesystem     FilesystemOptions
	AutonumberSize int
}

// ArtifactDeclaration is the exact, sanitized basename an app may reserve.
// It deliberately contains no absolute path or output-root information.
type ArtifactDeclaration struct {
	Kind             ArtifactKind
	Identity         string
	ProposedBasename string
}

// RenderOutputArtifacts renders the primary artifact with the same template
// parser and filename sanitization used by the engine. E1 intentionally
// accepts basename-only templates so the declaration maps one-to-one onto a
// CommitTarget; directory-shaped output is deferred until it has an equally
// safe reservation contract.
func RenderOutputArtifacts(request OutputPreviewRequest) ([]ArtifactDeclaration, error) {
	if err := validateFilenameOptions(request.Filesystem, request.AutonumberSize); err != nil {
		return nil, fmt.Errorf("engine: invalid output preview filename options: %w", err)
	}
	if request.Template == "" {
		request.Template = "%(title)s.%(ext)s"
	}
	if !validPreviewExtension(request.Extension) {
		return nil, errors.New("engine: invalid output preview extension")
	}
	info := value.NewInfo(request.Metadata.Fields().Clone())
	info.Set("ext", value.String(strings.TrimPrefix(request.Extension, ".")))
	root := filepath.Join(string(filepath.Separator), "engine-output-preview")
	resolved, err := outputtemplate.ResolveWithOptions(
		root, request.Template, info, filenameOptionsFor(request.Filesystem, request.AutonumberSize),
	)
	if err != nil {
		return nil, fmt.Errorf("engine: render output preview: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || filepath.Dir(relative) != "." || !validPortableResumeBasename(relative) {
		return nil, errors.New("engine: output preview must resolve to one basename")
	}
	return []ArtifactDeclaration{{Kind: ArtifactKindPrimary, Identity: "primary", ProposedBasename: relative}}, nil
}

func validPreviewExtension(extension string) bool {
	extension = strings.TrimPrefix(extension, ".")
	if extension == "" || len(extension) > 64 || !utf8.ValidString(extension) || strings.ContainsAny(extension, "\x00/\\") {
		return false
	}
	for _, character := range extension {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}
