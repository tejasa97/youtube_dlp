package ytdlp

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
)

const (
	maxRoutingInputBytes     = 16 << 10
	maxDefaultSearchBytes    = 64
	defaultSearchPlaceholder = "ytdlp-routing-placeholder"
)

// routedInput is intentionally internal. The public Request retains the
// caller's typed controls while the operation receives a safe pseudo URL and
// a separate validated query when default-search selects an opaque extractor.
type routedInput struct {
	URL             string
	SearchQuery     string
	Warning         string
	SearchExtractor string
}

// routeRequestInput performs deterministic, network-free input routing. It
// runs before transport construction so malformed controls and unsafe forced
// generic targets cannot cause a live lookup or credential-bearing diagnostic.
func routeRequestInput(ctx context.Context, registry *extractor.Registry, request Request) (routedInput, error) {
	if err := ctx.Err(); err != nil {
		return routedInput{}, err
	}
	if request.URL == "" {
		return routedInput{}, fmt.Errorf("%w: empty input", extractor.ErrInvalidRouting)
	}
	if len(request.URL) > maxRoutingInputBytes || !utf8.ValidString(request.URL) || strings.ContainsAny(request.URL, "\x00\r\n\t") {
		return routedInput{}, fmt.Errorf("%w: input bounds", extractor.ErrInvalidRouting)
	}

	// A plugin ID is an explicit extractor key. It wins over generic forcing
	// and default-search just as an explicit ie_key wins in the reference. Do
	// not even parse or select a built-in extractor here: signed plugin callers
	// own the input grammar after this basic bounded-input check.
	if request.PluginID != "" {
		return routedInput{URL: request.URL}, nil
	}
	if registry == nil {
		return routedInput{}, fmt.Errorf("%w: missing extractor registry", extractor.ErrInvalidRouting)
	}
	rawURL := request.URL
	if strings.Contains(strings.SplitN(rawURL, "/", 2)[0], "@") {
		return routedInput{}, fmt.Errorf("%w: unsafe protocol-less URL", extractor.ErrInvalidRouting)
	}
	if implicitHostInput(rawURL) {
		qualified, err := qualifyImplicitHost(rawURL)
		if err != nil {
			return routedInput{}, err
		}
		if request.ForceGenericExtractor {
			if _, err := registry.SelectFor(qualified, "generic"); err != nil {
				return routedInput{}, err
			}
		}
		return routedInput{URL: qualified, Warning: "input URL has no scheme; trying https"}, nil
	}
	parsed, parseErr := url.Parse(rawURL)
	if parseErr != nil {
		return routedInput{}, fmt.Errorf("%w: malformed input", extractor.ErrUnsupported)
	}
	if parsed.Scheme != "" {
		if (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && parsed.Host == "" {
			return routedInput{}, fmt.Errorf("%w: HTTP URL has no host", extractor.ErrUnsupported)
		}
		if request.ForceGenericExtractor {
			if _, err := registry.SelectFor(rawURL, "generic"); err != nil {
				return routedInput{}, err
			}
		}
		return routedInput{URL: rawURL}, nil
	}
	if strings.HasPrefix(rawURL, "/") {
		return routedInput{}, fmt.Errorf("%w: ambiguous path input", extractor.ErrInvalidRouting)
	}

	// The reference repairs a protocol-less public host only when its shape is
	// unambiguous. Localhost and IP literals are deliberately not promoted to
	// HTTPS, preventing a search-control flag from turning a local target into a
	// generic network request by accident.
	if request.ForceGenericExtractor {
		// Do not turn an unqualified search term into a search while the caller
		// explicitly requested the generic extractor. The registered generic
		// extractor will reject this non-URL before transport use.
		if _, err := registry.SelectFor(rawURL, "generic"); err != nil {
			return routedInput{}, err
		}
		return routedInput{URL: rawURL}, nil
	}

	mode := strings.ToLower(request.DefaultSearch)
	if mode == "" {
		mode = "fixup_error"
	}
	switch mode {
	case "fixup_error", "error":
		return routedInput{}, fmt.Errorf("%w: input is not a URL", extractor.ErrUnsupported)
	case "auto", "auto_warning":
		return defaultSearchRoute(registry, "ytsearch", rawURL, mode == "auto_warning")
	default:
		prefix, err := normalizeSearchPrefix(mode)
		if err != nil {
			return routedInput{}, err
		}
		return defaultSearchRoute(registry, prefix, rawURL, false)
	}
}

func normalizeSearchPrefix(raw string) (string, error) {
	if raw == "" || len(raw) > maxDefaultSearchBytes || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\x00\r\n\t /\\") {
		return "", fmt.Errorf("%w: default-search prefix", extractor.ErrInvalidRouting)
	}
	if strings.HasSuffix(raw, ":") {
		raw = strings.TrimSuffix(raw, ":")
	}
	if raw == "" || strings.Contains(raw, ":") {
		return "", fmt.Errorf("%w: default-search prefix syntax", extractor.ErrInvalidRouting)
	}
	return strings.ToLower(raw), nil
}

func defaultSearchRoute(registry *extractor.Registry, prefix, query string, warning bool) (routedInput, error) {
	search, ok := registry.SearchPrefix(prefix)
	if !ok {
		return routedInput{}, fmt.Errorf("%w: default-search prefix", extractor.ErrUnsupportedRouting)
	}
	if !search.SearchQueryAllowed(query) {
		return routedInput{}, fmt.Errorf("%w: default-search query bounds", extractor.ErrInvalidRouting)
	}
	rawURL := prefix + ":" + defaultSearchPlaceholder
	parsed, err := url.Parse(rawURL)
	if err != nil || !search.Suitable(parsed) {
		return routedInput{}, fmt.Errorf("%w: default-search route", extractor.ErrUnsupportedRouting)
	}
	route := routedInput{URL: rawURL, SearchQuery: query, SearchExtractor: search.Name()}
	if warning {
		route.Warning = "input is not a URL; falling back to YouTube search"
	}
	return route, nil
}

func implicitHostInput(raw string) bool {
	if !strings.Contains(raw, "/") {
		return false
	}
	first := strings.SplitN(raw, "/", 2)[0]
	if first == "" || strings.ContainsAny(first, "?&#") || strings.ContainsAny(first, " \t") {
		return false
	}
	if strings.Contains(first, ".") || strings.EqualFold(first, "localhost") {
		return true
	}
	if host, _, err := net.SplitHostPort(first); err == nil {
		return net.ParseIP(host) != nil
	}
	return net.ParseIP(first) != nil
}

func qualifyImplicitHost(raw string) (string, error) {
	first := strings.SplitN(raw, "/", 2)[0]
	hostCandidate := strings.Trim(first, "[]")
	if strings.EqualFold(hostCandidate, "localhost") || strings.HasSuffix(strings.ToLower(hostCandidate), ".localhost") || net.ParseIP(hostCandidate) != nil {
		return "", fmt.Errorf("%w: local or IP protocol-less URL", extractor.ErrInvalidRouting)
	}
	qualified := "https://" + raw
	parsed, err := url.Parse(qualified)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", fmt.Errorf("%w: unsafe protocol-less URL", extractor.ErrInvalidRouting)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || net.ParseIP(host) != nil {
		return "", fmt.Errorf("%w: local or IP protocol-less URL", extractor.ErrInvalidRouting)
	}
	return parsed.String(), nil
}
