package sponsorblock

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Transport is the minimal surface required to fetch one SponsorBlock
// page. The shared internal/network.Client satisfies it. The
// cookieIsolated interface is asserted at the call site so a transport
// that cannot isolate cookies is rejected with a closed failure
// instead of silently downgrading to a cookie-bearing request.
type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
}

// cookieIsolated is the stronger credential-isolated interface asserted at
// request time.
// The shared internal/network.Client implements it.
type cookieIsolated interface {
	DoWithoutCredentials(context.Context, *http.Request) (*http.Response, error)
}

// FetchResult is the bounded output of a single SponsorBlock lookup.
// Chapters is sorted, deterministic, and may be empty (a 404 or a
// no-match response).
type FetchResult struct {
	Prefix   string
	Chapters []Chapter
}

// Fetch performs the canonical SponsorBlock lookup for one video and
// returns the normalized chapters for the matching group. The function
// is context-aware, retries are the caller's responsibility, and the
// transport must support cookie isolation. If the transport does not
// expose DoWithoutCredentials, the call fails closed and returns
// ErrIsolation so operation credentials can never be forwarded to
// SponsorBlock.
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
	if strings.ToLower(service) != "youtube" {
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
			chapters := Normalize(segments, videoDuration)
			return FetchResult{Prefix: prefix, Chapters: chapters}, nil
		}
	}
	return FetchResult{Prefix: prefix, Chapters: []Chapter{}}, nil
}
