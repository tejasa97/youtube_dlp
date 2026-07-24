package sponsorblock

import (
	"net/url"
	"strings"
)

// isAcceptableAPIBase returns true when raw is a syntactically valid
// http(s) URL with a non-empty host. The same predicate is used by the
// request builder.
func isAcceptableAPIBase(raw string) bool {
	if raw == "" || len(raw) > 4096 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return false
	}
	return true
}

// resolvedCategories returns a defensive copy in caller order. Validation
// guarantees that enabled requests have at least one category.
func (options Options) resolvedCategories() []Category {
	out := make([]Category, len(options.Categories))
	for i, raw := range options.Categories {
		out[i] = Category(raw)
	}
	return out
}

// resolvedAPIBase returns the caller-supplied API base or the pinned
// default. The returned string is always non-empty and uses the http or
// https scheme.
func (options Options) resolvedAPIBase() string {
	if options.APIBase == "" {
		return DefaultAPIBase
	}
	return options.APIBase
}
