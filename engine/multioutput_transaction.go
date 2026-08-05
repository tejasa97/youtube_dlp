package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

var errDestinationCollision = errors.New("output destination collision")

const maxDestinationSuffixAttempts = 16

type destinationSlot struct {
	destination string
	backupPath  string
	published   bool
	removed     bool
}

// mediaTransaction tracks one entry download attempt: media destinations receive
// overwrite backups immediately before publication; sidecar paths are protected
// before write via protectPath.
type mediaTransaction struct {
	destinations    []destinationSlot
	artifacts       []destinationSlot
	published       map[string]struct{}
	backupsAcquired bool
}

func newMediaTransaction() *mediaTransaction {
	return &mediaTransaction{published: make(map[string]struct{})}
}

func portablePathKey(path string) string {
	if path == "" || path == "-" {
		return ""
	}
	path = filepath.Clean(path)
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")
	for index, part := range parts {
		parts[index] = strings.ToLower(part)
	}
	return strings.Join(parts, "/")
}

func (operation *operation) resolveOutputPlanDestinations(
	info value.Info,
	plans []mediaformat.OutputPlan,
) ([]string, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	prefs := operation.mergeOutputPreferences()
	multiOutput := len(plans) > 1
	rendered := make([]string, len(plans))
	for index, plan := range plans {
		outputInfo := operation.planDestinationOutputInfo(info, plan)
		destination, err := operation.renderFilenameBase(outputInfo)
		if err != nil {
			return nil, err
		}
		if multiOutput && len(plan.Tracks) > 1 {
			destination, err = alignMergedDestinationExtension(destination, plan, prefs, operation.request)
			if err != nil {
				return nil, err
			}
		}
		rendered[index] = thumbnailEmbeddingDestination(
			operation.request, plan.Tracks, destination, outputInfo,
		)
		rendered[index] = applyHLSDiscontinuityDestinationSuffix(rendered[index], outputInfo)
	}
	if len(plans) == 1 {
		return rendered, nil
	}
	return disambiguatePlanDestinations(rendered, plans)
}

// alignMergedDestinationExtension mirrors pinned yt-dlp correct_ext for merged outputs.
func alignMergedDestinationExtension(
	destination string,
	plan mediaformat.OutputPlan,
	preferences []string,
	request Request,
) (string, error) {
	if destination == "" || destination == "-" {
		return destination, nil
	}
	oldExt, err := planDestinationExtension(plan, preferences)
	if err != nil {
		return "", err
	}
	newExt := thumbnailEmbeddingOutputExtension(request, plan.Tracks, oldExt)
	filenameRealExt := strings.TrimPrefix(filepath.Ext(destination), ".")
	stem := destination
	if filenameRealExt == oldExt || filenameRealExt == newExt {
		if ext := filepath.Ext(destination); ext != "" {
			stem = strings.TrimSuffix(destination, ext)
		}
	}
	return stem + "." + newExt, nil
}

func disambiguatePlanDestinations(
	rendered []string,
	plans []mediaformat.OutputPlan,
) ([]string, error) {
	if len(rendered) != len(plans) {
		return rendered, nil
	}
	result := append([]string(nil), rendered...)
	for attempt := 0; attempt < maxDestinationSuffixAttempts; attempt++ {
		if !hasPortableDestinationCollision(result) {
			return result, nil
		}
		counts := portableDestinationCounts(result)
		for index, path := range result {
			if counts[portablePathKey(path)] > 1 {
				result[index] = mechanicalDestinationSuffix(path, plans[index], index+1)
			}
		}
	}
	return nil, fmt.Errorf("%w: unresolved destination collision after suffixing", errDestinationCollision)
}

func hasPortableDestinationCollision(paths []string) bool {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		key := portablePathKey(path)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			return true
		}
		seen[key] = struct{}{}
	}
	return false
}

func portableDestinationCounts(paths []string) map[string]int {
	counts := make(map[string]int, len(paths))
	for _, path := range paths {
		key := portablePathKey(path)
		if key == "" {
			continue
		}
		counts[key]++
	}
	return counts
}

func mechanicalDestinationSuffix(
	base string,
	plan mediaformat.OutputPlan,
	planIndex int,
) string {
	if base == "" || base == "-" {
		return base
	}
	ext := filepath.Ext(base)
	dir := filepath.Dir(base)
	stem := strings.TrimSuffix(filepath.Base(base), ext)
	suffix := plan.DestinationSuffix(planIndex)
	return filepath.Join(dir, stem+".f"+suffix+ext)
}

func inspectDestinationPath(path string, overwrite bool) error {
	path = filepath.Clean(path)
	if path == "" || path == "-" {
		return nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return downloader.ErrUnsafeDestination
	}
	if !overwrite {
		return fmt.Errorf("%w: %s", downloader.ErrDestinationExists, path)
	}
	return nil
}

func backupExistingRegularFile(path string, overwrite bool) (string, error) {
	path = filepath.Clean(path)
	if path == "" || path == "-" {
		return "", nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", downloader.ErrUnsafeDestination
	}
	if !overwrite {
		return "", fmt.Errorf("%w: %s", downloader.ErrDestinationExists, path)
	}
	backup, err := reserveMediaTransactionBackup(path)
	if err != nil {
		return "", err
	}
	if err := os.Rename(path, backup); err != nil {
		_ = os.Remove(backup)
		return "", fmt.Errorf("backup %s: %w", path, err)
	}
	return backup, nil
}

func (operation *operation) preflightMediaDestinations(destinations []string) error {
	policies := make([]destinationPolicy, len(destinations))
	for index, destination := range destinations {
		policies[index] = destinationPolicy{path: destination, overwrite: operation.request.Overwrite}
	}
	return operation.preflightDestinationPolicies(policies)
}

type destinationPolicy struct {
	path      string
	overwrite bool
}

func (operation *operation) preflightDestinationPolicies(policies []destinationPolicy) error {
	destinations := make([]string, len(policies))
	for index, policy := range policies {
		destinations[index] = policy.path
	}
	if err := validatePortableDestinationSet(destinations); err != nil {
		return err
	}
	for _, policy := range policies {
		if err := inspectDestinationPath(policy.path, policy.overwrite); err != nil {
			return err
		}
	}
	return nil
}

func (transaction *mediaTransaction) hasPath(path string) bool {
	path = filepath.Clean(path)
	for _, slot := range transaction.destinations {
		if slot.destination == path {
			return true
		}
	}
	for _, slot := range transaction.artifacts {
		if slot.destination == path {
			return true
		}
	}
	return false
}

func copyExistingToBackup(path string) (string, error) {
	path = filepath.Clean(path)
	if path == "" || path == "-" {
		return "", nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", downloader.ErrUnsafeDestination
	}
	backup, err := reserveMediaTransactionBackup(path)
	if err != nil {
		return "", err
	}
	if err := copyMediaTransactionFile(path, backup); err != nil {
		_ = os.Remove(backup)
		return "", err
	}
	return backup, nil
}

func (transaction *mediaTransaction) protectAppendPath(path string) error {
	path = filepath.Clean(path)
	if path == "" || path == "-" {
		return nil
	}
	if transaction.hasPath(path) {
		return nil
	}
	backup, err := copyExistingToBackup(path)
	if err != nil {
		return err
	}
	transaction.artifacts = append(transaction.artifacts, destinationSlot{
		destination: path,
		backupPath:  backup,
	})
	return nil
}

func (transaction *mediaTransaction) snapshotRemovedPath(path string) error {
	path = filepath.Clean(path)
	if path == "" || path == "-" {
		return nil
	}
	for index, slot := range transaction.artifacts {
		if slot.destination != path {
			continue
		}
		if slot.backupPath == "" {
			backup, err := copyExistingToBackup(path)
			if err != nil {
				return err
			}
			transaction.artifacts[index].backupPath = backup
		}
		transaction.artifacts[index].removed = true
		return nil
	}
	backup, err := copyExistingToBackup(path)
	if err != nil {
		return err
	}
	transaction.artifacts = append(transaction.artifacts, destinationSlot{
		destination: path,
		backupPath:  backup,
		removed:     true,
	})
	return nil
}

func (transaction *mediaTransaction) protectPath(path string, overwrite bool) error {
	path = filepath.Clean(path)
	if path == "" || path == "-" {
		return nil
	}
	if transaction.hasPath(path) {
		return nil
	}
	backup, err := backupExistingRegularFile(path, overwrite)
	if err != nil {
		return err
	}
	transaction.artifacts = append(transaction.artifacts, destinationSlot{
		destination: path,
		backupPath:  backup,
	})
	return nil
}

func (transaction *mediaTransaction) acquireDestinationBackups(destinations []string, overwrite bool) error {
	for _, destination := range destinations {
		destination = filepath.Clean(destination)
		if destination == "" || destination == "-" {
			continue
		}
		if transaction.hasPath(destination) {
			continue
		}
		backup, err := backupExistingRegularFile(destination, overwrite)
		if err != nil {
			if rollbackErr := transaction.rollback(); rollbackErr != nil {
				return fmt.Errorf("%w (rollback failed: %v)", err, rollbackErr)
			}
			return err
		}
		transaction.destinations = append(transaction.destinations, destinationSlot{
			destination: destination,
			backupPath:  backup,
		})
	}
	transaction.backupsAcquired = true
	return nil
}

func validatePortableDestinationSet(destinations []string) error {
	seen := make(map[string]string, len(destinations))
	for _, destination := range destinations {
		destination = filepath.Clean(destination)
		if destination == "" || destination == "-" {
			continue
		}
		key := portablePathKey(destination)
		if prior, exists := seen[key]; exists {
			return fmt.Errorf("%w: %s (collides with %s)", errDestinationCollision, destination, prior)
		}
		seen[key] = destination
	}
	return nil
}

func reserveMediaTransactionBackup(destination string) (string, error) {
	dir := filepath.Dir(destination)
	base := filepath.Base(destination)
	for attempt := 0; attempt < 16; attempt++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s.ytdlp-trx-%d-%d", base, time.Now().UnixNano(), attempt))
		if _, err := os.Stat(candidate); err != nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("failed to reserve transaction backup for %s", destination)
}

func (transaction *mediaTransaction) markPublished(path string) {
	path = filepath.Clean(path)
	transaction.published[path] = struct{}{}
	for index := range transaction.destinations {
		if transaction.destinations[index].destination == path {
			transaction.destinations[index].published = true
			return
		}
	}
}

func rollbackDestinationSlot(slot destinationSlot) error {
	if slot.backupPath == "" {
		if slot.published {
			if err := os.Remove(slot.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", slot.destination, err)
			}
		}
		return nil
	}
	if slot.published {
		if err := os.Remove(slot.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", slot.destination, err)
		}
	}
	if err := restoreMediaDestination(slot.destination, slot.backupPath); err != nil {
		return err
	}
	return nil
}

func rollbackArtifactSlot(slot destinationSlot) error {
	if slot.removed {
		if slot.backupPath == "" {
			return nil
		}
		return restoreMediaDestination(slot.destination, slot.backupPath)
	}
	if slot.backupPath == "" {
		if err := os.Remove(slot.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", slot.destination, err)
		}
		return nil
	}
	if err := os.Remove(slot.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", slot.destination, err)
	}
	return restoreMediaDestination(slot.destination, slot.backupPath)
}

func (transaction *mediaTransaction) rollbackArtifacts() error {
	var rollbackErrs []error
	for _, slot := range transaction.artifacts {
		if err := rollbackArtifactSlot(slot); err != nil {
			rollbackErrs = append(rollbackErrs, err)
		}
	}
	transaction.artifacts = nil
	if len(rollbackErrs) > 0 {
		return errors.Join(rollbackErrs...)
	}
	return nil
}

func (transaction *mediaTransaction) rollback() error {
	var rollbackErrs []error
	if err := transaction.rollbackArtifacts(); err != nil {
		rollbackErrs = append(rollbackErrs, err)
	}
	for _, slot := range transaction.destinations {
		if err := rollbackDestinationSlot(slot); err != nil {
			rollbackErrs = append(rollbackErrs, err)
		}
	}
	transaction.destinations = nil
	transaction.backupsAcquired = false
	if len(rollbackErrs) > 0 {
		return errors.Join(rollbackErrs...)
	}
	return nil
}

func (transaction *mediaTransaction) commitDestinations() error {
	var commitErrs []error
	for index, slot := range transaction.destinations {
		if slot.backupPath == "" {
			continue
		}
		if err := os.Remove(slot.backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			commitErrs = append(commitErrs, fmt.Errorf("remove backup %s: %w", slot.backupPath, err))
			continue
		}
		transaction.destinations[index].backupPath = ""
	}
	if len(commitErrs) > 0 {
		return errors.Join(commitErrs...)
	}
	return nil
}

func (transaction *mediaTransaction) commitArtifacts() error {
	var commitErrs []error
	for index, slot := range transaction.artifacts {
		if slot.backupPath == "" {
			continue
		}
		if err := os.Remove(slot.backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			commitErrs = append(commitErrs, fmt.Errorf("remove backup %s: %w", slot.backupPath, err))
			continue
		}
		transaction.artifacts[index].backupPath = ""
	}
	if len(commitErrs) > 0 {
		return errors.Join(commitErrs...)
	}
	return nil
}

func (transaction *mediaTransaction) finalize() {
	transaction.artifacts = nil
	transaction.destinations = nil
	transaction.published = nil
	transaction.backupsAcquired = false
}

func (transaction *mediaTransaction) commit() error {
	var commitErrs []error
	if err := transaction.commitDestinations(); err != nil {
		commitErrs = append(commitErrs, err)
	}
	if err := transaction.commitArtifacts(); err != nil {
		commitErrs = append(commitErrs, err)
	}
	if len(commitErrs) > 0 {
		return errors.Join(commitErrs...)
	}
	return nil
}

func restoreMediaDestination(destination, backup string) error {
	if _, err := os.Lstat(destination); err == nil {
		if runtime.GOOS == "windows" {
			if err := os.Remove(destination); err != nil {
				return fmt.Errorf("restore %s: %w", destination, err)
			}
		} else {
			_ = os.Remove(destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("restore %s: %w", destination, err)
	}
	if err := os.Rename(backup, destination); err == nil {
		return nil
	}
	return copyMediaTransactionFile(backup, destination)
}

func copyMediaTransactionFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("copy %s: %w", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("copy %s: %w", destination, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", source, destination, err)
	}
	return out.Close()
}

func rollbackTransactionResult(transaction *mediaTransaction, err error) (Result, error) {
	return Result{}, rollbackTransaction(transaction, err)
}

func rollbackTransaction(transaction *mediaTransaction, primary error) error {
	if transaction == nil {
		return primary
	}
	var rollbackErr error
	if transaction.backupsAcquired {
		rollbackErr = transaction.rollback()
	} else {
		rollbackErr = transaction.rollbackArtifacts()
	}
	if rollbackErr != nil {
		return fmt.Errorf("%w (rollback failed: %v)", primary, rollbackErr)
	}
	return primary
}

func rollbackMediaResult(transaction *mediaTransaction, err error) (Result, error) {
	return Result{}, rollbackMediaTransaction(transaction, err)
}

func rollbackArtifactResult(transaction *mediaTransaction, err error) (Result, error) {
	return Result{}, rollbackArtifactTransaction(transaction, err)
}

func rollbackArtifactTransaction(transaction *mediaTransaction, primary error) error {
	if transaction == nil {
		return primary
	}
	if rollbackErr := transaction.rollbackArtifacts(); rollbackErr != nil {
		return fmt.Errorf("%w (rollback failed: %v)", primary, rollbackErr)
	}
	return primary
}

func rollbackMediaTransaction(transaction *mediaTransaction, primary error) error {
	if transaction == nil {
		return primary
	}
	if rollbackErr := transaction.rollback(); rollbackErr != nil {
		return fmt.Errorf("%w (rollback failed: %v)", primary, rollbackErr)
	}
	return primary
}

type mediaTransactionContextKey struct{}

func withMediaTransaction(ctx context.Context, transaction *mediaTransaction) context.Context {
	if transaction == nil {
		return ctx
	}
	return context.WithValue(ctx, mediaTransactionContextKey{}, transaction)
}

func mediaTransactionFromContext(ctx context.Context) *mediaTransaction {
	transaction, _ := ctx.Value(mediaTransactionContextKey{}).(*mediaTransaction)
	return transaction
}

func (operation *operation) protectTransactionAppendPath(ctx context.Context, path string) error {
	transaction := mediaTransactionFromContext(ctx)
	if transaction == nil {
		return nil
	}
	return transaction.protectAppendPath(path)
}

func (operation *operation) snapshotTransactionRemovedPath(ctx context.Context, path string) error {
	transaction := mediaTransactionFromContext(ctx)
	if transaction == nil {
		return nil
	}
	return transaction.snapshotRemovedPath(path)
}

func (operation *operation) protectTransactionPath(ctx context.Context, path string) error {
	transaction := mediaTransactionFromContext(ctx)
	if transaction == nil {
		return nil
	}
	return transaction.protectPath(path, operation.request.Overwrite)
}

func trackTransactionArtifacts(transaction *mediaTransaction, artifacts []Artifact) {
	if transaction == nil {
		return
	}
	for _, artifact := range artifacts {
		if transaction.hasPath(artifact.Path) {
			continue
		}
		transaction.artifacts = append(transaction.artifacts, destinationSlot{
			destination: filepath.Clean(artifact.Path),
		})
	}
}

// outputPlanDestination renders a multi-output path from one shared template base.
// It remains for tests and mechanical suffix verification; product code uses
// resolveOutputPlanDestinations with per-plan templates.
func outputPlanDestination(
	base string,
	planIndex int,
	plan mediaformat.OutputPlan,
	multi bool,
	preferences []string,
) (string, error) {
	if !multi {
		return base, nil
	}
	extension, err := planDestinationExtension(plan, preferences)
	if err != nil {
		return "", err
	}
	baseExtension := filepath.Ext(base)
	stem := strings.TrimSuffix(filepath.Base(base), baseExtension)
	suffix := plan.DestinationSuffix(planIndex + 1)
	return filepath.Join(filepath.Dir(base), stem+".f"+suffix+"."+extension), nil
}

func validateOutputPlans(plans []mediaformat.OutputPlan, preferences []string) error {
	for index, plan := range plans {
		if _, err := planDestinationExtension(plan, preferences); err != nil {
			return fmt.Errorf("output plan[%d]: %w", index, err)
		}
	}
	return nil
}
