package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// InspectionClass is a non-destructive classification returned by Inspect.
type InspectionClass string

const (
	InspectionAvailable                InspectionClass = "available"
	InspectionUnavailableRoot          InspectionClass = "unavailable_root"
	InspectionUnsafePath               InspectionClass = "unsafe_path"
	InspectionCorruptManifest          InspectionClass = "corrupt_manifest"
	InspectionUnknownManifestVersion   InspectionClass = "unknown_manifest_version"
	InspectionMissingLease             InspectionClass = "missing_lease"
	InspectionLeaseContended           InspectionClass = "lease_contention"
	InspectionManifestIndeterminate    InspectionClass = "manifest_commit_indeterminate"
	InspectionPublicationIndeterminate InspectionClass = "publication_indeterminate"
	InspectionNeedsReconciliation      InspectionClass = "needs_reconciliation"
)

// Inspection is intentionally value-oriented. Manifest is populated only
// after a complete validated read; Inspect never removes stale files or
// trusts holder PID metadata.
type Inspection struct {
	Ref                 WorkspaceRef
	WorkspacePath       string
	Manifest            Manifest
	HasManifest         bool
	Classification      InspectionClass
	Classifications     []InspectionClass
	LeaseContended      bool
	CommitIndeterminate bool
}

func (inspection *Inspection) add(classification InspectionClass) {
	for _, existing := range inspection.Classifications {
		if existing == classification {
			return
		}
	}
	inspection.Classifications = append(inspection.Classifications, classification)
	if inspection.Classification == "" {
		inspection.Classification = classification
	}
}

// Inspect performs only validation, bounded reads, and a nonblocking native
// lease acquisition without holder metadata. Holding the lease while reading
// prevents transient atomic-write evidence from being misclassified. Inspect
// never removes candidates or calls a network API.
func Inspect(ref WorkspaceRef) (Inspection, error) {
	inspection := Inspection{Ref: ref}
	if err := ref.validate(); err != nil {
		return inspection, ErrInvalidReference
	}
	root := filepath.Clean(ref.OutputRoot)
	if err := validateDirectoryChain(root, false); err != nil {
		if errors.Is(err, ErrWorkspaceUnavailable) {
			inspection.add(InspectionUnavailableRoot)
		} else {
			inspection.add(InspectionUnsafePath)
		}
		return inspection, nil
	}
	sessionsRoot := filepath.Join(root, SessionsDirectoryName)
	if err := validateDirectoryChain(sessionsRoot, true); err != nil {
		if errors.Is(err, ErrWorkspaceUnavailable) {
			inspection.add(InspectionUnavailableRoot)
		} else {
			inspection.add(InspectionUnsafePath)
		}
		return inspection, nil
	}
	workspacePath, err := ref.Path()
	if err != nil {
		return inspection, ErrInvalidReference
	}
	inspection.WorkspacePath = workspacePath
	if err := validateDirectoryChain(workspacePath, true); err != nil {
		if errors.Is(err, ErrWorkspaceUnavailable) {
			inspection.add(InspectionUnavailableRoot)
		} else {
			inspection.add(InspectionUnsafePath)
		}
		return inspection, nil
	}
	leasePath := filepath.Join(workspacePath, LeaseFileName)
	lease, leaseErr := acquireWorkspaceLease(leasePath, false, false)
	if leaseErr != nil {
		switch {
		case errors.Is(leaseErr, ErrLeaseContended):
			inspection.LeaseContended = true
			inspection.add(InspectionLeaseContended)
		case errors.Is(leaseErr, ErrMissingLease):
			inspection.add(InspectionMissingLease)
			inspection.add(InspectionNeedsReconciliation)
		case errors.Is(leaseErr, ErrWorkspaceUnavailable), errors.Is(leaseErr, ErrLeaseUnavailable):
			inspection.add(InspectionUnavailableRoot)
		default:
			inspection.add(InspectionUnsafePath)
		}
		return inspection, nil
	}
	defer lease.Close()

	manifestPath := filepath.Join(workspacePath, ManifestFileName)
	if info, statErr := os.Lstat(manifestPath); statErr != nil {
		inspection.add(InspectionCorruptManifest)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !ownerOnlyFileAt(manifestPath, info) {
		inspection.add(InspectionUnsafePath)
	} else {
		manifest, readErr := readManifest(manifestPath)
		switch {
		case errors.Is(readErr, ErrUnknownManifestVersion):
			inspection.add(InspectionUnknownManifestVersion)
		case readErr != nil:
			inspection.add(InspectionCorruptManifest)
		default:
			if manifest.SessionID != ref.SessionID {
				inspection.add(InspectionCorruptManifest)
			} else if validateManifestDerivedPaths(root, workspacePath, manifest) != nil {
				inspection.add(InspectionUnsafePath)
			} else {
				inspection.Manifest = manifest
				inspection.HasManifest = true
			}
		}
	}

	evidence, unsafeEvidence := atomicManifestEvidence(workspacePath)
	if evidence {
		inspection.CommitIndeterminate = true
		inspection.add(InspectionManifestIndeterminate)
	}
	if unsafeEvidence {
		inspection.add(InspectionUnsafePath)
	}
	if inspection.HasManifest {
		if inspection.Manifest.Publication == PublicationIndeterminate {
			inspection.add(InspectionPublicationIndeterminate)
		}
		if inspection.Manifest.Status == StatusNeedsReconciliation {
			inspection.add(InspectionNeedsReconciliation)
		}
	}
	if inspection.Classification == "" {
		inspection.add(InspectionAvailable)
	}
	return inspection, nil
}

func hasAtomicManifestEvidence(workspacePath string) bool {
	evidence, _ := atomicManifestEvidence(workspacePath)
	return evidence
}

func atomicManifestEvidence(workspacePath string) (evidence, unsafe bool) {
	entries, err := os.ReadDir(workspacePath)
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".atomic-") {
			evidence = true
			info, statErr := os.Lstat(filepath.Join(workspacePath, entry.Name()))
			if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !ownerOnlyFileAt(filepath.Join(workspacePath, entry.Name()), info) {
				unsafe = true
			}
		}
	}
	return evidence, unsafe
}
