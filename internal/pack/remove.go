package pack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type RemoveOptions struct {
	ActivatePrevious          bool
	ApprovePermissionIncrease bool
	// ValidateReplacement binds an optional replacement pack to the product's
	// runtime contract before removal publishes it as active.
	ValidateReplacement func(Verified) error
}

type RemovalReceipt struct {
	RemovedVersion string
	Replacement    string
	Review         PermissionReview
	State          State
	Transition     Transition
}

// Remove atomically publishes state that no longer references version before
// unlinking any payload. A pending-removal journal makes a crash between those
// steps recoverable by the next locked operation. Removal intentionally does
// not require the target to be unexpired or untampered: quarantine-and-unlink
// never follows links, allowing a compromised plugin to be removed safely.
func Remove(ctx context.Context, root, name, version string, policy VerifyPolicy, options RemoveOptions) (RemovalReceipt, error) {
	var receipt RemovalReceipt
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if !secureInstallPlatform() {
		return receipt, ErrPlatformSecurity
	}
	if !validName(name) {
		return receipt, ErrInvalidManifest
	}
	if _, err := parseVersion(version); err != nil {
		return receipt, err
	}
	if policy.Now.IsZero() {
		return receipt, fmt.Errorf("%w: removal time is required", ErrInvalidManifest)
	}
	if err := validateExistingRoot(root); err != nil {
		return receipt, err
	}
	packRoot := filepath.Join(root, name)
	if err := validateSecureDirectory(packRoot); err != nil {
		return receipt, err
	}
	unlock, err := acquireLock(packRoot)
	if err != nil {
		return receipt, err
	}
	defer unlock()
	state, err := loadState(packRoot, name)
	if err != nil {
		return receipt, err
	}
	versionsRoot := filepath.Join(packRoot, "versions")
	if err := validateSecureDirectory(versionsRoot); err != nil {
		return receipt, err
	}
	state, err = recoverPendingRemovals(packRoot, versionsRoot, state)
	if err != nil {
		return receipt, err
	}
	removed := recordFor(state, version)
	if removed == nil {
		return receipt, ErrNotInstalled
	}
	if err := validateRemovalTarget(filepath.Join(versionsRoot, version)); err != nil {
		return receipt, err
	}
	var replacement string
	review := PermissionReview{}
	if state.Current == version && options.ActivatePrevious && state.Previous != "" {
		replacement = state.Previous
		replacementRecord := recordFor(state, replacement)
		if replacementRecord == nil {
			return receipt, ErrCorruptInstall
		}
		replacementPath := filepath.Join(versionsRoot, replacement)
		rollbackPolicy := policy
		rollbackPolicy.CurrentVersion = ""
		verified, verifyErr := verifyInstalled(replacementPath, rollbackPolicy)
		if verifyErr != nil || verified.ArchiveSHA256 != replacementRecord.ArchiveSHA256 || verified.ManifestSHA256 != replacementRecord.ManifestSHA256 {
			if verifyErr != nil {
				return receipt, verifyErr
			}
			return receipt, ErrCorruptInstall
		}
		if options.ValidateReplacement != nil {
			if err := options.ValidateReplacement(verified); err != nil {
				return receipt, err
			}
		}
		review = ReviewPermissions(removed.Permissions, replacementRecord.Permissions)
		if review.Increase() && !options.ApprovePermissionIncrease {
			return RemovalReceipt{RemovedVersion: version, Replacement: replacement, Review: review, State: state}, ErrPermissionReview
		}
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	state.Records = removeRecord(state.Records, version)
	switch {
	case state.Current == version:
		state.Current = replacement
		state.Previous = ""
	case state.Previous == version:
		state.Previous = ""
	}
	state.PendingRemovals = append(state.PendingRemovals, version)
	transition := Transition{From: version, To: state.Current, Reason: "remove", At: policy.Now.UTC().Format(time.RFC3339)}
	state.LastTransition = transition
	if err := writeState(packRoot, state); err != nil {
		return receipt, err
	}
	// The state publication above is the transaction commit point. Finish the
	// bounded local cleanup even if cancellation arrives now; otherwise the
	// pending journal is recovered by the next operation.
	if err := removeVersionDurably(versionsRoot, version); err != nil {
		return RemovalReceipt{RemovedVersion: version, Replacement: replacement, Review: review, State: state, Transition: transition}, err
	}
	state.PendingRemovals = nil
	if err := writeState(packRoot, state); err != nil {
		return RemovalReceipt{RemovedVersion: version, Replacement: replacement, Review: review, State: state, Transition: transition}, err
	}
	return RemovalReceipt{RemovedVersion: version, Replacement: replacement, Review: review, State: state, Transition: transition}, nil
}

func validateRemovalTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrCorruptInstall
	}
	if err != nil {
		return fmt.Errorf("%w: inspect removal target", ErrIO)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !secureOwnership(info) {
		return ErrUnsafePath
	}
	return nil
}

func removeRecord(records []InstalledRecord, version string) []InstalledRecord {
	result := make([]InstalledRecord, 0, len(records)-1)
	for _, record := range records {
		if record.Version != version {
			result = append(result, record)
		}
	}
	return result
}
