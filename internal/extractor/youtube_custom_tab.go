package extractor

// Dynamically advertised channel tabs. Unknown tabs are accepted only when
// YouTube advertises an endpoint that is securely bound to the requested
// channel identity.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	youtubeCustomTabMaxBytes      = 64
	youtubeCustomTabMaxTitleBytes = 200
)

// youtubeAdvertisedTab is a channel tab YouTube listed on the browse page.
type youtubeAdvertisedTab struct {
	ID    string
	Title string
	URL   string
	Count int // approximate when present; zero means unknown/absent
}

// youtubeChannelIdentity describes the channel URL form that must own a custom
// tab endpoint. Exactly one of ChannelID, Handle, or Alias is set.
type youtubeChannelIdentity struct {
	ChannelID string
	Handle    string
	AliasKind string // "user" or "c"
	Alias     string
}

func (identity youtubeChannelIdentity) basePath() string {
	paths := identity.attributablePaths()
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// attributablePaths returns every channel URL prefix this identity may own.
// Handle/alias pages often advertise UCID-form endpoints after resolution, and
// either form must remain attributable to the requested identity.
func (identity youtubeChannelIdentity) attributablePaths() []string {
	var paths []string
	seen := make(map[string]struct{})
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	if youtubeChannelIDPattern.MatchString(identity.ChannelID) {
		add("/channel/" + identity.ChannelID)
	}
	if identity.Handle != "" {
		add("/" + identity.Handle)
	}
	if (identity.AliasKind == "user" || identity.AliasKind == "c") && identity.Alias != "" {
		add("/" + identity.AliasKind + "/" + identity.Alias)
	}
	return paths
}

func youtubeDiscoverAdvertisedTabs(root *value.Object, identity youtubeChannelIdentity) []youtubeAdvertisedTab {
	if root == nil {
		return nil
	}
	contents, ok := root.Lookup("contents").Object()
	if !ok {
		return nil
	}
	browse, ok := contents.Lookup("twoColumnBrowseResultsRenderer").Object()
	if !ok {
		return nil
	}
	tabs, ok := browse.Lookup("tabs").ListValue()
	if !ok {
		return nil
	}
	var result []youtubeAdvertisedTab
	seen := make(map[string]struct{})
	for _, item := range tabs {
		itemObject, ok := item.Object()
		if !ok {
			continue
		}
		for _, rendererName := range []string{"tabRenderer", "expandableTabRenderer"} {
			renderer, ok := itemObject.Lookup(rendererName).Object()
			if !ok {
				continue
			}
			tab, ok := youtubeAdvertisedTabFromRenderer(renderer, identity)
			if !ok {
				continue
			}
			if _, exists := seen[tab.ID]; exists {
				continue
			}
			seen[tab.ID] = struct{}{}
			result = append(result, tab)
		}
	}
	return result
}

func youtubeAdvertisedTabFromRenderer(renderer *value.Object, identity youtubeChannelIdentity) (youtubeAdvertisedTab, bool) {
	if renderer == nil {
		return youtubeAdvertisedTab{}, false
	}
	rawURL := objectString(renderer, "endpoint", "commandMetadata", "webCommandMetadata", "url")
	if rawURL == "" {
		rawURL = objectString(renderer, "endpoint", "browseEndpoint", "canonicalBaseUrl")
	}
	tabID, tabURL, ok := youtubeTabIDFromEndpointURL(rawURL)
	if !ok {
		// Fall back to tabIdentifier / title for built-in tabs that omit URLs
		// in synthetic fixtures.
		for _, candidate := range youtubeSelectedTabIdentities(renderer) {
			if youtubePublicTabType(candidate) != youtubeTabUnsupported || candidate == "search" {
				tabID = candidate
				break
			}
		}
		if tabID == "" {
			return youtubeAdvertisedTab{}, false
		}
	}
	if len(identity.attributablePaths()) > 0 {
		if tabURL == "" {
			// Custom tabs without an attributable endpoint must not surface in
			// channel_tabs metadata; built-in/search fixtures may omit URLs.
			if youtubePublicTabType(tabID) == youtubeTabUnsupported && tabID != "search" {
				return youtubeAdvertisedTab{}, false
			}
		} else if err := youtubeValidateCustomTabEndpoint(tabURL, identity); err != nil {
			return youtubeAdvertisedTab{}, false
		}
	}
	title := ""
	if text, ok := renderer.Lookup("title").StringValue(); ok {
		title = text
	} else {
		title = rendererText(renderer.Lookup("title"))
	}
	if len(title) > youtubeCustomTabMaxTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	count := youtubeApproximateTabCount(renderer)
	return youtubeAdvertisedTab{ID: tabID, Title: title, URL: tabURL, Count: count}, true
}

func youtubeTabIDFromEndpointURL(raw string) (id, canonical string, ok bool) {
	if raw == "" {
		return "", "", false
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	resolved := (&url.URL{Scheme: "https", Host: "www.youtube.com"}).ResolveReference(reference)
	if err := youtubeAssertExactWebHost(resolved); err != nil {
		return "", "", false
	}
	if resolved.User != nil || resolved.Fragment != "" || resolved.RawPath != "" {
		return "", "", false
	}
	lowQuery := strings.ToLower(resolved.RawQuery)
	if strings.Contains(lowQuery, "%2f") || strings.Contains(lowQuery, "%5c") || strings.Contains(lowQuery, "%00") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(resolved.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "" {
		return "", "", false
	}
	tab := parts[len(parts)-1]
	if !validYouTubeCustomTabSegment(tab) {
		return "", "", false
	}
	if normalized := normalizedYouTubeTabIdentity(tab); normalized != "" {
		tab = normalized
	}
	canonicalURL := &url.URL{Scheme: "https", Host: "www.youtube.com", Path: resolved.Path}
	return tab, canonicalURL.String(), true
}

func validYouTubeCustomTabSegment(tab string) bool {
	if tab == "" || len(tab) > youtubeCustomTabMaxBytes || !utf8.ValidString(tab) {
		return false
	}
	if tab == "." || tab == ".." || strings.ContainsAny(tab, `/\.%?#&`) {
		return false
	}
	for _, r := range tab {
		if unicode.IsControl(r) {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func youtubeApproximateTabCount(renderer *value.Object) int {
	if renderer == nil {
		return 0
	}
	for _, path := range [][]string{
		{"content", "richGridRenderer", "header", "feedFilterChipBarRenderer", "contents"},
	} {
		_ = path // reserved for future chip-count shapes
	}
	// Prefer accessibility labels that carry "N videos" style counts when present.
	label := objectString(renderer, "accessibility", "accessibilityData", "label")
	if label == "" {
		label = objectString(renderer, "endpoint", "commandMetadata", "webCommandMetadata", "label")
	}
	return youtubeParseApproximateCount(label)
}

func youtubeParseApproximateCount(label string) int {
	label = strings.TrimSpace(label)
	if label == "" {
		return 0
	}
	var digits strings.Builder
	for _, r := range label {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
			continue
		}
		if digits.Len() > 0 {
			break
		}
	}
	if digits.Len() == 0 || digits.Len() > 9 {
		return 0
	}
	n, err := strconv.Atoi(digits.String())
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// youtubeValidateCustomTabEndpoint ensures an advertised or selected tab
// endpoint stays under the requested channel identity and never pivots host,
// identity, or encoded separators.
func youtubeValidateCustomTabEndpoint(raw string, identity youtubeChannelIdentity) error {
	if raw == "" {
		return fmt.Errorf("%w: missing custom tab endpoint", ErrInvalidMetadata)
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: invalid custom tab endpoint", ErrInvalidMetadata)
	}
	resolved := (&url.URL{Scheme: "https", Host: "www.youtube.com"}).ResolveReference(reference)
	if err := youtubeAssertExactWebHost(resolved); err != nil {
		return err
	}
	if resolved.User != nil || resolved.Fragment != "" || resolved.RawPath != "" ||
		strings.Contains(strings.ToLower(resolved.EscapedPath()), "%2f") ||
		strings.Contains(strings.ToLower(resolved.EscapedPath()), "%5c") ||
		strings.Contains(strings.ToLower(resolved.EscapedPath()), "%00") {
		return fmt.Errorf("%w: hostile custom tab endpoint", ErrInvalidMetadata)
	}
	lowQuery := strings.ToLower(resolved.RawQuery)
	if strings.Contains(lowQuery, "%2f") || strings.Contains(lowQuery, "%5c") || strings.Contains(lowQuery, "%00") {
		return fmt.Errorf("%w: hostile custom tab query", ErrInvalidMetadata)
	}
	bases := identity.attributablePaths()
	if len(bases) == 0 {
		return fmt.Errorf("%w: missing channel identity for custom tab", ErrInvalidMetadata)
	}
	path := strings.TrimSuffix(resolved.Path, "/")
	var matchedBase string
	for _, base := range bases {
		if path == base || strings.HasPrefix(path, base+"/") {
			matchedBase = base
			break
		}
	}
	if matchedBase == "" {
		return fmt.Errorf("%w: custom tab endpoint escapes channel identity", ErrInvalidMetadata)
	}
	remainder := strings.TrimPrefix(path, matchedBase)
	remainder = strings.TrimPrefix(remainder, "/")
	if remainder == "" {
		return nil
	}
	parts := strings.Split(remainder, "/")
	if len(parts) != 1 || !validYouTubeCustomTabSegment(parts[0]) {
		return fmt.Errorf("%w: hostile custom tab path", ErrInvalidMetadata)
	}
	return nil
}

func youtubeValidateCustomTabBrowseID(browseID string, identity youtubeChannelIdentity) error {
	if browseID == "" {
		return nil
	}
	if !youtubeChannelIDPattern.MatchString(browseID) {
		return fmt.Errorf("%w: hostile custom tab browse id", ErrInvalidMetadata)
	}
	if !youtubeChannelIDPattern.MatchString(identity.ChannelID) {
		return fmt.Errorf("%w: custom tab browse id without resolved channel identity", ErrInvalidMetadata)
	}
	if browseID != identity.ChannelID {
		return fmt.Errorf("%w: custom tab browse id identity mismatch", ErrInvalidMetadata)
	}
	return nil
}

func youtubeCustomTabSelectedAndBound(data []byte, requested string, identity youtubeChannelIdentity) error {
	if err := validateYouTubeSelectedTab(data, requested); err != nil {
		return err
	}
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("%w: decode custom tab binding", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return fmt.Errorf("%w: custom tab binding root", ErrInvalidMetadata)
	}
	contents, ok := rootObject.Lookup("contents").Object()
	if !ok {
		return fmt.Errorf("%w: missing custom tab contents", ErrInvalidMetadata)
	}
	browse, ok := contents.Lookup("twoColumnBrowseResultsRenderer").Object()
	if !ok {
		return fmt.Errorf("%w: missing custom tab browse renderer", ErrInvalidMetadata)
	}
	tabs, ok := browse.Lookup("tabs").ListValue()
	if !ok || len(tabs) == 0 {
		return fmt.Errorf("%w: missing custom tab list", ErrInvalidMetadata)
	}
	for _, item := range tabs {
		itemObject, ok := item.Object()
		if !ok {
			continue
		}
		for _, rendererName := range []string{"tabRenderer", "expandableTabRenderer"} {
			renderer, ok := itemObject.Lookup(rendererName).Object()
			if !ok {
				continue
			}
			selected, _ := renderer.Lookup("selected").Bool()
			if !selected {
				continue
			}
			rawURL := objectString(renderer, "endpoint", "commandMetadata", "webCommandMetadata", "url")
			if rawURL == "" {
				rawURL = objectString(renderer, "endpoint", "browseEndpoint", "canonicalBaseUrl")
			}
			if rawURL == "" {
				return fmt.Errorf("%w: custom tab missing attributable endpoint", ErrInvalidMetadata)
			}
			if err := youtubeValidateCustomTabEndpoint(rawURL, identity); err != nil {
				return err
			}
			browseID := objectString(renderer, "endpoint", "browseEndpoint", "browseId")
			if err := youtubeValidateCustomTabBrowseID(browseID, identity); err != nil {
				return err
			}
			params := objectString(renderer, "endpoint", "browseEndpoint", "params")
			if strings.ContainsAny(params, "\x00\r\n") || len(params) > 512 {
				return fmt.Errorf("%w: hostile custom tab params", ErrInvalidMetadata)
			}
			return nil
		}
	}
	return fmt.Errorf("%w: missing selected custom tab", ErrInvalidMetadata)
}
