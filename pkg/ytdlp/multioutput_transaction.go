package ytdlp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/downloader"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

var errDestinationCollision = errors.New("output destination collision")

// mediaTransaction tracks published media paths for one entry download attempt.
// Pre-existing destinations are never removed on rollback.
type mediaTransaction struct {
	preexisting map[string]struct{}
	created     []string
}

func newMediaTransaction(destinations []string) mediaTransaction {
	tracker := mediaTransaction{preexisting: make(map[string]struct{})}
	for _, destination := range destinations {
		destination = filepath.Clean(destination)
		if destination == "" || destination == "-" {
			continue
		}
		if _, err := os.Stat(destination); err == nil {
			tracker.preexisting[destination] = struct{}{}
		}
	}
	return tracker
}

func (transaction *mediaTransaction) recordCreated(path string) {
	path = filepath.Clean(path)
	if path == "" || path == "-" {
		return
	}
	if _, exists := transaction.preexisting[path]; exists {
		return
	}
	for _, existing := range transaction.created {
		if existing == path {
			return
		}
	}
	transaction.created = append(transaction.created, path)
}

func (transaction *mediaTransaction) rollback() {
	for _, path := range transaction.created {
		_ = os.Remove(path)
	}
	transaction.created = nil
}

func (operation *operation) resolveOutputPlanDestinations(
	info value.Info,
	plans []mediaformat.OutputPlan,
) ([]string, error) {
	if len(plans) == 0 {
		return nil, nil
	}
	prefs := operation.mergeOutputPreferences()
	rendered := make([]string, len(plans))
	for index, plan := range plans {
		destination, err := operation.printFilenameForPlan(info, plan)
		if err != nil {
			return nil, err
		}
		destination, err = alignDestinationExtension(destination, plan, prefs)
		if err != nil {
			return nil, err
		}
		rendered[index] = destination
	}
	if len(plans) == 1 {
		return rendered, nil
	}
	return disambiguatePlanDestinations(rendered, plans), nil
}

func alignDestinationExtension(
	destination string,
	plan mediaformat.OutputPlan,
	preferences []string,
) (string, error) {
	if destination == "" || destination == "-" {
		return destination, nil
	}
	want, err := planDestinationExtension(plan, preferences)
	if err != nil {
		return "", err
	}
	current := strings.TrimPrefix(filepath.Ext(destination), ".")
	if current == want {
		return destination, nil
	}
	// Mirror pinned correct_ext: replace only when the rendered extension matches
	// a known plan extension, otherwise keep the template stem intact.
	planExt := plannedOutputExtension(plan, preferences)
	if current != "" && current != planExt && current != want {
		return destination, nil
	}
	stem := destination
	if ext := filepath.Ext(destination); ext != "" {
		stem = strings.TrimSuffix(destination, ext)
	}
	return stem + "." + want, nil
}

func disambiguatePlanDestinations(
	rendered []string,
	plans []mediaformat.OutputPlan,
) []string {
	if len(rendered) != len(plans) {
		return rendered
	}
	counts := make(map[string]int, len(rendered))
	for _, path := range rendered {
		counts[filepath.Clean(path)]++
	}
	result := make([]string, len(rendered))
	for index, path := range rendered {
		if counts[filepath.Clean(path)] <= 1 {
			result[index] = path
			continue
		}
		result[index] = mechanicalDestinationSuffix(path, plans[index], index+1)
	}
	return result
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

func (operation *operation) preflightPlanDestinations(destinations []string) (mediaTransaction, error) {
	transaction := newMediaTransaction(destinations)
	seen := make(map[string]struct{}, len(destinations))
	for _, destination := range destinations {
		destination = filepath.Clean(destination)
		if destination == "" || destination == "-" {
			continue
		}
		if _, exists := seen[destination]; exists {
			return transaction, fmt.Errorf("%w: %s", errDestinationCollision, destination)
		}
		seen[destination] = struct{}{}
		if _, preexisting := transaction.preexisting[destination]; preexisting {
			if !operation.request.Overwrite {
				return transaction, fmt.Errorf("%w: %s", downloader.ErrDestinationExists, destination)
			}
			continue
		}
		if _, err := os.Stat(destination); err == nil {
			if !operation.request.Overwrite {
				return transaction, fmt.Errorf("%w: %s", downloader.ErrDestinationExists, destination)
			}
		}
	}
	return transaction, nil
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
