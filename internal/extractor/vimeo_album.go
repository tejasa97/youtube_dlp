package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	vimeoAlbumPageSize       = 100
	vimeoAlbumMaxPages       = 100
	vimeoAlbumMaxEntries     = 10_000
	vimeoAlbumMaxJWTBytes    = 8 << 10
	vimeoAlbumMaxDescription = 8 << 10
	vimeoAlbumMaxJWTPayload  = 4 << 10
	vimeoAlbumMaxSlugAuth    = 1 << 20
	vimeoAlbumJWTRefreshLead = 2 * time.Minute
)

var (
	vimeoAlbumJWTPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
	vimeoAlbumURI        = regexp.MustCompile(`^/videos/([0-9]{1,20})$`)
)

type vimeoAlbumMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Privacy     struct {
		View string `json:"view"`
	} `json:"privacy"`
}

type vimeoAlbumVideoPage struct {
	Data []struct {
		Link string `json:"link"`
		URI  string `json:"uri"`
	} `json:"data"`
}

func classifyVimeoAlbumURL(parsed *url.URL) (vimeoPlaylistTarget, bool) {
	if parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		parsed.RawPath != "" || strings.Contains(parsed.String(), "\x00") {
		return vimeoPlaylistTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "vimeo.com" && host != "www.vimeo.com" {
		return vimeoPlaylistTarget{}, false
	}
	parts := splitVimeoPath(parsed.Path)
	if len(parts) != 2 || (parts[0] != "album" && parts[0] != "showcase") ||
		parsed.Path != "/"+parts[0]+"/"+parts[1] {
		return vimeoPlaylistTarget{}, false
	}
	target := vimeoPlaylistTarget{
		kind:      vimeoRouteAlbum,
		canonical: "https://vimeo.com/" + parts[0] + "/" + parts[1],
		baseURL:   parts[0],
	}
	if validVimeoNumericVideoID(parts[1]) {
		numericID, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil || numericID == 0 {
			return vimeoPlaylistTarget{}, false
		}
		target.id = parts[1]
		return target, true
	}
	slug, ok := validVimeoSlug(parts[1], false)
	if !ok || vimeoNumericPattern.MatchString(slug) {
		return vimeoPlaylistTarget{}, false
	}
	target.slug = slug
	return target, true
}

func extractVimeoAlbumPlaylist(ctx context.Context, transport Transport, target vimeoPlaylistTarget) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	viewerTransport, viewerOK := transport.(CredentialIsolatedNoRedirectTransport)
	_, scopedOK := transport.(ScopedAuthorizationNoRedirectTransport)
	if !viewerOK || !scopedOK {
		return Extraction{}, ErrTransportIsolation
	}
	albumID := target.id
	if target.slug != "" {
		var err error
		albumID, err = resolveVimeoAlbumSlug(ctx, viewerTransport, target.slug)
		if err != nil {
			return Extraction{}, categorizeVimeoAlbumError(err)
		}
	}
	provider := &vimeoAlbumTokenProvider{transport: viewerTransport, now: time.Now}
	metadata, err := fetchVimeoAlbumMetadata(ctx, transport, albumID, provider)
	if err != nil {
		return Extraction{}, categorizeVimeoAlbumError(err)
	}
	title := boundedVimeoPlaylistText(metadata.Name, vimeoMaxPlaylistTitle)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Vimeo album title", ErrInvalidMetadata)
	}
	switch strings.ToLower(strings.TrimSpace(metadata.Privacy.View)) {
	case "anybody":
	case "password", "nobody", "contacts", "users", "unlisted":
		return Extraction{}, ErrAuthentication
	default:
		return Extraction{}, fmt.Errorf("%w: unsupported Vimeo album privacy", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(albumID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
	)
	if description := boundedVimeoPlaylistText(metadata.Description, vimeoAlbumMaxDescription); description != "" {
		info.Set("description", value.String(description))
	}
	return Playlist(value.NewInfo(info), vimeoAlbumEntries{
		transport: transport,
		albumID:   albumID,
		provider:  provider,
	})
}

func resolveVimeoAlbumSlug(
	ctx context.Context,
	transport CredentialIsolatedNoRedirectTransport,
	slug string,
) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	validSlug, ok := validVimeoSlug(slug, false)
	if !ok || validSlug != slug || vimeoNumericPattern.MatchString(slug) {
		return "", fmt.Errorf("%w: invalid Vimeo album slug", ErrInvalidMetadata)
	}
	endpoint := "https://vimeo.com/showcase/" + slug + "/auth"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("%w: invalid Vimeo album resolver request", ErrInvalidMetadata)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Requested-With", "XMLHttpRequest")
	response, err := transport.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return "", err
	}
	if response == nil || response.Body == nil {
		return "", fmt.Errorf("%w: empty Vimeo album resolver response", ErrInvalidMetadata)
	}
	defer response.Body.Close()

	status := response.StatusCode
	switch status {
	case http.StatusOK, http.StatusUnauthorized, http.StatusForbidden:
	default:
		return "", &HTTPStatusError{Code: status}
	}
	limited := io.LimitReader(response.Body, vimeoAlbumMaxSlugAuth+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if len(payload) > vimeoAlbumMaxSlugAuth {
		return "", fmt.Errorf("%w: Vimeo album resolver response too large", ErrJSONResponseTooLarge)
	}
	id, err := parseVimeoAlbumSlugID(payload)
	if err != nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return "", ErrAuthentication
		}
		return "", err
	}
	return id, nil
}

func parseVimeoAlbumSlugID(payload []byte) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	var response struct {
		Metadata struct {
			ID json.RawMessage `json:"id"`
		} `json:"metadata"`
	}
	if err := decoder.Decode(&response); err != nil || ensureJSONEOF(decoder) != nil {
		return "", fmt.Errorf("%w: malformed Vimeo album resolver response", ErrInvalidMetadata)
	}
	rawID := strings.TrimSpace(string(response.Metadata.ID))
	if rawID == "" || len(rawID) > 20 || !vimeoNumericPattern.MatchString(rawID) {
		return "", fmt.Errorf("%w: invalid Vimeo album resolver identity", ErrInvalidMetadata)
	}
	numericID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || numericID == 0 {
		return "", fmt.Errorf("%w: invalid Vimeo album resolver identity", ErrInvalidMetadata)
	}
	return rawID, nil
}

type vimeoAlbumTokenProvider struct {
	transport CredentialIsolatedNoRedirectTransport
	now       func() time.Time

	mu      sync.Mutex
	token   string
	expires int64
}

func (provider *vimeoAlbumTokenProvider) get(ctx context.Context) (string, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	now := time.Now
	if provider.now != nil {
		now = provider.now
	}
	if provider.token != "" && provider.expires-now().Unix() >= int64(vimeoAlbumJWTRefreshLead/time.Second) {
		return provider.token, nil
	}
	token, expires, err := fetchVimeoAlbumJWT(ctx, provider.transport)
	if err != nil {
		return "", err
	}
	if expires-now().Unix() < int64(vimeoAlbumJWTRefreshLead/time.Second) {
		return "", ErrAuthentication
	}
	provider.token, provider.expires = token, expires
	return token, nil
}

func (provider *vimeoAlbumTokenProvider) invalidate(token string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.token == token {
		provider.token, provider.expires = "", 0
	}
}

func fetchVimeoAlbumJWT(ctx context.Context, transport CredentialIsolatedNoRedirectTransport) (string, int64, error) {
	var viewer struct {
		JWT string `json:"jwt"`
	}
	if err := requestJSON(ctx, transport.DoWithoutCredentialsNoRedirect, http.MethodGet,
		"https://vimeo.com/_next/viewer", nil, http.Header{"Accept": {"application/json"}}, &viewer); err != nil {
		return "", 0, err
	}
	if len(viewer.JWT) == 0 || len(viewer.JWT) > vimeoAlbumMaxJWTBytes ||
		!vimeoAlbumJWTPattern.MatchString(viewer.JWT) {
		return "", 0, fmt.Errorf("%w: malformed Vimeo viewer token", ErrInvalidMetadata)
	}
	parts := strings.Split(viewer.JWT, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) == 0 || len(payload) > vimeoAlbumMaxJWTPayload {
		return "", 0, fmt.Errorf("%w: malformed Vimeo viewer token", ErrInvalidMetadata)
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	var claims struct {
		Expires json.Number `json:"exp"`
	}
	if err := decoder.Decode(&claims); err != nil || ensureJSONEOF(decoder) != nil {
		return "", 0, fmt.Errorf("%w: malformed Vimeo viewer token", ErrInvalidMetadata)
	}
	expires, err := claims.Expires.Int64()
	if err != nil || expires <= 0 {
		return "", 0, fmt.Errorf("%w: malformed Vimeo viewer token", ErrInvalidMetadata)
	}
	return viewer.JWT, expires, nil
}

func fetchVimeoAlbumMetadata(
	ctx context.Context,
	transport Transport,
	albumID string,
	provider *vimeoAlbumTokenProvider,
) (vimeoAlbumMetadata, error) {
	var metadata vimeoAlbumMetadata
	query := url.Values{
		"fields":   {"description,name,privacy"},
		"is_embed": {"false"},
		"referrer": {""},
	}
	endpoint := "https://api.vimeo.com/albums/" + albumID + "?" + query.Encode()
	err := withVimeoAlbumToken(ctx, provider, func(jwt string) error {
		return RequestJSONWithScopedAuthorizationNoRedirect(
			ctx, transport, http.MethodGet, endpoint, nil, vimeoAlbumHeaders(jwt), &metadata)
	})
	return metadata, err
}

func withVimeoAlbumToken(ctx context.Context, provider *vimeoAlbumTokenProvider, request func(string) error) error {
	if provider == nil || request == nil {
		return fmt.Errorf("%w: missing Vimeo album token provider", ErrInvalidMetadata)
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := provider.get(ctx)
		if err != nil {
			return err
		}
		err = request(token)
		var status *HTTPStatusError
		if attempt == 0 && errors.As(err, &status) &&
			(status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) {
			provider.invalidate(token)
			continue
		}
		return err
	}
	return ErrAuthentication
}

func vimeoAlbumHeaders(jwt string) http.Header {
	return http.Header{
		"Accept":        {"application/json"},
		"Authorization": {"jwt " + jwt},
	}
}

type vimeoAlbumEntries struct {
	transport Transport
	albumID   string
	provider  *vimeoAlbumTokenProvider
}

func (entries vimeoAlbumEntries) Iterator() EntryIterator {
	return &vimeoAlbumIterator{
		transport: entries.transport,
		albumID:   entries.albumID,
		provider:  entries.provider,
		seen:      make(map[string]struct{}),
	}
}

type vimeoAlbumIterator struct {
	transport Transport
	albumID   string
	provider  *vimeoAlbumTokenProvider
	page      []Entry
	pageIndex int
	pageNum   int
	seen      map[string]struct{}
	total     int
	done      bool
}

func (iterator *vimeoAlbumIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.done = true
		return Entry{}, false, err
	}
	for !iterator.done && iterator.pageIndex >= len(iterator.page) {
		if iterator.pageNum >= vimeoAlbumMaxPages {
			iterator.done = true
			return Entry{}, false, fmt.Errorf("%w: Vimeo album page bound", ErrPlaylistLimit)
		}
		iterator.pageNum++
		page, short, err := fetchVimeoAlbumVideoPage(
			ctx, iterator.transport, iterator.albumID, iterator.provider, iterator.pageNum)
		if err != nil {
			var status *HTTPStatusError
			if iterator.pageNum > 1 && errors.As(err, &status) && status.Code == http.StatusBadRequest {
				iterator.done = true
				break
			}
			iterator.done = true
			return Entry{}, false, categorizeVimeoAlbumError(err)
		}
		iterator.page = iterator.page[:0]
		iterator.pageIndex = 0
		for _, entry := range page {
			if _, exists := iterator.seen[entry.ID]; exists {
				continue
			}
			if iterator.total >= vimeoAlbumMaxEntries {
				iterator.done = true
				return Entry{}, false, fmt.Errorf("%w: Vimeo album entry bound", ErrPlaylistLimit)
			}
			iterator.seen[entry.ID] = struct{}{}
			iterator.page = append(iterator.page, entry)
			iterator.total++
		}
		if short {
			iterator.done = true
		}
		if len(iterator.page) == 0 && iterator.done {
			break
		}
	}
	if iterator.pageIndex < len(iterator.page) {
		entry := iterator.page[iterator.pageIndex]
		iterator.pageIndex++
		return entry, true, nil
	}
	if iterator.total == 0 {
		return Entry{}, false, fmt.Errorf("%w: missing Vimeo album entries", ErrInvalidPlaylist)
	}
	return Entry{}, false, nil
}

func fetchVimeoAlbumVideoPage(
	ctx context.Context,
	transport Transport,
	albumID string,
	provider *vimeoAlbumTokenProvider,
	page int,
) ([]Entry, bool, error) {
	query := url.Values{
		"fields":   {"link,uri"},
		"is_embed": {"false"},
		"page":     {fmt.Sprintf("%d", page)},
		"per_page": {fmt.Sprintf("%d", vimeoAlbumPageSize)},
		"referrer": {""},
	}
	endpoint := "https://api.vimeo.com/albums/" + albumID + "/videos?" + query.Encode()
	var payload vimeoAlbumVideoPage
	if err := withVimeoAlbumToken(ctx, provider, func(jwt string) error {
		return RequestJSONWithScopedAuthorizationNoRedirect(
			ctx, transport, http.MethodGet, endpoint, nil, vimeoAlbumHeaders(jwt), &payload)
	}); err != nil {
		return nil, false, err
	}
	if payload.Data == nil || len(payload.Data) > vimeoAlbumPageSize {
		return nil, false, fmt.Errorf("%w: malformed Vimeo album page", ErrInvalidPlaylist)
	}
	out := make([]Entry, 0, len(payload.Data))
	for _, video := range payload.Data {
		if entry, ok := vimeoAlbumVideoEntry(video.Link, video.URI); ok {
			out = append(out, entry)
		}
	}
	return out, len(payload.Data) < vimeoAlbumPageSize, nil
}

func vimeoAlbumVideoEntry(link, uri string) (Entry, bool) {
	match := vimeoAlbumURI.FindStringSubmatch(uri)
	if len(match) != 2 {
		return Entry{}, false
	}
	id := match[1]
	parsed, err := url.Parse(link)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		(parsed.Hostname() != "vimeo.com" && parsed.Hostname() != "www.vimeo.com") ||
		parsed.Path != "/"+id {
		return Entry{}, false
	}
	return Entry{
		URL:          "https://vimeo.com/" + id,
		ExtractorKey: "vimeo",
		ID:           id,
		Transparent:  true,
	}, true
}

func categorizeVimeoAlbumError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrInvalidPlaylist) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrPlaylistLimit) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		}
	}
	return ErrVimeoPlaylistNetwork
}
