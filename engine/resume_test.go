package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/engine/value"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/session"
)

const testResumeSessionID = "0123456789abcdef0123456789abcdef"

func TestSessionRequestValidationPrecedesRuntimeEffects(t *testing.T) {
	t.Parallel()
	var transportConstructed bool
	cacheDir := t.TempDir()
	cacheSentinel := filepath.Join(cacheDir, "must-survive")
	if err := os.WriteFile(cacheSentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{transportFactory: func(network.Config) (*network.Client, error) {
		transportConstructed = true
		return nil, errors.New("transport must not be constructed")
	}}
	_, err := client.Run(context.Background(), Request{
		URL:            "https://example.invalid/watch",
		OutputDir:      t.TempDir(),
		CacheDir:       cacheDir,
		RemoveCacheDir: true,
		Overwrite:      true,
		Filesystem: FilesystemOptions{Resume: ResumeOptions{
			SessionID:          testResumeSessionID,
			PublicationArbiter: NewPublicationArbiter(),
			CommitTargets: []CommitTarget{{
				Kind: ArtifactKindPrimary, Identity: "primary", Basename: "output.mp4",
			}},
		}},
	})
	if !IsCategory(err, ErrorInvalidInput) || !strings.Contains(err.Error(), "overwrite=false") {
		t.Fatalf("Run() error = %v, want preflight overwrite validation", err)
	}
	if transportConstructed {
		t.Fatal("Run() constructed a transport after invalid session preflight")
	}
	if _, err := os.Stat(cacheSentinel); err != nil {
		t.Fatalf("Run() mutated cache before invalid session preflight: %v", err)
	}
}

func TestEmptyResumeSessionPreservesValidationBehavior(t *testing.T) {
	t.Parallel()
	if err := validateRequestOptions(Request{}); err != nil {
		t.Fatalf("empty resume session changed legacy validation: %v", err)
	}
	for name, resume := range map[string]ResumeOptions{
		"arbiter without session": {PublicationArbiter: NewPublicationArbiter()},
		"targets without session": {CommitTargets: []CommitTarget{}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRequestOptions(Request{Filesystem: FilesystemOptions{Resume: resume}}); !errors.Is(err, errInvalidRequestOptions) {
				t.Fatalf("partial resume validation error = %v, want invalid request options", err)
			}
		})
	}
}

func TestValidateOutputRootCanonicalizesRelativePathWithoutCreatingIt(t *testing.T) {
	t.Parallel()
	root, err := ValidateOutputRoot(".")
	if err != nil || !filepath.IsAbs(root.CanonicalPath) || root.Identity == "" {
		t.Fatalf("ValidateOutputRoot(.) = %#v, %v", root, err)
	}
	if _, err := ValidateOutputRoot(""); err == nil {
		t.Fatal("ValidateOutputRoot accepted an empty path")
	}
}

func TestValidateOutputRootRejectsSymlink(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	parent := t.TempDir()
	link := filepath.Join(parent, "root-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ValidateOutputRoot(link); err == nil {
		t.Fatal("ValidateOutputRoot accepted a symlink root")
	}
}

func TestSessionRequestRequiresCompleteSafeCommitDeclaration(t *testing.T) {
	t.Parallel()
	valid := ResumeOptions{
		SessionID:          testResumeSessionID,
		PublicationArbiter: NewPublicationArbiter(),
		CommitTargets:      []CommitTarget{{Kind: ArtifactKindPrimary, Identity: "primary", Basename: "output.mp4"}},
	}
	for _, test := range []struct {
		name   string
		mutate func(*ResumeOptions)
	}{
		{name: "missing arbiter", mutate: func(options *ResumeOptions) { options.PublicationArbiter = nil }},
		{name: "missing targets", mutate: func(options *ResumeOptions) { options.CommitTargets = nil }},
		{name: "unsafe basename", mutate: func(options *ResumeOptions) { options.CommitTargets[0].Basename = "nested/output.mp4" }},
		{name: "portable reserved basename", mutate: func(options *ResumeOptions) { options.CommitTargets[0].Basename = "CON.mp4" }},
		{name: "unicode control basename", mutate: func(options *ResumeOptions) { options.CommitTargets[0].Basename = "report\u0085.mp4" }},
		{name: "duplicate basename", mutate: func(options *ResumeOptions) {
			options.CommitTargets = append(options.CommitTargets, CommitTarget{Kind: ArtifactKind("sidecar"), Identity: "sidecar", Basename: "output.mp4"})
		}},
		{name: "duplicate identity", mutate: func(options *ResumeOptions) {
			options.CommitTargets = append(options.CommitTargets, options.CommitTargets[0])
		}},
		{name: "invalid session ID", mutate: func(options *ResumeOptions) { options.SessionID = "not-a-session" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			options.CommitTargets = append([]CommitTarget(nil), valid.CommitTargets...)
			test.mutate(&options)
			err := validateRequestOptions(Request{OutputDir: t.TempDir(), Filesystem: FilesystemOptions{Resume: options}})
			if !errors.Is(err, errInvalidRequestOptions) {
				t.Fatalf("validateRequestOptions() error = %v, want invalid request options", err)
			}
		})
	}
}

func TestRenderOutputArtifactsUsesSanitizedBasenameOnly(t *testing.T) {
	t.Parallel()
	metadata := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("a b")},
		value.Field{Key: "ext", Value: value.String("wrong")},
	))
	declarations, err := RenderOutputArtifacts(OutputPreviewRequest{
		Template: "%(title)s.%(ext)s", Metadata: metadata, Extension: "mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 1 || declarations[0].Kind != ArtifactKindPrimary || declarations[0].Identity != "primary" ||
		!strings.HasSuffix(declarations[0].ProposedBasename, ".mp4") || !validResumeBasename(declarations[0].ProposedBasename) {
		t.Fatalf("declarations = %#v", declarations)
	}
	if _, err := RenderOutputArtifacts(OutputPreviewRequest{
		Template: "nested/%(title)s.%(ext)s", Metadata: metadata, Extension: "mp4",
	}); err == nil {
		t.Fatal("directory-shaped preview template succeeded")
	}
}

func TestRenderOutputArtifactsMatchesRuntimeFilenameOptions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		template       string
		metadata       value.Info
		filesystem     FilesystemOptions
		autonumberSize int
	}{
		{
			name:     "defaults",
			template: "%(title)s-%(ext)s",
			metadata: value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("plain title")})),
		},
		{
			name:       "restrict filenames",
			template:   "%(title)s-%(ext)s",
			metadata:   value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("a b")})),
			filesystem: FilesystemOptions{RestrictFilenames: true},
		},
		{
			name:       "windows filenames",
			template:   "%(title)s-%(ext)s",
			metadata:   value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("a?b")})),
			filesystem: FilesystemOptions{WindowsFilenames: true},
		},
		{
			name:       "trim",
			template:   "%(title)s.%(ext)s",
			metadata:   value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("long filename")})),
			filesystem: FilesystemOptions{TrimFilenames: 7},
		},
		{
			name:       "NA placeholder",
			template:   "%(missing)s-%(ext)s",
			metadata:   value.NewInfo(value.NewObject()),
			filesystem: FilesystemOptions{OutputNaPlaceholder: "missing-value"},
		},
		{
			name:           "autonumber width",
			template:       "%(autonumber)s-%(ext)s",
			metadata:       value.NewInfo(value.NewObject(value.Field{Key: "autonumber", Value: value.Int(7)})),
			autonumberSize: 4,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			const extension = "mp4"
			root := t.TempDir()
			operation := operation{request: Request{
				Filesystem:     test.filesystem,
				AutonumberSize: test.autonumberSize,
			}}
			runtimeInfo := value.NewInfo(test.metadata.Fields().Clone())
			runtimeInfo.Set("ext", value.String(extension))
			runtimePath, err := operation.resolveOutputPath(root, test.template, runtimeInfo)
			if err != nil {
				t.Fatalf("runtime filename: %v", err)
			}
			preview, err := RenderOutputArtifacts(OutputPreviewRequest{
				Template: test.template, Metadata: test.metadata, Extension: extension,
				Filesystem: test.filesystem, AutonumberSize: test.autonumberSize,
			})
			if err != nil {
				t.Fatalf("preview filename: %v", err)
			}
			if len(preview) != 1 || preview[0].ProposedBasename != filepath.Base(runtimePath) {
				t.Fatalf("preview=%#v runtime=%q", preview, runtimePath)
			}
		})
	}
}

func TestRenderOutputArtifactsRejectsRuntimeFilenameOptions(t *testing.T) {
	t.Parallel()
	metadata := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("title")}))
	cases := []struct {
		name           string
		filesystem     FilesystemOptions
		autonumberSize int
	}{
		{name: "negative autonumber size", autonumberSize: -1},
		{name: "oversized autonumber size", autonumberSize: maxFilenameAutonumberSize + 1},
		{name: "negative trim", filesystem: FilesystemOptions{TrimFilenames: -1}},
		{name: "oversized trim", filesystem: FilesystemOptions{TrimFilenames: maxFilenameTrim + 1}},
		{name: "oversized NA placeholder", filesystem: FilesystemOptions{OutputNaPlaceholder: strings.Repeat("x", maxOutputNaPlaceholderBytes+1)}},
		{name: "C0 placeholder control", filesystem: FilesystemOptions{OutputNaPlaceholder: "bad\tvalue"}},
		{name: "C1 placeholder control", filesystem: FilesystemOptions{OutputNaPlaceholder: "bad\u0085value"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := OutputPreviewRequest{
				Template:       "%(title)s.%(ext)s",
				Metadata:       metadata,
				Extension:      "mp4",
				Filesystem:     test.filesystem,
				AutonumberSize: test.autonumberSize,
			}
			if _, err := RenderOutputArtifacts(request); err == nil {
				t.Fatal("preview accepted filename controls rejected by runtime")
			}
			if err := validateRequestOptions(Request{Filesystem: test.filesystem, AutonumberSize: test.autonumberSize}); !errors.Is(err, errInvalidRequestOptions) {
				t.Fatalf("runtime validation error = %v, want invalid request options", err)
			}
		})
	}
}

func TestCloneResumeTargetsAllowsCallerMutationAfterRunSnapshot(t *testing.T) {
	t.Parallel()
	original := ResumeOptions{CommitTargets: []CommitTarget{{
		Kind: ArtifactKindPrimary, Identity: "primary", Basename: "first.mp4",
	}}}
	snapshot := cloneResumeOptions(original)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for index := 0; index < 1000; index++ {
			original.CommitTargets[0].Basename = "caller-mutated.mp4"
		}
	}()
	for index := 0; index < 1000; index++ {
		if snapshot.CommitTargets[0].Basename != "first.mp4" {
			t.Fatalf("snapshot changed after caller mutation: %#v", snapshot)
		}
	}
	wait.Wait()
	empty := cloneResumeOptions(ResumeOptions{CommitTargets: []CommitTarget{}})
	if empty.CommitTargets == nil {
		t.Fatal("cloneResumeOptions collapsed a supplied empty target declaration")
	}
}

func TestResumeFacadeIsPathAndCredentialFree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace, err := session.Create(session.CreateOptions{
		OutputRoot: root,
		Source: session.SourceIntent{
			Provider: "fixture", ID: "video", Kind: "video", CanonicalURL: "https://example.invalid/watch",
		},
		Output: session.OutputIntent{Container: "mp4", Extension: "mp4", PlanIdentity: "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := workspace.Ref().SessionID
	workspacePath := workspace.Path()
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := InspectResumeState(context.Background(), rootRef, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.HasManifest || summary.Classification != ResumeInspectionClass(session.InspectionAvailable) {
		t.Fatalf("summary = %#v", summary)
	}
	public := summary.SessionID + string(summary.Phase) + string(summary.Status) + string(summary.Publication)
	if strings.Contains(public, root) || strings.Contains(public, "example.invalid") {
		t.Fatalf("resume summary leaked private data: %#v", summary)
	}
	handle, err := PrepareResumeDiscard(context.Background(), rootRef, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := handle.Discard(context.Background())
	if err != nil || !result.Discarded || result.Disposition != ResumeDiscarded || result.SessionID != sessionID {
		t.Fatalf("Discard() result=%#v err=%v", result, err)
	}
	if _, err := os.Stat(workspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains after discard: %v", err)
	}
}

func TestResumeDiscardReportsReconciliationDisposition(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace, err := session.Create(session.CreateOptions{
		OutputRoot: root,
		Source:     session.SourceIntent{Provider: "fixture", ID: "video", Kind: "video", CanonicalURL: "https://example.invalid/watch"},
		Output:     session.OutputIntent{Container: "mp4", Extension: "mp4", PlanIdentity: "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := workspace.Ref().SessionID
	workspacePath := workspace.Path()
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := PrepareResumeDiscard(context.Background(), rootRef, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(workspacePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(workspacePath, "replacement.txt")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, discardErr := handle.Discard(context.Background())
	if discardErr == nil || result.Disposition != ResumeDiscardReconciliationRequired || result.Discarded {
		t.Fatalf("Discard() result=%#v err=%v, want reconciliation disposition", result, discardErr)
	}
	if _, err := os.Stat(replacement); err != nil {
		t.Fatalf("replacement evidence was removed: %v", err)
	}
}

func TestResumeFacadeRejectsReplacedOutputRoot(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "output")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	workspace, err := session.Create(session.CreateOptions{
		OutputRoot: root,
		Source:     session.SourceIntent{Provider: "fixture", ID: "video", Kind: "video", CanonicalURL: "https://example.invalid/watch"},
		Output:     session.OutputIntent{Container: "mp4", Extension: "mp4", PlanIdentity: "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := workspace.Ref().SessionID
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(parent, "replaced-output")
	if err := os.Rename(root, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = InspectResumeState(context.Background(), rootRef, sessionID)
	if err == nil {
		t.Fatal("replaced-root inspection unexpectedly succeeded")
	}
	if _, err := PrepareResumeDiscard(context.Background(), rootRef, sessionID); err == nil {
		t.Fatal("discard accepted a replaced output root")
	}
}

func TestResumeDiscardHandleConcurrentCloseAndDiscard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace, err := session.Create(session.CreateOptions{
		OutputRoot: root,
		Source:     session.SourceIntent{Provider: "fixture", ID: "video", Kind: "video", CanonicalURL: "https://example.invalid/watch"},
		Output:     session.OutputIntent{Container: "mp4", Extension: "mp4", PlanIdentity: "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := workspace.Ref().SessionID
	workspacePath := workspace.Path()
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := PrepareResumeDiscard(context.Background(), rootRef, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var discardErr error
	go func() {
		defer wait.Done()
		<-start
		_, discardErr = handle.Discard(context.Background())
	}()
	var closeErr error
	go func() {
		defer wait.Done()
		<-start
		closeErr = handle.Close()
	}()
	close(start)
	wait.Wait()
	if discardErr != nil && closeErr != nil {
		t.Fatalf("both discard and close failed: discard=%v close=%v", discardErr, closeErr)
	}
	if discardErr == nil {
		if _, err := os.Stat(workspacePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("workspace remains after successful concurrent discard: %v", err)
		}
	}
}

func TestCollectResumeOrphansPreservesLiveSessions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	create := func() string {
		workspace, err := session.Create(session.CreateOptions{
			OutputRoot: root,
			Source:     session.SourceIntent{Provider: "fixture", ID: "video", Kind: "video", CanonicalURL: "https://example.invalid/watch"},
			Output:     session.OutputIntent{Container: "mp4", Extension: "mp4", PlanIdentity: "fixture"},
		})
		if err != nil {
			t.Fatal(err)
		}
		id := workspace.Ref().SessionID
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		return id
	}
	orphan, live := create(), create()
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CollectResumeOrphans(context.Background(), rootRef, map[string]struct{}{live: {}}, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CollectedSessionIDs) != 1 || result.CollectedSessionIDs[0] != orphan {
		t.Fatalf("collection result = %#v, want only %q", result, orphan)
	}
	if len(result.CleanupPendingSessionIDs) != 0 || len(result.ReconciliationSessionIDs) != 0 {
		t.Fatalf("collection reported unexpected unresolved sessions: %#v", result)
	}
	if summary, err := InspectResumeState(context.Background(), rootRef, live); err != nil || !summary.HasManifest {
		t.Fatalf("live summary=%#v err=%v", summary, err)
	}
}
