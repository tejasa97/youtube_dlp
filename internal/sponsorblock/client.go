package sponsorblock

import (
	"context"
	"errors"
	"net/http"
)

// Transport is the minimal surface required to fetch one SponsorBlock
// page. The shared internal/network.Client satisfies it. The stronger
// credential-isolated, no-redirect interface is asserted at the call site so
// a transport cannot silently downgrade this third-party request.
type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
}

// cookieIsolatedNoRedirect is the stronger boundary required for SponsorBlock:
// it drops operation credentials and returns the first redirect response. A
// third-party API request must never be redirected to an arbitrary authority.
type cookieIsolatedNoRedirect interface {
	DoWithoutCredentialsNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

// FetchResult is the bounded output of a single SponsorBlock lookup.
// Chapters is sorted, deterministic, and may be empty (a 404 or a
// no-match response). DurationMismatchFiltered is true when one or more
// otherwise-valid segments were dropped by the pinned videoDuration filter.
type FetchResult struct {
	Prefix                   string
	Chapters                 []Chapter
	DurationMismatchFiltered bool
}

// Fetch performs the canonical SponsorBlock lookup for one video and
// returns the normalized chapters for the matching group. The function
// is context-aware, retries are the caller's responsibility, and the
// transport must support credential-isolated no-redirect requests. Otherwise
// the call fails closed with ErrIsolation so credentials cannot be forwarded
// and redirects cannot cross this third-party trust boundary.
func Fetch(ctx context.Context, transport Transport, options Options, service, videoID string, videoDuration float64) (FetchResult, error) {
	if ctx == nil {
		return FetchResult{}, errorf(ErrInvalidInput, "nil context")
	}
	if transport == nil {
		return FetchResult{}, errorf(ErrInvalidInput, "nil transport")
	}
	if !options.Enabled {
		return FetchResult{}, nil
	}
	cloned := options
	if err := cloned.validate(); err != nil {
		return FetchResult{}, err
	}
	if service != "YouTube" {
		return FetchResult{}, errorf(ErrUnsupported, "unsupported service")
	}
	prefix, err := hashPrefix(videoID)
	if err != nil {
		return FetchResult{}, err
	}
	endpoint, err := buildEndpointURL(cloned.resolvedAPIBase(), prefix, cloned.resolvedCategories(), AllActions())
	if err != nil {
		return FetchResult{}, err
	}
	body, err := fetchBody(ctx, transport, endpoint)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return FetchResult{Prefix: prefix, Chapters: []Chapter{}}, nil
		}
		return FetchResult{}, err
	}
	groups, err := decodeResponse(body, videoID)
	if err != nil {
		return FetchResult{}, err
	}
	// Match only the exact videoID group, per the pinned reference.
	for _, group := range groups {
		if group.VideoID == videoID {
			allowed := make(map[string]bool, len(cloned.Categories))
			for _, category := range cloned.Categories {
				allowed[category] = true
			}
			segments := make([]RawSegment, 0, len(group.Segments))
			for _, segment := range group.Segments {
				if allowed[segment.Category] {
					segments = append(segments, segment)
				}
			}
			normalized := NormalizeDetailed(segments, videoDuration)
			return FetchResult{
				Prefix:                   prefix,
				Chapters:                 normalized.Chapters,
				DurationMismatchFiltered: normalized.DurationMismatchFiltered,
			}, nil
		}
	}
	return FetchResult{Prefix: prefix, Chapters: []Chapter{}}, nil
}
