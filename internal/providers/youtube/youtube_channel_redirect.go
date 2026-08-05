package youtube

import (
	"encoding/json"
	"fmt"
	"net/url"
	"unicode"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	youtubeMaxConditionalRedirectActions  = 128
	youtubeMaxConditionalRedirectURLBytes = 4096
)

// youtubeConditionalChannelRedirect extracts YouTube's non-HTTP regional
// channel redirect. Destinations are restricted to the same channel route
// families owned by the product registry, and the caller's explicit tab is
// appended only after validating that the response supplied a bare root.
// When requestedTab is "search", sourceCanonical must carry a validated
// query/q that is preserved onto the redirected identity.
func youtubeConditionalChannelRedirect(data []byte, sourceCanonical, requestedTab string) (Entry, bool, error) {
	// Conditional redirects may append only built-in public tabs or channel
	// search. Arbitrary custom tabs are never rewritten through this path; the
	// custom-tab extract continues with its own identity binding instead.
	if requestedTab != "" && youtubePublicTabType(requestedTab) == youtubeTabUnsupported && requestedTab != "search" {
		return Entry{}, false, nil
	}
	searchQuery := ""
	if requestedTab == "search" {
		parsedSource, err := url.Parse(sourceCanonical)
		if err != nil {
			return Entry{}, false, fmt.Errorf("%w: invalid YouTube conditional redirect source", ErrInvalidMetadata)
		}
		searchQuery = youtubeChannelSearchQuery(parsedSource)
		if searchQuery == "" {
			return Entry{}, false, fmt.Errorf("%w: missing YouTube conditional redirect search query", ErrInvalidMetadata)
		}
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
		candidate, err := normalizeYouTubeConditionalChannelRedirect(raw, requestedTab, searchQuery)
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

func normalizeYouTubeConditionalChannelRedirect(raw, requestedTab, searchQuery string) (Entry, error) {
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

	appendTab := func(root string) (string, error) {
		if requestedTab == "" {
			return root, nil
		}
		if requestedTab == "search" {
			if !validYouTubeSearchQuery(searchQuery) {
				return "", fmt.Errorf("%w: missing YouTube conditional redirect search query", ErrInvalidMetadata)
			}
			return root + "/search?" + url.Values{"query": {searchQuery}}.Encode(), nil
		}
		return root + "/" + requestedTab, nil
	}

	if channelID, tab, ok := youtubeChannelTabTarget(resolved); ok && tab == "" {
		canonical, err := appendTab("https://www.youtube.com/channel/" + channelID)
		if err != nil {
			return Entry{}, err
		}
		return Entry{URL: canonical, ExtractorKey: "youtube_channel_tab"}, nil
	}
	if handle, tab, ok := youtubeHandleTabTarget(resolved); ok && tab == "" {
		canonical, err := appendTab("https://www.youtube.com/" + handle)
		if err != nil {
			return Entry{}, err
		}
		return Entry{URL: canonical, ExtractorKey: "youtube_handle_tab"}, nil
	}
	if kind, alias, tab, ok := youtubeAliasTabTarget(resolved); ok && tab == "" {
		root := youtubeAliasTabCanonicalURL(kind, alias, "")
		canonical, err := appendTab(root)
		if err != nil {
			return Entry{}, err
		}
		return Entry{URL: canonical, ExtractorKey: "youtube_alias_tab"}, nil
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
