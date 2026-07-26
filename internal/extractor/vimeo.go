package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const vimeoImpersonationProfile = "chrome-133"

var ErrVimeoPlaylistNetwork = errors.New("Vimeo playlist network failure")

const (
	vimeoMaxTextTracks = 128
	vimeoMaxTextURL    = 8192
	vimeoMaxTextLang   = 64
	vimeoMaxTextName   = 256
	vimeoMaxConfigURL  = 8192

	vimeoMaxSlugBytes         = 64
	vimeoMaxPlaylistTitle     = 512
	vimeoMaxEntryTitle        = 512
	vimeoMaxPageBytes         = 4 << 20
	vimeoMaxPlaylistPages     = 100
	vimeoMaxClipsPerPage      = 128
	vimeoMaxPlaylistEntries   = 10_000
	vimeoClipLookaheadBytes   = 2048
	vimeoMaxNumericVideoIDLen = 20
)

type vimeoRouteKind int

const (
	vimeoRouteNone vimeoRouteKind = iota
	vimeoRouteVideo
	vimeoRouteChannel
	vimeoRouteUserVideos
	vimeoRouteGroup
	vimeoRouteAlbum
)

var (
	vimeoURLPattern       = regexp.MustCompile(`^/(?:video/)?([0-9]+)(?:/)?$`)
	vimeoConfigURLPattern = regexp.MustCompile(`(?i)\bdata-config-url=["']([^"']+)`)
	vimeoSlugPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_-]{0,62}[A-Za-z0-9])?$`)
	vimeoNumericPattern   = regexp.MustCompile(`^[0-9]+$`)
)

// Reserved path segments that must never be treated as public user slugs.
// Channel routes use /channels/{slug} and do not consult this set.
var vimeoReservedUserSlugs = map[string]struct{}{
	"watchlater": {}, "channels": {}, "album": {}, "showcase": {}, "groups": {},
	"ondemand": {}, "home": {}, "log_in": {}, "login": {}, "join": {}, "search": {},
	"store": {}, "upload": {}, "settings": {}, "about": {}, "videos": {}, "likes": {},
	"live": {}, "features": {}, "solutions": {}, "enterprise": {}, "create": {},
	"watch": {}, "manage": {}, "stock": {}, "school": {}, "tv": {}, "plus": {},
	"go": {}, "ott": {}, "help": {}, "privacy": {}, "terms": {}, "cookies": {},
	"review": {}, "event": {}, "events": {}, "user": {}, "users": {}, "me": {},
	"messages": {}, "notifications": {}, "stats": {}, "analytics": {},
}

type vimeoPlaylistTarget struct {
	kind      vimeoRouteKind
	id        string
	slug      string
	canonical string
	baseURL   string
}

type Vimeo struct{}

func NewVimeo() Vimeo { return Vimeo{} }

func (Vimeo) Name() string { return "vimeo" }

func (Vimeo) Suitable(parsed *url.URL) bool {
	kind, _ := classifyVimeoURL(parsed)
	return kind != vimeoRouteNone
}

func (Vimeo) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	kind, target := classifyVimeoURL(parsed)
	switch kind {
	case vimeoRouteChannel, vimeoRouteUserVideos, vimeoRouteGroup:
		return extractVimeoPlaylist(ctx, request.Transport, target)
	case vimeoRouteAlbum:
		return extractVimeoAlbumPlaylist(ctx, request.Transport, target)
	case vimeoRouteVideo:
		return extractVimeoVideo(ctx, request, target.id, target.canonical)
	default:
		return Extraction{}, ErrUnsupported
	}
}

func extractVimeoVideo(ctx context.Context, request Request, videoID, contextualURL string) (Extraction, error) {
	// Never reflect caller query credentials into the webpage request or the
	// config Referer. Contextual routes preserve only their validated path.
	webpageURL := contextualURL
	if webpageURL == "" {
		webpageURL = "https://vimeo.com/" + videoID
	}
	page, _, err := ReadPageWithProfile(ctx, request.Transport, webpageURL, vimeoImpersonationProfile)
	if err != nil {
		return Extraction{}, err
	}
	config, err := extractVimeoConfig(ctx, request.Transport, webpageURL, page)
	if err != nil {
		return Extraction{}, err
	}
	return parseVimeoConfigContext(ctx, config, videoID, webpageURL)
}

func classifyVimeoURL(parsed *url.URL) (vimeoRouteKind, vimeoPlaylistTarget) {
	if parsed == nil {
		return vimeoRouteNone, vimeoPlaylistTarget{}
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "player.vimeo.com" {
		if match := vimeoURLPattern.FindStringSubmatch(parsed.Path); len(match) == 2 {
			return vimeoRouteVideo, vimeoPlaylistTarget{kind: vimeoRouteVideo, id: match[1]}
		}
		return vimeoRouteNone, vimeoPlaylistTarget{}
	}
	if host != "vimeo.com" && host != "www.vimeo.com" {
		return vimeoRouteNone, vimeoPlaylistTarget{}
	}
	if match := vimeoURLPattern.FindStringSubmatch(parsed.Path); len(match) == 2 {
		return vimeoRouteVideo, vimeoPlaylistTarget{kind: vimeoRouteVideo, id: match[1]}
	}
	if target, ok := classifyVimeoContextVideoURL(parsed); ok {
		return vimeoRouteVideo, target
	}
	if target, ok := classifyVimeoAlbumURL(parsed); ok {
		return vimeoRouteAlbum, target
	}
	if target, ok := classifyVimeoPlaylistURL(parsed); ok {
		return target.kind, target
	}
	return vimeoRouteNone, vimeoPlaylistTarget{}
}

func classifyVimeoContextVideoURL(parsed *url.URL) (vimeoPlaylistTarget, bool) {
	if parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		!vimeoPlaylistPathOK(parsed) || strings.Contains(parsed.String(), "\x00") {
		return vimeoPlaylistTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "vimeo.com" && host != "www.vimeo.com" {
		return vimeoPlaylistTarget{}, false
	}
	parts := splitVimeoPath(parsed.Path)
	var contextPath, videoID string
	switch {
	case len(parts) == 3 && parts[0] == "channels":
		slug, ok := validVimeoSlug(parts[1], false)
		if !ok {
			return vimeoPlaylistTarget{}, false
		}
		contextPath, videoID = "channels/"+slug, parts[2]
	case len(parts) == 4 && parts[0] == "groups" && parts[2] == "videos":
		slug, ok := validVimeoSlug(parts[1], false)
		if !ok {
			return vimeoPlaylistTarget{}, false
		}
		contextPath, videoID = "groups/"+slug+"/videos", parts[3]
	case len(parts) == 4 && (parts[0] == "album" || parts[0] == "showcase") && parts[2] == "video":
		collectionID, ok := validVimeoSlug(parts[1], false)
		if !ok {
			return vimeoPlaylistTarget{}, false
		}
		contextPath, videoID = parts[0]+"/"+collectionID+"/video", parts[3]
	default:
		return vimeoPlaylistTarget{}, false
	}
	if !validVimeoNumericVideoID(videoID) {
		return vimeoPlaylistTarget{}, false
	}
	return vimeoPlaylistTarget{
		kind:      vimeoRouteVideo,
		id:        videoID,
		canonical: "https://vimeo.com/" + contextPath + "/" + videoID,
	}, true
}

func validVimeoNumericVideoID(videoID string) bool {
	return videoID != "" && len(videoID) <= vimeoMaxNumericVideoIDLen && vimeoNumericPattern.MatchString(videoID)
}

func classifyVimeoPlaylistURL(parsed *url.URL) (vimeoPlaylistTarget, bool) {
	if parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		!vimeoPlaylistPathOK(parsed) || strings.Contains(parsed.String(), "\x00") {
		return vimeoPlaylistTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "vimeo.com" && host != "www.vimeo.com" {
		return vimeoPlaylistTarget{}, false
	}
	parts := splitVimeoPath(parsed.Path)
	switch {
	case len(parts) == 1:
		slug, ok := validVimeoSlug(parts[0], true)
		if !ok {
			return vimeoPlaylistTarget{}, false
		}
		return vimeoPlaylistTarget{
			kind:      vimeoRouteUserVideos,
			id:        slug,
			canonical: "https://vimeo.com/" + slug,
			baseURL:   "https://vimeo.com/" + slug,
		}, true
	case len(parts) == 2 && parts[0] == "channels":
		slug, ok := validVimeoSlug(parts[1], false)
		if !ok {
			return vimeoPlaylistTarget{}, false
		}
		return vimeoPlaylistTarget{
			kind:      vimeoRouteChannel,
			id:        slug,
			canonical: "https://vimeo.com/channels/" + slug,
			baseURL:   "https://vimeo.com/channels/" + slug,
		}, true
	case len(parts) == 2 && parts[1] == "videos":
		slug, ok := validVimeoSlug(parts[0], true)
		if !ok {
			return vimeoPlaylistTarget{}, false
		}
		return vimeoPlaylistTarget{
			kind:      vimeoRouteUserVideos,
			id:        slug,
			canonical: "https://vimeo.com/" + slug + "/videos",
			baseURL:   "https://vimeo.com/" + slug,
		}, true
	case len(parts) == 2 && parts[0] == "groups":
		slug, ok := validVimeoSlug(parts[1], false)
		if !ok {
			return vimeoPlaylistTarget{}, false
		}
		return vimeoPlaylistTarget{
			kind:      vimeoRouteGroup,
			id:        slug,
			canonical: "https://vimeo.com/groups/" + slug,
			baseURL:   "https://vimeo.com/groups/" + slug,
		}, true
	default:
		return vimeoPlaylistTarget{}, false
	}
}

func splitVimeoPath(rawPath string) []string {
	trimmed := strings.Trim(rawPath, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil
		}
		out = append(out, part)
	}
	return out
}

// vimeoPlaylistPathOK accepts an optional trailing slash while rejecting
// encoded separators, dots, NULs, and other unclean path encodings.
func vimeoPlaylistPathOK(parsed *url.URL) bool {
	if parsed == nil || parsed.RawPath != "" || parsed.Path == "" || strings.Contains(parsed.Path, "\x00") {
		return false
	}
	cleaned := path.Clean(parsed.Path)
	if parsed.Path != cleaned && parsed.Path != cleaned+"/" {
		return false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	return !strings.Contains(escaped, "%2f") && !strings.Contains(escaped, "%5c") &&
		!strings.Contains(escaped, "%00") && !strings.Contains(escaped, "%2e") &&
		!strings.Contains(escaped, "%25")
}

func validVimeoSlug(slug string, userRoute bool) (string, bool) {
	if slug == "" || len(slug) > vimeoMaxSlugBytes || !vimeoSlugPattern.MatchString(slug) || strings.ContainsRune(slug, '\x00') {
		return "", false
	}
	if userRoute {
		if vimeoNumericPattern.MatchString(slug) {
			return "", false
		}
		if _, reserved := vimeoReservedUserSlugs[strings.ToLower(slug)]; reserved {
			return "", false
		}
	}
	return slug, true
}

func extractVimeoPlaylist(ctx context.Context, transport Transport, target vimeoPlaylistTarget) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if transport == nil {
		return Extraction{}, fmt.Errorf("%w: missing transport", ErrInvalidPlaylist)
	}
	firstPage, err := fetchVimeoPlaylistPage(ctx, transport, target, 1)
	if err != nil {
		return Extraction{}, err
	}
	parsed, err := parseVimeoPlaylistPage(ctx, firstPage)
	if err != nil {
		return Extraction{}, err
	}
	if len(parsed.entries) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing Vimeo playlist entries", ErrInvalidPlaylist)
	}
	title := extractVimeoPlaylistTitle(firstPage, target.kind)
	if title == "" {
		title = vimeoPlaylistFallbackTitle(target)
	}
	seen := make(map[string]struct{}, len(parsed.entries))
	first := make([]Entry, 0, len(parsed.entries))
	for _, entry := range parsed.entries {
		if _, dup := seen[entry.ID]; dup {
			continue
		}
		seen[entry.ID] = struct{}{}
		first = append(first, entry)
	}
	if len(first) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing Vimeo playlist entries", ErrInvalidPlaylist)
	}
	sequence := vimeoPlaylistEntries{
		transport: transport,
		target:    target,
		first:     append([]Entry(nil), first...),
		hasMore:   parsed.hasNext,
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
	)
	return Playlist(value.NewInfo(info), sequence)
}

type vimeoPlaylistEntries struct {
	transport Transport
	target    vimeoPlaylistTarget
	first     []Entry
	hasMore   bool
}

func (entries vimeoPlaylistEntries) Iterator() EntryIterator {
	seen := make(map[string]struct{}, len(entries.first))
	for _, entry := range entries.first {
		seen[entry.ID] = struct{}{}
	}
	return &vimeoPlaylistIterator{
		transport: entries.transport,
		target:    entries.target,
		page:      append([]Entry(nil), entries.first...),
		pageNum:   1,
		hasMore:   entries.hasMore,
		seen:      seen,
		total:     len(entries.first),
	}
}

type vimeoPlaylistIterator struct {
	transport Transport
	target    vimeoPlaylistTarget
	page      []Entry
	pageIndex int
	pageNum   int
	hasMore   bool
	seen      map[string]struct{}
	total     int
	done      bool
}

func (iterator *vimeoPlaylistIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.done = true
		return Entry{}, false, err
	}
	if iterator.done {
		return Entry{}, false, nil
	}
	for iterator.pageIndex >= len(iterator.page) {
		if !iterator.hasMore {
			iterator.done = true
			return Entry{}, false, nil
		}
		nextPage := iterator.pageNum + 1
		if nextPage > vimeoMaxPlaylistPages {
			iterator.done = true
			return Entry{}, false, fmt.Errorf("%w: Vimeo playlist page bound", ErrPlaylistLimit)
		}
		if iterator.total >= vimeoMaxPlaylistEntries {
			iterator.done = true
			return Entry{}, false, fmt.Errorf("%w: Vimeo playlist entry bound", ErrPlaylistLimit)
		}
		raw, err := fetchVimeoPlaylistPage(ctx, iterator.transport, iterator.target, nextPage)
		if err != nil {
			iterator.done = true
			return Entry{}, false, err
		}
		parsed, err := parseVimeoPlaylistPage(ctx, raw)
		if err != nil {
			iterator.done = true
			return Entry{}, false, err
		}
		entries := make([]Entry, 0, len(parsed.entries))
		for _, entry := range parsed.entries {
			if err := contextError(ctx); err != nil {
				iterator.done = true
				return Entry{}, false, err
			}
			if _, dup := iterator.seen[entry.ID]; dup {
				continue
			}
			if iterator.total+len(entries) >= vimeoMaxPlaylistEntries {
				iterator.done = true
				return Entry{}, false, fmt.Errorf("%w: Vimeo playlist entry bound", ErrPlaylistLimit)
			}
			iterator.seen[entry.ID] = struct{}{}
			entries = append(entries, entry)
		}
		iterator.page = entries
		iterator.pageIndex = 0
		iterator.pageNum = nextPage
		iterator.hasMore = parsed.hasNext
		iterator.total += len(entries)
		if len(entries) == 0 && !iterator.hasMore {
			iterator.done = true
			return Entry{}, false, nil
		}
	}
	entry := iterator.page[iterator.pageIndex]
	iterator.pageIndex++
	return entry, true, nil
}

func fetchVimeoPlaylistPage(ctx context.Context, transport Transport, target vimeoPlaylistTarget, pageNum int) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if pageNum < 1 || pageNum > vimeoMaxPlaylistPages {
		return nil, fmt.Errorf("%w: Vimeo playlist page bound", ErrPlaylistLimit)
	}
	pageURL := fmt.Sprintf("%s/videos/page:%d/", target.baseURL, pageNum)
	page, _, err := ReadPageWithProfileWithoutCredentialsNoRedirect(ctx, transport, pageURL, vimeoImpersonationProfile)
	if err != nil {
		return nil, categorizeVimeoPlaylistTransportError(err)
	}
	if len(page) == 0 {
		return nil, fmt.Errorf("%w: empty Vimeo playlist page", ErrInvalidPlaylist)
	}
	if len(page) > vimeoMaxPageBytes {
		return nil, fmt.Errorf("%w: Vimeo playlist page", ErrJSONResponseTooLarge)
	}
	return page, nil
}

func categorizeVimeoPlaylistTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTransportProfile) || errors.Is(err, ErrTransportIsolation) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrInvalidPlaylist) || errors.Is(err, ErrPlaylistLimit) ||
		errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRegionRestricted) ||
		errors.Is(err, ErrVimeoPlaylistNetwork) {
		return err
	}
	if errors.Is(err, network.ErrPageTooLarge) {
		return fmt.Errorf("%w: Vimeo playlist page", ErrJSONResponseTooLarge)
	}
	if errors.Is(err, network.ErrImpersonationUnavailable) {
		return fmt.Errorf("%w: %s", ErrTransportProfile, vimeoImpersonationProfile)
	}
	var httpStatus *HTTPStatusError
	if errors.As(err, &httpStatus) {
		switch httpStatus.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		default:
			return ErrVimeoPlaylistNetwork
		}
	}
	var networkStatus *network.StatusError
	if errors.As(err, &networkStatus) {
		switch networkStatus.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		default:
			return ErrVimeoPlaylistNetwork
		}
	}
	// Opaque sentinel: never echo transport/body/URL details that may carry tokens.
	return ErrVimeoPlaylistNetwork
}

type vimeoPlaylistPage struct {
	entries []Entry
	hasNext bool
}

func parseVimeoPlaylistPage(ctx context.Context, page []byte) (vimeoPlaylistPage, error) {
	if err := contextError(ctx); err != nil {
		return vimeoPlaylistPage{}, err
	}
	if len(page) == 0 {
		return vimeoPlaylistPage{}, fmt.Errorf("%w: empty Vimeo playlist page", ErrInvalidPlaylist)
	}
	if len(page) > vimeoMaxPageBytes {
		return vimeoPlaylistPage{}, fmt.Errorf("%w: Vimeo playlist page", ErrJSONResponseTooLarge)
	}
	hasNext := vimeoPlaylistHasNext(page)
	entries, err := parseVimeoPlaylistClips(ctx, page)
	if err != nil {
		return vimeoPlaylistPage{}, err
	}
	return vimeoPlaylistPage{entries: entries, hasNext: hasNext}, nil
}

func vimeoPlaylistHasNext(page []byte) bool {
	// Bounded indicator matching the pinned VimeoChannelIE intent: an anchor
	// that declares rel=next. Arbitrary page-declared URLs are never followed.
	for _, marker := range []string{`rel="next"`, `rel='next'`} {
		search := page
		for {
			idx := bytes.Index(search, []byte(marker))
			if idx < 0 {
				break
			}
			abs := len(page) - len(search) + idx
			if vimeoRelNextInsideAnchor(page, abs) {
				return true
			}
			search = search[idx+len(marker):]
		}
	}
	return false
}

func vimeoRelNextInsideAnchor(page []byte, relIdx int) bool {
	start := relIdx
	for start > 0 && page[start] != '<' {
		start--
		if relIdx-start > 512 {
			return false
		}
	}
	if start >= len(page) || page[start] != '<' {
		return false
	}
	if !bytes.HasPrefix(bytes.ToLower(page[start:min(start+3, len(page))]), []byte("<a")) {
		return false
	}
	if bytes.IndexByte(page[start:relIdx], '>') >= 0 {
		return false
	}
	return true
}

func parseVimeoPlaylistClips(ctx context.Context, page []byte) ([]Entry, error) {
	entries, sawCandidateAnchor, err := parseVimeoPlaylistClipAnchors(ctx, page)
	if err != nil {
		return nil, err
	}
	// Marker fallback is only for pages that declare clip_IDs without any
	// candidate anchors. Hostile/mismatched/cross-origin anchors must not be
	// reintroduced by bare ID emission.
	if sawCandidateAnchor {
		return entries, nil
	}
	return parseVimeoPlaylistClipMarkers(ctx, page)
}

func parseVimeoPlaylistClipAnchors(ctx context.Context, page []byte) ([]Entry, bool, error) {
	entries := make([]Entry, 0)
	seen := make(map[string]struct{})
	sawCandidateAnchor := false
	offset := 0
	steps := 0
	for offset < len(page) {
		if steps%32 == 0 {
			if err := contextError(ctx); err != nil {
				return nil, false, err
			}
		}
		steps++
		id, idEnd, next, ok := findVimeoClipID(page, offset)
		if !ok {
			break
		}
		offset = next
		if _, dup := seen[id]; dup {
			continue
		}
		windowEnd := idEnd + vimeoClipLookaheadBytes
		if windowEnd > len(page) {
			windowEnd = len(page)
		}
		window := page[idEnd:windowEnd]
		if _, _, found := findVimeoClipCandidateAnchor(window); found {
			sawCandidateAnchor = true
		}
		href, title, found := findVimeoClipAnchor(window, id)
		if !found {
			continue
		}
		entry, ok := vimeoPlaylistEntry(id, href, title)
		if !ok {
			continue
		}
		if len(entries) >= vimeoMaxClipsPerPage {
			return nil, false, fmt.Errorf("%w: Vimeo playlist page clip bound", ErrInvalidPlaylist)
		}
		seen[id] = struct{}{}
		entries = append(entries, entry)
	}
	return entries, sawCandidateAnchor, nil
}

func parseVimeoPlaylistClipMarkers(ctx context.Context, page []byte) ([]Entry, error) {
	entries := make([]Entry, 0)
	seen := make(map[string]struct{})
	offset := 0
	steps := 0
	for offset < len(page) {
		if steps%32 == 0 {
			if err := contextError(ctx); err != nil {
				return nil, err
			}
		}
		steps++
		id, _, next, ok := findVimeoClipID(page, offset)
		if !ok {
			break
		}
		offset = next
		if _, dup := seen[id]; dup {
			continue
		}
		entry, ok := vimeoPlaylistEntry(id, "", "")
		if !ok {
			continue
		}
		if len(entries) >= vimeoMaxClipsPerPage {
			return nil, fmt.Errorf("%w: Vimeo playlist page clip bound", ErrInvalidPlaylist)
		}
		seen[id] = struct{}{}
		entries = append(entries, entry)
	}
	return entries, nil
}

func findVimeoClipID(page []byte, offset int) (id string, idEnd, next int, ok bool) {
	for offset < len(page) {
		idx := bytes.Index(page[offset:], []byte("clip_"))
		if idx < 0 {
			return "", 0, len(page), false
		}
		abs := offset + idx
		if abs == 0 || (page[abs-1] != '"' && page[abs-1] != '\'') {
			offset = abs + 5
			continue
		}
		quoteIdx := abs - 1
		eq := quoteIdx - 1
		for eq >= 0 && (page[eq] == ' ' || page[eq] == '\t') {
			eq--
		}
		if eq < 0 || page[eq] != '=' {
			offset = abs + 5
			continue
		}
		nameEnd := eq
		nameStart := nameEnd
		for nameStart > 0 {
			c := page[nameStart-1]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-' {
				nameStart--
				continue
			}
			break
		}
		if !bytes.EqualFold(page[nameStart:nameEnd], []byte("id")) {
			offset = abs + 5
			continue
		}
		digitStart := abs + 5
		digitEnd := digitStart
		for digitEnd < len(page) && page[digitEnd] >= '0' && page[digitEnd] <= '9' {
			digitEnd++
		}
		if digitEnd == digitStart || digitEnd-digitStart > vimeoMaxNumericVideoIDLen {
			offset = abs + 5
			continue
		}
		quote := page[quoteIdx]
		if digitEnd >= len(page) || page[digitEnd] != quote {
			offset = abs + 5
			continue
		}
		return string(page[digitStart:digitEnd]), digitEnd + 1, digitEnd + 1, true
	}
	return "", 0, len(page), false
}

func findVimeoClipCandidateAnchor(window []byte) (href, title string, ok bool) {
	search := window
	for len(search) > 0 {
		idx := indexVimeoAnchorStart(search)
		if idx < 0 {
			return "", "", false
		}
		tagEndRel := bytes.IndexByte(search[idx:], '>')
		if tagEndRel < 0 {
			return "", "", false
		}
		tag := search[idx : idx+tagEndRel]
		hrefVal, hasHref := vimeoHTMLAttr(tag, "href")
		if !hasHref {
			search = search[idx+2:]
			continue
		}
		titleVal, _ := vimeoHTMLAttr(tag, "title")
		return hrefVal, titleVal, true
	}
	return "", "", false
}

func findVimeoClipAnchor(window []byte, clipID string) (href, title string, ok bool) {
	search := window
	for len(search) > 0 {
		hrefVal, titleVal, found := findVimeoClipCandidateAnchor(search)
		if !found {
			return "", "", false
		}
		if vimeoHrefAgreesWithClipID(hrefVal, clipID) {
			return hrefVal, titleVal, true
		}
		// Advance past this candidate and keep looking for an agreeing href.
		idx := indexVimeoAnchorStart(search)
		if idx < 0 {
			return "", "", false
		}
		search = search[idx+2:]
	}
	return "", "", false
}

func indexVimeoAnchorStart(page []byte) int {
	for i := 0; i+1 < len(page); i++ {
		if page[i] != '<' {
			continue
		}
		if page[i+1] == 'a' || page[i+1] == 'A' {
			if i+2 == len(page) {
				return i
			}
			next := page[i+2]
			if next == ' ' || next == '\t' || next == '\n' || next == '\r' || next == '>' || next == '/' {
				return i
			}
		}
	}
	return -1
}

func vimeoHTMLAttr(tag []byte, name string) (string, bool) {
	lowerName := strings.ToLower(name)
	search := tag
	for len(search) > 0 {
		idx := bytes.IndexByte(search, '=')
		if idx < 0 {
			return "", false
		}
		nameEnd := idx
		nameStart := nameEnd
		for nameStart > 0 {
			c := search[nameStart-1]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
				nameStart--
				continue
			}
			break
		}
		attrName := strings.ToLower(string(search[nameStart:nameEnd]))
		rest := bytes.TrimLeft(search[idx+1:], " \t\r\n")
		if len(rest) == 0 {
			return "", false
		}
		var raw string
		switch rest[0] {
		case '"':
			end := bytes.IndexByte(rest[1:], '"')
			if end < 0 {
				return "", false
			}
			raw = string(rest[1 : 1+end])
			search = rest[2+end:]
		case '\'':
			end := bytes.IndexByte(rest[1:], '\'')
			if end < 0 {
				return "", false
			}
			raw = string(rest[1 : 1+end])
			search = rest[2+end:]
		default:
			end := 0
			for end < len(rest) && rest[end] != ' ' && rest[end] != '\t' && rest[end] != '>' {
				end++
			}
			raw = string(rest[:end])
			search = rest[end:]
		}
		if attrName == lowerName {
			return html.UnescapeString(raw), true
		}
	}
	return "", false
}

func vimeoHrefAgreesWithClipID(rawHref, clipID string) bool {
	if rawHref == "" || clipID == "" || strings.ContainsAny(rawHref, "\\\x00\r\n") || len(rawHref) > vimeoMaxConfigURL {
		return false
	}
	parsed, err := url.Parse(rawHref)
	if err != nil || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	if parsed.IsAbs() {
		if parsed.Scheme != "https" && parsed.Scheme != "http" {
			return false
		}
		host := strings.ToLower(parsed.Hostname())
		if host != "vimeo.com" && host != "www.vimeo.com" {
			return false
		}
	} else if parsed.Host != "" {
		return false
	}
	if parsed.Path == "" || path.Clean(parsed.Path) != parsed.Path {
		return false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") ||
		strings.Contains(escaped, "%00") || strings.Contains(escaped, "%2e") ||
		strings.Contains(escaped, "%25") {
		return false
	}
	parts := splitVimeoPath(parsed.Path)
	if len(parts) == 0 {
		return false
	}
	return parts[len(parts)-1] == clipID && vimeoNumericPattern.MatchString(clipID)
}

func vimeoPlaylistEntry(id, href, title string) (Entry, bool) {
	if !vimeoNumericPattern.MatchString(id) || len(id) == 0 || len(id) > vimeoMaxNumericVideoIDLen {
		return Entry{}, false
	}
	if href != "" && !vimeoHrefAgreesWithClipID(href, id) {
		return Entry{}, false
	}
	cleanTitle := boundedVimeoPlaylistText(title, vimeoMaxEntryTitle)
	return Entry{
		URL:          "https://vimeo.com/" + id,
		ExtractorKey: "vimeo",
		ID:           id,
		Title:        cleanTitle,
		Transparent:  true,
	}, true
}

func extractVimeoPlaylistTitle(page []byte, kind vimeoRouteKind) string {
	switch kind {
	case vimeoRouteChannel, vimeoRouteGroup:
		return extractVimeoChannelListTitle(page)
	case vimeoRouteUserVideos:
		return extractVimeoUserListTitle(page)
	default:
		return ""
	}
}

func extractVimeoChannelListTitle(page []byte) string {
	offset := 0
	for offset < len(page) {
		idx := indexASCIITag(page[offset:], "link")
		if idx < 0 {
			return ""
		}
		abs := offset + idx
		tagEnd := bytes.IndexByte(page[abs:], '>')
		if tagEnd < 0 {
			return ""
		}
		tag := page[abs : abs+tagEnd]
		rel, hasRel := vimeoHTMLAttr(tag, "rel")
		title, hasTitle := vimeoHTMLAttr(tag, "title")
		if hasRel && hasTitle && strings.EqualFold(strings.TrimSpace(rel), "alternate") {
			return boundedVimeoPlaylistText(title, vimeoMaxPlaylistTitle)
		}
		offset = abs + 5
	}
	return ""
}

func extractVimeoUserListTitle(page []byte) string {
	offset := 0
	for offset < len(page) {
		idx := indexVimeoAnchorStart(page[offset:])
		if idx < 0 {
			return ""
		}
		abs := offset + idx
		tagEnd := bytes.IndexByte(page[abs:], '>')
		if tagEnd < 0 {
			return ""
		}
		tag := page[abs : abs+tagEnd]
		className, hasClass := vimeoHTMLAttr(tag, "class")
		if hasClass && classContainsToken(className, "user") {
			contentStart := abs + tagEnd + 1
			contentEndRel := bytes.Index(page[contentStart:], []byte("</"))
			if contentEndRel < 0 || contentEndRel > vimeoMaxPlaylistTitle*4 {
				offset = abs + 2
				continue
			}
			raw := string(page[contentStart : contentStart+contentEndRel])
			if strings.ContainsAny(raw, "<>") {
				offset = abs + 2
				continue
			}
			return boundedVimeoPlaylistText(raw, vimeoMaxPlaylistTitle)
		}
		offset = abs + 2
	}
	return ""
}

func indexASCIITag(page []byte, name string) int {
	if name == "" {
		return -1
	}
	for i := 0; i+1+len(name) <= len(page); i++ {
		if page[i] != '<' {
			continue
		}
		if !bytes.EqualFold(page[i+1:i+1+len(name)], []byte(name)) {
			continue
		}
		if i+1+len(name) == len(page) {
			return i
		}
		next := page[i+1+len(name)]
		if next == ' ' || next == '\t' || next == '\n' || next == '\r' || next == '>' || next == '/' {
			return i
		}
	}
	return -1
}

func classContainsToken(className, token string) bool {
	for _, part := range strings.Fields(className) {
		if strings.EqualFold(part, token) {
			return true
		}
	}
	return false
}

func vimeoPlaylistFallbackTitle(target vimeoPlaylistTarget) string {
	switch target.kind {
	case vimeoRouteChannel:
		return "Vimeo channel " + target.id
	case vimeoRouteUserVideos:
		return "Vimeo user " + target.id
	case vimeoRouteGroup:
		return "Vimeo group " + target.id
	default:
		return "Vimeo playlist " + target.id
	}
}

func boundedVimeoPlaylistText(input string, limit int) string {
	input = strings.TrimSpace(html.UnescapeString(input))
	if input == "" || strings.ContainsRune(input, '\x00') {
		return ""
	}
	var builder strings.Builder
	for _, r := range input {
		if r == '\uFFFD' || (!unicode.IsPrint(r) && !unicode.IsSpace(r)) {
			continue
		}
		if r == '\n' || r == '\r' || r == '\t' {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(r)
	}
	cleaned := strings.Join(strings.Fields(builder.String()), " ")
	if cleaned == "" {
		return ""
	}
	if utf8.RuneCountInString(cleaned) > limit {
		runes := []rune(cleaned)
		cleaned = string(runes[:limit])
	}
	if len(cleaned) > limit*4 {
		return ""
	}
	return cleaned
}

func extractVimeoConfig(ctx context.Context, transport Transport, webpageURL string, page []byte) (vimeoConfig, error) {
	if raw, err := extractJSONObject(page, "playerConfig"); err == nil {
		var config vimeoConfig
		if json.Unmarshal(raw, &config) != nil {
			return vimeoConfig{}, fmt.Errorf("%w: Vimeo player config", ErrInvalidMetadata)
		}
		return config, nil
	}
	configURL := ""
	if match := vimeoConfigURLPattern.FindSubmatch(page); len(match) == 2 {
		configURL = html.UnescapeString(string(match[1]))
	}
	if configURL == "" {
		for _, marker := range []string{"vimeo.clip_page_config", "vimeo.vod_title_page_config"} {
			raw, err := extractJSONObject(page, marker)
			if err != nil {
				continue
			}
			var pageConfig struct {
				Player struct {
					ConfigURL string `json:"config_url"`
				} `json:"player"`
			}
			if json.Unmarshal(raw, &pageConfig) == nil {
				configURL = pageConfig.Player.ConfigURL
			}
			break
		}
	}
	if configURL == "" {
		lower := strings.ToLower(string(page))
		if strings.Contains(lower, "privacy settings") || strings.Contains(lower, "password") || strings.Contains(lower, "log in") {
			return vimeoConfig{}, ErrAuthentication
		}
		return vimeoConfig{}, fmt.Errorf("%w: missing Vimeo config", ErrInvalidMetadata)
	}
	configURL, ok := normalizeVimeoConfigURL(configURL)
	if !ok {
		// Do not include the untrusted URL: config URLs commonly carry tokens.
		return vimeoConfig{}, fmt.Errorf("%w: unsafe Vimeo config URL", ErrInvalidMetadata)
	}
	headers := make(http.Header)
	headers.Set("Referer", webpageURL)
	var config vimeoConfig
	if err := RequestJSON(ctx, transport, http.MethodGet, configURL, nil, headers, &config); err != nil {
		var status *HTTPStatusError
		if errors.As(err, &status) {
			switch status.Code {
			case http.StatusUnauthorized, http.StatusForbidden:
				return vimeoConfig{}, ErrAuthentication
			case http.StatusNotFound, http.StatusGone:
				return vimeoConfig{}, ErrUnavailable
			}
		}
		return vimeoConfig{}, err
	}
	return config, nil
}

// normalizeVimeoConfigURL permits only Vimeo's public player-config origin.
// It intentionally preserves the query because that is where public config
// tokens live, while rejecting every path encoding that could alter routing.
func normalizeVimeoConfigURL(rawURL string) (string, bool) {
	if len(rawURL) == 0 || len(rawURL) > vimeoMaxConfigURL || strings.ContainsAny(rawURL, "\\\x00\r\n") {
		return "", false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path == "" || strings.ToLower(parsed.Hostname()) != "player.vimeo.com" {
		return "", false
	}
	if vimeoUnsafePath(parsed) {
		return "", false
	}
	parsed.Scheme = "https"
	parsed.Host = "player.vimeo.com"
	return parsed.String(), true
}

type vimeoConfig struct {
	View  int `json:"view"`
	Video struct {
		ID          json.Number       `json:"id"`
		Title       string            `json:"title"`
		Description string            `json:"description"`
		Duration    int64             `json:"duration"`
		Width       int64             `json:"width"`
		Height      int64             `json:"height"`
		Thumbs      map[string]string `json:"thumbs"`
		Owner       struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"owner"`
		LiveEvent struct {
			Status string `json:"status"`
		} `json:"live_event"`
		Files vimeoFiles `json:"files"`
	} `json:"video"`
	Request struct {
		Files      vimeoFiles       `json:"files"`
		TextTracks []vimeoTextTrack `json:"text_tracks"`
	} `json:"request"`
}

// vimeoTextTrack is the public player-config shape used for manually supplied
// captions. The pinned yt-dlp implementation uses lang and url; label/name and
// kind are accepted only to make the normalized result useful and safe.
type vimeoTextTrack struct {
	URL      string `json:"url"`
	Language string `json:"lang"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Name     string `json:"name"`
}

type vimeoFiles struct {
	Progressive []struct {
		URL     string `json:"url"`
		Quality string `json:"quality"`
		Width   int64  `json:"width"`
		Height  int64  `json:"height"`
		FPS     int64  `json:"fps"`
		Bitrate int64  `json:"bitrate"`
	} `json:"progressive"`
	HLS struct {
		CDNs map[string]struct {
			URL string `json:"url"`
		} `json:"cdns"`
	} `json:"hls"`
	DASH struct {
		CDNs map[string]struct {
			URL string `json:"url"`
		} `json:"cdns"`
	} `json:"dash"`
}

func parseVimeoConfig(config vimeoConfig, videoID, webpageURL string) (Extraction, error) {
	return parseVimeoConfigContext(context.Background(), config, videoID, webpageURL)
}

func parseVimeoConfigContext(ctx context.Context, config vimeoConfig, videoID, webpageURL string) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if config.View == 4 {
		return Extraction{}, ErrAuthentication
	}
	if config.Video.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Vimeo title", ErrInvalidMetadata)
	}
	files := config.Video.Files
	if len(files.Progressive) == 0 && len(files.HLS.CDNs) == 0 && len(files.DASH.CDNs) == 0 {
		files = config.Request.Files
	}
	formats := make([]value.Value, 0, len(files.Progressive)+len(files.HLS.CDNs)+len(files.DASH.CDNs))
	for _, format := range files.Progressive {
		if err := contextError(ctx); err != nil {
			return Extraction{}, err
		}
		if !validHTTPURL(format.URL) {
			continue
		}
		extension := strings.TrimPrefix(path.Ext(mustURLPath(format.URL)), ".")
		if extension == "" {
			extension = "mp4"
		}
		object := value.NewObject(
			value.Field{Key: "format_id", Value: value.String("http-" + format.Quality)},
			value.Field{Key: "url", Value: value.String(format.URL)},
			value.Field{Key: "ext", Value: value.String(extension)},
		)
		setPositiveInt(object, "width", format.Width)
		setPositiveInt(object, "height", format.Height)
		setPositiveInt(object, "fps", format.FPS)
		if format.Bitrate > 0 {
			object.Set("tbr", value.Float(float64(format.Bitrate)))
		}
		formats = append(formats, value.ObjectValue(object))
	}
	for _, name := range sortedVimeoCDNs(files.HLS.CDNs) {
		if err := contextError(ctx); err != nil {
			return Extraction{}, err
		}
		cdn := files.HLS.CDNs[name]
		if validHTTPURL(cdn.URL) {
			formats = append(formats, value.ObjectValue(manifestFormat("hls-"+name, cdn.URL, "m3u8_native")))
		}
	}
	for _, name := range sortedVimeoCDNs(files.DASH.CDNs) {
		if err := contextError(ctx); err != nil {
			return Extraction{}, err
		}
		cdn := files.DASH.CDNs[name]
		if validHTTPURL(cdn.URL) {
			manifestURL := strings.Replace(cdn.URL, "/master.json", "/master.mpd", 1)
			formats = append(formats, value.ObjectValue(manifestFormat("dash-"+name, manifestURL, "http_dash_segments")))
		}
	}
	liveStatus := map[string]string{"pending": "is_upcoming", "active": "is_upcoming", "started": "is_live", "ended": "post_live"}[config.Video.LiveEvent.Status]
	if len(formats) == 0 {
		if liveStatus == "is_upcoming" {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: no Vimeo formats", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(config.Video.Title)},
		value.Field{Key: "description", Value: value.String(config.Video.Description)},
		value.Field{Key: "uploader", Value: value.String(config.Video.Owner.Name)},
		value.Field{Key: "uploader_url", Value: value.String(config.Video.Owner.URL)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	setPositiveInt(info, "duration", config.Video.Duration)
	setPositiveInt(info, "width", config.Video.Width)
	setPositiveInt(info, "height", config.Video.Height)
	if thumbnail := bestVimeoThumbnail(config.Video.Thumbs); thumbnail != "" {
		info.Set("thumbnail", value.String(thumbnail))
	}
	if liveStatus != "" {
		info.Set("live_status", value.String(liveStatus))
	}
	if subtitles, err := vimeoSubtitles(ctx, config.Request.TextTracks); err != nil {
		return Extraction{}, err
	} else if subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

func vimeoSubtitles(ctx context.Context, tracks []vimeoTextTrack) (*value.Object, error) {
	if len(tracks) > vimeoMaxTextTracks {
		return nil, fmt.Errorf("%w: Vimeo text-track limit", ErrInvalidMetadata)
	}
	grouped := make(map[string][]value.Value)
	order := make([]string, 0, len(tracks))
	seen := make(map[string]struct{}, len(tracks))
	for _, track := range tracks {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		language := boundedVimeoText(track.Language, vimeoMaxTextLang)
		if !validVimeoLanguage(language) || !validVimeoTextKind(track.Kind) {
			continue
		}
		trackURL := normalizeVimeoTextTrackURL(track.URL)
		if trackURL == "" {
			continue
		}
		name := boundedVimeoText(track.Label, vimeoMaxTextName)
		if name == "" {
			name = boundedVimeoText(track.Name, vimeoMaxTextName)
		}
		// A URL is the stable identity of a declared text format. Labels are
		// presentation metadata and must not manufacture duplicate downloads.
		key := language + "\x00" + trackURL
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if _, exists := grouped[language]; !exists {
			order = append(order, language)
		}
		entry := value.NewObject(
			value.Field{Key: "url", Value: value.String(trackURL)},
			value.Field{Key: "ext", Value: value.String("vtt")},
		)
		if name != "" {
			entry.Set("name", value.String(name))
		}
		grouped[language] = append(grouped[language], value.ObjectValue(entry))
	}
	result := value.NewObject()
	for _, language := range order {
		result.Set(language, value.List(grouped[language]...))
	}
	return result, nil
}

func boundedVimeoText(input string, limit int) string {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > limit || strings.ContainsRune(input, '\x00') {
		return ""
	}
	return input
}

func validVimeoLanguage(language string) bool {
	if language == "" || len(language) > vimeoMaxTextLang {
		return false
	}
	for index, character := range language {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || (index > 0 && (character == '.' || character == '_' || character == '-'))) {
			return false
		}
	}
	return true
}

func validVimeoTextKind(kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	return kind == "" || kind == "subtitles" || kind == "captions"
}

// normalizeVimeoTextTrackURL mirrors the reference's player.vimeo.com URL
// join, but fails closed: subtitle tokens never leave the player origin.
func normalizeVimeoTextTrackURL(rawURL string) string {
	if len(rawURL) == 0 || len(rawURL) > vimeoMaxTextURL || strings.ContainsAny(rawURL, "\\\x00\r\n") {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || vimeoUnsafePath(parsed) {
		return ""
	}
	base, _ := url.Parse("https://player.vimeo.com/")
	parsed = base.ResolveReference(parsed)
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || strings.ToLower(parsed.Hostname()) != "player.vimeo.com" || vimeoUnsafePath(parsed) {
		return ""
	}
	parsed.Scheme = "https"
	parsed.Host = "player.vimeo.com"
	result := parsed.String()
	if len(result) > vimeoMaxTextURL {
		return ""
	}
	return result
}

func vimeoUnsafePath(parsed *url.URL) bool {
	if parsed == nil || parsed.RawPath != "" || parsed.Path == "" || path.Clean(parsed.Path) != parsed.Path {
		return true
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	return strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") || strings.Contains(escaped, "%2e") || strings.Contains(escaped, "%25") || strings.Contains(parsed.String(), "\x00")
}

func sortedVimeoCDNs(cdns map[string]struct {
	URL string `json:"url"`
}) []string {
	names := make([]string, 0, len(cdns))
	for name := range cdns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validHTTPURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func mustURLPath(rawURL string) string {
	parsed, _ := url.Parse(rawURL)
	return parsed.Path
}

func setPositiveInt(object *value.Object, key string, number int64) {
	if number > 0 {
		object.Set(key, value.Int(number))
	}
}

func bestVimeoThumbnail(thumbs map[string]string) string {
	bestWidth := -1
	bestURL := ""
	for width, rawURL := range thumbs {
		parsedWidth, err := strconv.Atoi(width)
		if err == nil && parsedWidth > bestWidth && validHTTPURL(rawURL) {
			bestWidth, bestURL = parsedWidth, rawURL
		}
	}
	return bestURL
}
