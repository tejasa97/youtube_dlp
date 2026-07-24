package sponsorblock

import (
	"errors"
	"fmt"
)

// Category is one pinned SponsorBlock category identifier.
type Category string

const (
	CategorySponsor       Category = "sponsor"
	CategoryIntro         Category = "intro"
	CategoryOutro         Category = "outro"
	CategorySelfPromo     Category = "selfpromo"
	CategoryPreview       Category = "preview"
	CategoryFiller        Category = "filler"
	CategoryInteraction   Category = "interaction"
	CategoryMusicOfftopic Category = "music_offtopic"
	CategoryHook          Category = "hook"
	CategoryPOIHighlight  Category = "poi_highlight"
	CategoryChapter       Category = "chapter"
)

// AllCategories returns the canonical category order for fallback defaults.
// The order intentionally matches the pinned reference table.
func AllCategories() []Category {
	return []Category{
		CategorySponsor, CategoryIntro, CategoryOutro, CategorySelfPromo,
		CategoryPreview, CategoryFiller, CategoryInteraction, CategoryMusicOfftopic,
		CategoryHook, CategoryPOIHighlight, CategoryChapter,
	}
}

// CanonicalTitle returns the pinned display title for one category. The
// "chapter" category uses its own description as title; the caller must
// resolve that branch separately. The returned string is treated as opaque
// untrusted metadata: it is never echoed in errors.
func CanonicalTitle(category Category) (string, bool) {
	switch category {
	case CategorySponsor:
		return "Sponsor", true
	case CategoryIntro:
		return "Intermission/Intro Animation", true
	case CategoryOutro:
		return "Endcards/Credits", true
	case CategorySelfPromo:
		return "Unpaid/Self Promotion", true
	case CategoryPreview:
		return "Preview/Recap", true
	case CategoryFiller:
		return "Filler Tangent", true
	case CategoryInteraction:
		return "Interaction Reminder", true
	case CategoryMusicOfftopic:
		return "Non-Music Section", true
	case CategoryHook:
		return "Hook/Greetings", true
	case CategoryPOIHighlight:
		return "Highlight", true
	case CategoryChapter:
		return "Chapter", true
	}
	return "", false
}

// ActionType is the bounded set of action types requested from the API.
type ActionType string

const (
	ActionSkip    ActionType = "skip"
	ActionPOI     ActionType = "poi"
	ActionChapter ActionType = "chapter"
)

// AllActions returns the canonical actionTypes set requested from the API.
// The order matches the pinned reference (skip, poi, chapter) and is the
// only legal set; the package never sends any other value.
func AllActions() []ActionType {
	return []ActionType{ActionSkip, ActionPOI, ActionChapter}
}

// IsValidCategory reports whether category is a pinned SponsorBlock
// category. Empty strings and any unknown identifier are rejected.
func IsValidCategory(category string) bool {
	for _, candidate := range AllCategories() {
		if string(candidate) == category {
			return true
		}
	}
	return false
}

// IsValidAction reports whether action is one of the pinned action types.
func IsValidAction(action string) bool {
	switch ActionType(action) {
	case ActionSkip, ActionPOI, ActionChapter:
		return true
	}
	return false
}

// IsPOI reports whether the category is one whose end is extended by one
// second by the pinned normalizer. Only poi_highlight qualifies.
func IsPOI(category Category) bool {
	return category == CategoryPOIHighlight
}

// ErrCategory is the sentinel used for categorized SponsorBlock errors. The
// package only exports sentinels and wraps them with %w so callers can map
// them onto the public pkg/ytdlp error categories via errors.Is.
var (
	// ErrInvalidInput signals malformed caller input (bad video ID, empty
	// categories, unknown categories, excessive limits). Maps to public
	// invalid_input in pkg/ytdlp.
	ErrInvalidInput = errors.New("sponsorblock invalid input")
	// ErrUnsupported signals a non-YouTube service or a request that the
	// package cannot satisfy because SponsorBlock only carries YouTube
	// metadata in this port. Maps to public unsupported.
	ErrUnsupported = errors.New("sponsorblock unsupported")
	// ErrUnavailable signals the SponsorBlock service has no metadata
	// for the requested video. Maps to public unavailable where the
	// pinned semantics treat a missing entry as a soft no-op.
	ErrUnavailable = errors.New("sponsorblock unavailable")
	// ErrNetwork signals transport, HTTP 429, or HTTP 5xx failures.
	ErrNetwork = errors.New("sponsorblock network error")
	// ErrAuthentication signals HTTP 401/403 responses. SponsorBlock is
	// unauthenticated, but the mapping is retained for completeness.
	ErrAuthentication = errors.New("sponsorblock authentication required")
	// ErrIsolation signals that the shared transport cannot guarantee that
	// unrelated operation credentials are excluded from the request.
	ErrIsolation = errors.New("sponsorblock credential isolation unavailable")
	// ErrInvalidMetadata signals malformed JSON, oversized envelopes,
	// or any other structurally hostile response. Maps to public
	// internal/invalid metadata.
	ErrInvalidMetadata = errors.New("sponsorblock invalid metadata")
)

// errorf wraps a sentinel with a static, secret-safe context string. It
// refuses to interpolate arbitrary caller data (video IDs, URLs, response
// bodies, descriptions) into the rendered message.
func errorf(sentinel error, context string) error {
	if context == "" {
		return sentinel
	}
	return fmt.Errorf("%w: %s", sentinel, context)
}
