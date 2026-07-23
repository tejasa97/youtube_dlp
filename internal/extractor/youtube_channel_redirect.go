package extractor

import (
	"encoding/json"
	"fmt"
	"net/url"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	youtubeMaxConditionalRedirectActions  = 128
	youtubeMaxConditionalRedirectURLBytes = 4096
)

// youtubeConditionalChannelRedirect extracts YouTube's non-HTTP regional
// channel redirect. Destinations are restricted to the same channel route
// families owned by the product registry, and the caller's explicit tab is
// appended only after validating that the response supplied a bare root.
func youtubeConditionalChannelRedirect(data []byte, sourceCanonical, requestedTab string) (Entry, bool, error) {
	if requestedTab != "" && youtubePublicTabType(requestedTab) == youtubeTabUnsupported {
		return Entry{}, false, fmt.Errorf("%w: unsupported YouTube conditional redirect tab", ErrInvalidMetadata)
	}
	if !utf8.Valid(data) {
		return Entry{}, false, fmt.Errorf("%w: invalid UTF-8 in YouTube conditional redirect", ErrInvalidMetadata)
	}
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return Entry{}, false, fmt.Errorf("%w: decode YouTube conditional redirect", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return Entry{}, false, fmt.Errorf("%w: YouTube conditional redirect root", ErrInvalidMetadata)
	}
	actions, ok := rootObject.Lookup("onResponseReceivedActions").ListValue()
	if !ok {
		return Entry{}, false, nil
	}
	if len(actions) > youtubeMaxConditionalRedirectActions {
		return Entry{}, false, fmt.Errorf("%w: too many YouTube conditional redirect actions", ErrInvalidMetadata)
	}

	var redirect Entry
	found := false
	for _, actionValue := range actions {
		action, ok := actionValue.Object()
		if !ok {
			continue
		}
		raw := objectString(action, "navigateAction", "endpoint", "commandMetadata", "webCommandMetadata", "url")
		if raw == "" {
			continue
		}
		candidate, err := normalizeYouTubeConditionalChannelRedirect(raw, requestedTab)
		if err != nil {
			return Entry{}, false, err
		}
		if found && (redirect.URL != candidate.URL || redirect.ExtractorKey != candidate.ExtractorKey) {
			return Entry{}, false, fmt.Errorf("%w: conflicting YouTube conditional redirects", ErrInvalidMetadata)
		}
		redirect, found = candidate, true
	}
	if !found {
		return Entry{}, false, nil
	}
	if sourceCanonical == redirect.URL {
		return Entry{}, false, fmt.Errorf("%w: self-referential YouTube conditional redirect", ErrInvalidMetadata)
	}
	return redirect, true, nil
}

func normalizeYouTubeConditionalChannelRedirect(raw, requestedTab string) (Entry, error) {
	if raw == "" || len(raw) > youtubeMaxConditionalRedirectURLBytes || !utf8.ValidString(raw) {
		return Entry{}, fmt.Errorf("%w: invalid YouTube conditional redirect URL", ErrInvalidMetadata)
	}
	for _, character := range raw {
		if unicode.IsControl(character) {
			return Entry{}, fmt.Errorf("%w: invalid YouTube conditional redirect URL", ErrInvalidMetadata)
		}
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: invalid YouTube conditional redirect URL", ErrInvalidMetadata)
	}
	base, _ := url.Parse("https://www.youtube.com")
	resolved := base.ResolveReference(reference)
	if resolved.RawQuery != "" || resolved.Fragment != "" || resolved.RawPath != "" {
		return Entry{}, fmt.Errorf("%w: unsafe YouTube conditional redirect URL", ErrInvalidMetadata)
	}

	if channelID, tab, ok := youtubeChannelTabTarget(resolved); ok && tab == "" {
		canonical := "https://www.youtube.com/channel/" + channelID
		if requestedTab != "" {
			canonical += "/" + requestedTab
		}
		return Entry{URL: canonical, ExtractorKey: "youtube_channel_tab"}, nil
	}
	if handle, tab, ok := youtubeHandleTabTarget(resolved); ok && tab == "" {
		canonical := "https://www.youtube.com/" + handle
		if requestedTab != "" {
			canonical += "/" + requestedTab
		}
		return Entry{URL: canonical, ExtractorKey: "youtube_handle_tab"}, nil
	}
	if kind, alias, tab, ok := youtubeAliasTabTarget(resolved); ok && tab == "" {
		return Entry{
			URL:          youtubeAliasTabCanonicalURL(kind, alias, requestedTab),
			ExtractorKey: "youtube_alias_tab",
		}, nil
	}
	return Entry{}, fmt.Errorf("%w: unsupported YouTube conditional redirect destination", ErrInvalidMetadata)
}

func youtubeConditionalRedirectResult(data []byte, sourceCanonical, requestedTab string) (Extraction, bool, error) {
	redirect, ok, err := youtubeConditionalChannelRedirect(data, sourceCanonical, requestedTab)
	if err != nil || !ok {
		return Extraction{}, ok, err
	}
	result, err := URLResult(redirect)
	return result, true, err
}
