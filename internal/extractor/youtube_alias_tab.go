package extractor

// This file implements a deliberately bounded subset of YoutubeTabIE for the
// two legacy public channel alias routes. It shares renderer and continuation
// parsing with youtube_handle_tab.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const youtubeAliasMaxBytes = 100

var (
	ErrYouTubeAliasTabRateLimited = errors.New("YouTube alias tab rate limited")
	ErrYouTubeAliasTabNetwork     = errors.New("YouTube alias tab network failure")
)

type YouTubeAliasTab struct{}

func NewYouTubeAliasTab() YouTubeAliasTab { return YouTubeAliasTab{} }
func (YouTubeAliasTab) Name() string      { return "youtube_alias_tab" }

func (YouTubeAliasTab) Suitable(parsed *url.URL) bool {
	_, _, _, ok := youtubeAliasTabTarget(parsed)
	return ok
}

func (YouTubeAliasTab) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube alias tab URL", ErrUnsupported)
	}
	kind, alias, tab, ok := youtubeAliasTabTarget(parsed)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube alias tab", ErrUnsupported)
	}
	return extractYouTubeAliasTab(ctx, request.Transport, kind, alias, tab)
}

func youtubeAliasTabTarget(parsed *url.URL) (kind, alias, tab string, ok bool) {
	if parsed == nil || parsed.Fragment != "" || len(parsed.String()) > 4096 {
		return "", "", "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", "", false
	}
	if _, _, err := validateYouTubeURLPolicy(parsed); err != nil {
		return "", "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "youtube.com" && host != "www.youtube.com" {
		return "", "", "", false
	}
	query := strings.ToLower(parsed.RawQuery)
	if strings.Contains(query, "%2f") || strings.Contains(query, "%5c") || strings.Contains(query, "%00") {
		return "", "", "", false
	}
	// net/url preserves some alternate encodings in RawPath. Accept only the
	// canonical decoded spelling; safely decoded Unicode and literal percent
	// aliases are re-escaped by youtubeAliasTabCanonicalURL.
	if parsed.RawPath != "" && parsed.RawPath != parsed.Path {
		return "", "", "", false
	}
	parts := strings.Split(parsed.Path, "/")
	if len(parts) != 4 || parts[0] != "" || (parts[1] != "user" && parts[1] != "c") {
		return "", "", "", false
	}
	alias = parts[2]
	if alias == "" || alias == "." || alias == ".." || len(alias) > youtubeAliasMaxBytes ||
		!utf8.ValidString(alias) || strings.ContainsAny(alias, `/\`) {
		return "", "", "", false
	}
	for _, r := range alias {
		if unicode.IsControl(r) {
			return "", "", "", false
		}
	}
	switch parts[3] {
	case "videos", "shorts", "streams", "playlists":
		return parts[1], alias, parts[3], true
	default:
		return "", "", "", false
	}
}

func extractYouTubeAliasTab(ctx context.Context, transport Transport, kind, alias, tab string) (Extraction, error) {
	canonical := youtubeAliasTabCanonicalURL(kind, alias, tab)
	page, _, err := transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeAliasTabError(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube alias tab initial data", ErrInvalidMetadata)
	}
	if err := validateYouTubeAliasSelectedTab(raw, tab); err != nil {
		return Extraction{}, err
	}
	parsed, err := parseYouTubeHandleTabData(raw, tab)
	if err != nil {
		return Extraction{}, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubeAliasTabAlertError(parsed.alert)
	}
	if parsed.title == "" {
		return Extraction{}, fmt.Errorf("%w: missing YouTube alias tab metadata", ErrInvalidMetadata)
	}
	id := kind + ":" + alias
	if youtubeChannelIDPattern.MatchString(parsed.channelID) {
		id = parsed.channelID
	}
	config := extractYouTubePlaylistConfig(page)
	visitorData := parsed.visitorData
	if visitorData == "" {
		visitorData = config.VisitorData
	}
	entries, err := StatefulContinuationEntries(parsed.entries, parsed.continuation, visitorData, func(ctx context.Context, token, visitorData string) ([]Entry, string, string, error) {
		return fetchYouTubeTabContinuation(ctx, transport, token, visitorData, config, tab, "alias", categorizeYouTubeAliasTabError)
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(parsed.title)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	)), entries)
}

func youtubeAliasTabCanonicalURL(kind, alias, tab string) string {
	return (&url.URL{
		Scheme: "https",
		Host:   "www.youtube.com",
		Path:   "/" + kind + "/" + alias + "/" + tab,
	}).String()
}

// validateYouTubeAliasSelectedTab fails closed only when the initial response
// contains a decisive tabs array. Continuation payloads intentionally do not
// pass through this function.
func validateYouTubeAliasSelectedTab(data []byte, requested string) error {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("%w: decode YouTube alias tab data", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return fmt.Errorf("%w: YouTube alias tab root", ErrInvalidMetadata)
	}
	contents, ok := rootObject.Lookup("contents").Object()
	if !ok {
		return nil
	}
	browse, ok := contents.Lookup("twoColumnBrowseResultsRenderer").Object()
	if !ok {
		return nil
	}
	tabs, ok := browse.Lookup("tabs").ListValue()
	if !ok || len(tabs) == 0 {
		return nil
	}
	var selected *value.Object
	for _, item := range tabs {
		itemObject, itemOK := item.Object()
		if !itemOK {
			continue
		}
		for _, rendererName := range []string{"tabRenderer", "expandableTabRenderer"} {
			renderer, rendererOK := itemObject.Lookup(rendererName).Object()
			if !rendererOK {
				continue
			}
			isSelected, _ := renderer.Lookup("selected").Bool()
			if isSelected {
				if selected != nil {
					return fmt.Errorf("%w: multiple selected YouTube alias tabs", ErrInvalidMetadata)
				}
				selected = renderer
			}
		}
	}
	if selected == nil {
		return fmt.Errorf("%w: missing selected YouTube alias tab", ErrInvalidMetadata)
	}
	identities := youtubeSelectedTabIdentities(selected)
	if len(identities) == 0 {
		return fmt.Errorf("%w: unknown selected YouTube alias tab identity", ErrInvalidMetadata)
	}
	identity := identities[0]
	for _, candidate := range identities[1:] {
		if candidate != identity {
			return fmt.Errorf("%w: conflicting selected YouTube alias tab identity", ErrInvalidMetadata)
		}
	}
	if identity != requested {
		return fmt.Errorf("%w: selected YouTube alias tab %q does not match %q", ErrInvalidPlaylist, identity, requested)
	}
	return nil
}

func youtubeSelectedTabIdentities(renderer *value.Object) []string {
	var identities []string
	appendIdentity := func(identity string) {
		if identity == "" {
			return
		}
		for _, existing := range identities {
			if existing == identity {
				return
			}
		}
		identities = append(identities, identity)
	}
	identifier := strings.ToLower(objectString(renderer, "tabIdentifier"))
	for _, tab := range []string{"videos", "shorts", "streams", "playlists"} {
		if identifier == tab || identifier == "fe"+tab {
			appendIdentity(tab)
		}
	}
	if identifier == "live" || identifier == "felive" {
		appendIdentity("streams")
	}
	for _, candidate := range []string{
		objectString(renderer, "endpoint", "browseEndpoint", "canonicalBaseUrl"),
		objectString(renderer, "endpoint", "commandMetadata", "webCommandMetadata", "url"),
	} {
		if parsed, err := url.Parse(candidate); err == nil {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) != 0 {
				switch parts[len(parts)-1] {
				case "videos", "shorts", "streams", "playlists":
					appendIdentity(parts[len(parts)-1])
				case "live":
					appendIdentity("streams")
				}
			}
		}
	}
	title, _ := renderer.Lookup("title").StringValue()
	if title == "" {
		title = rendererText(renderer.Lookup("title"))
	}
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "videos":
		appendIdentity("videos")
	case "shorts":
		appendIdentity("shorts")
	case "streams", "live":
		appendIdentity("streams")
	case "playlists":
		appendIdentity("playlists")
	}
	return identities
}

func categorizeYouTubeAliasTabError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrInvalidPlaylist) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrUnavailable) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests:
			return ErrYouTubeAliasTabRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeAliasTabNetwork)
}

func youtubeAliasTabAlertError(alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "private") || strings.Contains(lower, "sign in") || strings.Contains(lower, "login") {
		return fmt.Errorf("%w: alias tab access denied", ErrAuthentication)
	}
	return fmt.Errorf("%w: alias tab unavailable", ErrUnavailable)
}
