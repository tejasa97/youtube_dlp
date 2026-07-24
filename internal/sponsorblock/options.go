package sponsorblock

import (
	"strings"
)

// Defaults are the pinned reference defaults.
const (
	// DefaultAPIBase is the canonical public SponsorBlock deployment
	// used by the upstream postprocessor. Tests and self-hosted
	// deployments may override it.
	DefaultAPIBase = "https://sponsor.ajay.app"
	// MaxCategories caps the size of the requested category set so a
	// caller cannot craft a request that the API will reject. The
	// pinned set has 11 entries; the cap is generous without being
	// unbounded.
	MaxCategories = 64
	// MaxSegmentCount caps the number of segments decoded from a single
	// response. A single video rarely has more than a few dozen
	// segments; the cap protects against hostile or pathological
	// responses.
	MaxSegmentCount = 4096
	// MaxResponseBytes caps the raw response body that will be
	// decoded. SponsorBlock responses are typically a few kilobytes;
	// 4 MiB is generous without permitting unbounded work.
	MaxResponseBytes = 4 << 20
	// MaxStringBytes caps any individual string decoded from the
	// response (videoID, UUID, description, category, action type).
	// Anything longer is rejected.
	MaxStringBytes = 1024
)

// Options is the bounded SponsorBlock request configuration. It is the
// shape exposed through pkg/ytdlp.Request and validated before any network
// work occurs.
type Options struct {
	// Enabled gates the entire stage. When false, Fetch returns
	// (nil, nil) and never touches the network. It is the only field
	// checked by the public integration.
	Enabled bool
	// Categories is the requested non-empty set of category identifiers. The slice
	// is treated as caller-owned and is never mutated.
	Categories []string
	// APIBase overrides the API origin for deterministic tests and
	// self-hosted deployments. The value must be a syntactically valid
	// http(s) URL with a non-empty host. When empty, DefaultAPIBase is
	// used.
	APIBase string
}

// validate enforces the public option bounds. The returned error wraps
// ErrInvalidInput so callers can map it to a public invalid_input
// category.
func (options *Options) validate() error {
	if !options.Enabled {
		return nil
	}
	if len(options.Categories) == 0 {
		return errorf(ErrInvalidInput, "empty enabled category set")
	}
	if len(options.Categories) > MaxCategories {
		return errorf(ErrInvalidInput, "too many categories")
	}
	seen := make(map[string]struct{}, len(options.Categories))
	categories := make([]Category, 0, len(options.Categories))
	for _, raw := range options.Categories {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return errorf(ErrInvalidInput, "empty category")
		}
		if len(trimmed) > 64 {
			return errorf(ErrInvalidInput, "category too long")
		}
		if !IsValidCategory(trimmed) {
			return errorf(ErrInvalidInput, "unknown category")
		}
		if _, duplicate := seen[trimmed]; duplicate {
			// Deterministic de-duplication keeps the first
			// caller occurrence. The reference only
			// guarantees a tuple of categories; ordering is
			// not part of the contract, so the de-duplicated
			// slice is sorted by the first-seen index.
			continue
		}
		seen[trimmed] = struct{}{}
		categories = append(categories, Category(trimmed))
	}
	if len(categories) == 0 {
		return errorf(ErrInvalidInput, "empty enabled category set")
	}
	if len(categories) > MaxCategories {
		return errorf(ErrInvalidInput, "too many categories")
	}
	options.Categories = make([]string, len(categories))
	for i, category := range categories {
		options.Categories[i] = string(category)
	}
	if options.APIBase != "" {
		if !isAcceptableAPIBase(options.APIBase) {
			return errorf(ErrInvalidInput, "invalid API base")
		}
	}
	return nil
}
