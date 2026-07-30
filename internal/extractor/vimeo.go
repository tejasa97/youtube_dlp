package extractor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const vimeoImpersonationProfile = "chrome-133"

// maxVimeoViewerXSRFTBytes bounds the unexported xsrft token parsed from a
// Vimeo _next/viewer payload. The value mirrors the public video-password
// budget; extractors that consume the token enforce their own narrower
// limits.
const maxVimeoViewerXSRFTBytes = 4096

var ErrVimeoPlaylistNetwork = errors.New("Vimeo playlist network failure")

const (
	vimeoMaxTextTracks = 128
	vimeoMaxTextURL    = 8192
	vimeoMaxTextLang   = 64
	vimeoMaxTextName   = 256
	vimeoMaxConfigURL  = 8192
	vimeoMaxReferer    = 2048
	vimeoMaxManifest   = 4 << 20

	vimeoMaxSlugBytes         = 64
	vimeoMaxPlaylistTitle     = 512
	vimeoMaxEntryTitle        = 512
	vimeoMaxPageBytes         = 4 << 20
	vimeoMaxPlaylistPages     = 100
	vimeoMaxClipsPerPage      = 128
	vimeoMaxPlaylistEntries   = 10_000
	vimeoClipLookaheadBytes   = 2048
	vimeoMaxNumericVideoIDLen = 20

	// vimeoUnlistedHashLen is the fixed width of the unlisted-share suffix
	// Vimeo renders on private video URLs. The route parser rejects any
	// value that does not match this length exactly so callers cannot smuggle
	// arbitrary path data through the unlisted route.
	vimeoUnlistedHashLen = 10
	// vimeoUnlistedAPIErrorCode is the exact numeric error_code Vimeo
	// returns for hash-gated unlisted videos when the requested account
	// is not authorized for the share. A quoted decimal is rejected here
	// so the response cannot trick the parser into an integer-shaped
	// string by mistake.
	vimeoUnlistedAPIErrorCode = 5460
)

// vimeoUnlistedHashPattern validates the exact 10-character lowercase hex
// suffix Vimeo appends to a private unlisted video URL. It anchors both ends
// and forbids uppercase, variable width, or other charset drift.
var vimeoUnlistedHashPattern = regexp.MustCompile(`^[0-9a-f]{10}$`)

const (
	// vimeoUnlistedAPIMaxBytes bounds the metadata JSON object returned by
	// api.vimeo.com/videos/{id}:{hash}. The value matches the shared
	// extractor JSON budget so the package-private helper can reuse the
	// same dispatch it uses for every other Vimeo metadata fetch.
	vimeoUnlistedAPIMaxBytes int64 = maxExtractorJSONBytes
	// vimeoUnlistedAPIStatusReadBytes is the hard cap on a non-2xx body.
	// It is intentionally far smaller than the 2xx budget; non-success
	// bodies are only read for error_code categorization and are
	// intentionally discarded after categorization.
	vimeoUnlistedAPIStatusReadBytes int64 = 8 << 10
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
	embed     bool
	// unlistedHash, when non-empty, is the validated 10-character lowercase
	// hex suffix Vimeo appends to a private video URL. A non-empty value
	// selects the authenticated metadata path; an empty value preserves the
	// existing public page/config behavior. The classifier enforces the
	// shape before this field is populated.
	unlistedHash string
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
		return extractVimeoAlbumPlaylist(ctx, request, target)
	case vimeoRouteVideo:
		if target.unlistedHash != "" {
			return extractVimeoUnlistedVideo(ctx, request, target.id, target.unlistedHash, target.canonical)
		}
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
	playerRoute := isVimeoPlayerVideoURL(webpageURL, videoID)
	page, _, err := readVimeoPage(ctx, request.Transport, webpageURL, request.Referer)
	if err != nil {
		return Extraction{}, err
	}
	config, err := extractVimeoConfig(ctx, request.Transport, configRefererURL(webpageURL, request.Referer), page)
	if err != nil {
		if !errors.Is(err, ErrAuthentication) {
			return Extraction{}, err
		}
		// The pinned player flow exposes its password gate as config.view == 4.
		// A player page without that bounded config must not be reinterpreted as
		// the standard vimeo.com JSON password flow.
		if playerRoute {
			return Extraction{}, err
		}
		if err := verifyVimeoVideoPassword(ctx, request.Transport, webpageURL, videoID, request.VideoPassword); err != nil {
			return Extraction{}, err
		}
		page, _, err = readVimeoPage(ctx, request.Transport, webpageURL, request.Referer)
		if err != nil {
			return Extraction{}, err
		}
		config, err = extractVimeoConfig(ctx, request.Transport, configRefererURL(webpageURL, request.Referer), page)
		if err != nil {
			return Extraction{}, err
		}
	}
	if config.View == 4 {
		playerURL := "https://player.vimeo.com/video/" + videoID
		if playerRoute {
			playerURL = webpageURL
		}
		config, err = verifyVimeoPlayerPassword(ctx, request.Transport, playerURL, configRefererURL(webpageURL, request.Referer), request.VideoPassword)
		if err != nil {
			return Extraction{}, err
		}
	}
	return parseVimeoConfigContext(ctx, request.Transport, config, videoID, webpageURL, request.Referer)
}

// readVimeoPage keeps the established bounded profile page transport while
// translating only the two pinned fingerprint-block status/origin pairs.  The
// page helper owns response cleanup and bounds; its StatusError URL has already
// had any signed query values redacted.
func readVimeoPage(ctx context.Context, transport Transport, webpageURL, referer string) ([]byte, http.Header, error) {
	if err := contextError(ctx); err != nil {
		return nil, nil, err
	}
	if profiled, ok := transport.(ProfiledPageNoRedirectTransport); ok {
		page, headers, status, err := readVimeoProfilePage(ctx, profiled, webpageURL, "")
		if err != nil || status < 300 {
			return page, headers, err
		}
		if isVimeoPrivacyRetryStatus(webpageURL, status) && isVimeoEmbedOnlyBody(page) {
			validated, valid := validVimeoReferer(referer)
			if !valid {
				return nil, nil, ErrAuthentication
			}
			returnPage, returnHeaders, returnStatus, retryErr := readVimeoProfilePage(ctx, profiled, webpageURL, validated)
			if retryErr != nil {
				return nil, nil, retryErr
			}
			if returnStatus >= 300 {
				if categorized := categorizeVimeoResponseStatus(webpageURL, returnStatus, returnPage); categorized != nil {
					return nil, nil, categorized
				}
				return nil, nil, &HTTPStatusError{Code: returnStatus}
			}
			return returnPage, returnHeaders, nil
		}
		if categorized := categorizeVimeoResponseStatus(webpageURL, status, page); categorized != nil {
			return nil, nil, categorized
		}
		return nil, nil, &HTTPStatusError{Code: status}
	}
	page, headers, err := ReadPageWithProfile(ctx, transport, webpageURL, vimeoImpersonationProfile)
	if err == nil {
		return page, headers, nil
	}
	var status *network.StatusError
	if errors.As(err, &status) {
		if classified := categorizeVimeoResponseStatus(webpageURL, status.Code, nil); classified != nil {
			return nil, nil, classified
		}
	}
	if errors.Is(err, network.ErrImpersonationUnavailable) {
		return nil, nil, ErrTransportProfile
	}
	return nil, nil, err
}

func readVimeoProfilePage(ctx context.Context, transport ProfiledPageNoRedirectTransport, rawURL, referer string) ([]byte, http.Header, int, error) {
	if err := contextError(ctx); err != nil {
		return nil, nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, 0, ErrInvalidMetadata
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := transport.DoProfiledPageNoRedirect(ctx, req, vimeoImpersonationProfile)
	if err != nil {
		return nil, nil, 0, err
	}
	if resp == nil || resp.Body == nil {
		return nil, nil, 0, ErrInvalidMetadata
	}
	defer resp.Body.Close()
	limit := int64(vimeoMaxPageBytes)
	if resp.StatusCode >= 300 {
		limit = vimeoUnlistedAPIStatusReadBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, resp.Header.Clone(), resp.StatusCode, err
		}
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, resp.Header.Clone(), resp.StatusCode, contextErr
		}
		return nil, resp.Header.Clone(), resp.StatusCode, ErrInvalidMetadata
	}
	if int64(len(body)) > limit {
		return nil, resp.Header.Clone(), resp.StatusCode, ErrJSONResponseTooLarge
	}
	return body, resp.Header.Clone(), resp.StatusCode, nil
}

func isVimeoEmbedOnlyBody(body []byte) bool {
	return len(body) <= int(vimeoUnlistedAPIStatusReadBytes) && bytes.Contains(body, []byte("Because of its privacy settings, this video cannot be played here"))
}

func isVimeoPrivacyRetryStatus(rawURL string, status int) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawFragment != "" || vimeoUnsafePath(parsed) {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return (status == http.StatusForbidden && (host == "vimeo.com" || host == "www.vimeo.com")) ||
		(status == http.StatusTooManyRequests && host == "player.vimeo.com")
}

// verifyVimeoVideoPassword submits the normal Vimeo password JSON only after a
// credential-isolated viewer fetch has supplied a bounded xsrft token. The
// password request itself retains the operation jar so its same-origin session
// cookie can be used by the immediately following public extraction flow.
func verifyVimeoVideoPassword(ctx context.Context, transport Transport, webpageURL, videoID, password string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if password == "" {
		return ErrAuthentication
	}
	endpoint, referer, ok := vimeoVideoPasswordEndpoint(webpageURL, videoID)
	if !ok {
		return fmt.Errorf("%w: unsafe Vimeo password endpoint", ErrInvalidMetadata)
	}
	viewer, _, err := ReadPageWithProfileWithoutCredentialsNoRedirect(ctx, transport, "https://vimeo.com/_next/viewer", vimeoImpersonationProfile)
	if err != nil {
		return err
	}
	xsrft, err := parseVimeoViewerXSRFT(viewer)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}{password, xsrft})
	if err != nil {
		return ErrInvalidMetadata
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ErrInvalidMetadata
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Referer", referer)
	profiled, ok := transport.(ProfiledNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	response, err := profiled.DoProfiledNoRedirect(ctx, request, vimeoImpersonationProfile)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusTeapot:
		return ErrWrongPassword
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuthentication
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrVimeoPlaylistNetwork
	}
	return nil
}

func vimeoVideoPasswordEndpoint(webpageURL, videoID string) (endpoint, referer string, ok bool) {
	parsed, err := url.Parse(webpageURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		(parsed.Hostname() != "vimeo.com" && parsed.Hostname() != "www.vimeo.com") || parsed.RawQuery != "" || parsed.Fragment != "" ||
		vimeoUnsafePath(parsed) || !strings.HasSuffix(parsed.Path, "/"+videoID) {
		return "", "", false
	}
	parsed.Host = "vimeo.com"
	parsed.RawPath = ""
	referer = parsed.String()
	parsed.Path += "/password"
	return parsed.String(), referer, true
}

func verifyVimeoPlayerPassword(ctx context.Context, transport Transport, playerURL, referer, password string) (vimeoConfig, error) {
	if err := contextError(ctx); err != nil {
		return vimeoConfig{}, err
	}
	if password == "" {
		return vimeoConfig{}, ErrAuthentication
	}
	endpoint, ok := vimeoPlayerPasswordEndpoint(playerURL)
	if !ok {
		return vimeoConfig{}, fmt.Errorf("%w: unsafe Vimeo player password endpoint", ErrInvalidMetadata)
	}
	form := url.Values{"password": {base64.StdEncoding.EncodeToString([]byte(password))}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return vimeoConfig{}, ErrInvalidMetadata
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if validated, ok := validVimeoReferer(referer); ok {
		request.Header.Set("Referer", validated)
	} else {
		return vimeoConfig{}, fmt.Errorf("%w: unsafe Vimeo player referer", ErrInvalidMetadata)
	}
	profiled, ok := transport.(ProfiledNoRedirectTransport)
	if !ok {
		return vimeoConfig{}, ErrTransportIsolation
	}
	response, err := profiled.DoProfiledNoRedirect(ctx, request, vimeoImpersonationProfile)
	if err != nil {
		return vimeoConfig{}, err
	}
	defer response.Body.Close()
	if statusErr := categorizeVimeoPasswordStatus(response.StatusCode); statusErr != nil {
		return vimeoConfig{}, statusErr
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxExtractorJSONBytes+1))
	if err != nil || int64(len(data)) > maxExtractorJSONBytes {
		return vimeoConfig{}, ErrInvalidMetadata
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("false")) {
		return vimeoConfig{}, ErrWrongPassword
	}
	var config vimeoConfig
	if json.Unmarshal(data, &config) != nil {
		return vimeoConfig{}, fmt.Errorf("%w: invalid Vimeo player password response", ErrInvalidMetadata)
	}
	return config, nil
}

func categorizeVimeoPasswordStatus(status int) error {
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusTeapot:
		return ErrWrongPassword
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuthentication
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		if status >= 200 && status < 300 {
			return nil
		}
		return ErrVimeoPlaylistNetwork
	}
}

func vimeoPlayerPasswordEndpoint(playerURL string) (string, bool) {
	parsed, err := url.Parse(playerURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Hostname() != "player.vimeo.com" || parsed.Fragment != "" || vimeoUnsafePath(parsed) ||
		!vimeoURLPattern.MatchString(parsed.Path) {
		return "", false
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Path += "/check-password"
	return parsed.String(), true
}

func isVimeoPlayerVideoURL(rawURL, videoID string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "player.vimeo.com" &&
		parsed.Path == "/video/"+videoID && parsed.User == nil && parsed.Port() == "" && parsed.Fragment == "" && parsed.RawPath == ""
}

// extractVimeoUnlistedVideo resolves a Vimeo private/unlisted share URL of
// the canonical form https://vimeo.com/{numeric_id}/{10-lowercase-hex-hash}.
// The helper consumes the merged authenticated viewer-token provider for the
// scoped metadata and optional source-format API requests, follows the
// returned config_url with the credential-isolated executor, and reuses the
// existing config parser. It never reaches an anonymous fallback once the
// authenticated path is taken, never submits the deferred password POST, and
// surfaces all categorized metadata failures as established Vimeo sentinels.
func extractVimeoUnlistedVideo(ctx context.Context, request Request, videoID, unlistedHash, webpageURL string) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	provider, err := vimeoAuthenticatedViewerTokenProviderFromTransport(request.Transport, time.Now)
	if err != nil {
		return Extraction{}, err
	}
	apiResponse, err := fetchVimeoUnlistedAPI(ctx, provider, request.Transport, videoID, unlistedHash)
	if err != nil {
		return Extraction{}, err
	}
	if apiResponse.URI != "/videos/"+videoID {
		return Extraction{}, fmt.Errorf("%w: Vimeo authenticated API video identity mismatch", ErrInvalidMetadata)
	}
	if apiResponse.ConfigURL == "" {
		return Extraction{}, ErrUnavailable
	}
	normalized, ok := normalizeVimeoConfigURL(apiResponse.ConfigURL)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsafe Vimeo authenticated config URL", ErrInvalidMetadata)
	}
	isolated, ok := request.Transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return Extraction{}, ErrTransportIsolation
	}
	config, err := fetchVimeoUnlistedConfig(ctx, isolated, normalized, webpageURL)
	if err != nil {
		return Extraction{}, err
	}
	if config.Video.ID.String() != videoID {
		return Extraction{}, fmt.Errorf("%w: Vimeo player config video identity mismatch", ErrInvalidMetadata)
	}
	sourceFormat, err := extractVimeoAuthenticatedOriginalFormat(ctx, provider, request.Transport, videoID, unlistedHash)
	if err != nil {
		return Extraction{}, err
	}
	extraction, err := parseVimeoUnlistedConfigContext(ctx, request.Transport, config, videoID, webpageURL, request.Referer)
	if err != nil {
		return Extraction{}, err
	}
	applyVimeoAuthenticatedMetadata(&extraction.Info, apiResponse)
	if sourceFormat.Kind() != value.KindMissing {
		formats, ok := extraction.Info.Formats()
		if !ok {
			return Extraction{}, fmt.Errorf("%w: missing Vimeo formats", ErrInvalidMetadata)
		}
		extraction.Info.Set("formats", value.List(append(formats, sourceFormat)...))
	}
	return extraction, nil
}

// vimeoUnlistedAPIResponse is the bounded response shape consumed from
// api.vimeo.com/videos/{id}:{hash}. Only fields the pinned web client
// returns are decoded. URI is added to the pinned web field set solely to
// bind the response identity before config fetch or media emission.
type vimeoUnlistedAPIResponse struct {
	URI         string `json:"uri"`
	Description string `json:"description"`
	License     string `json:"license"`
	CreatedTime string `json:"created_time"`
	ReleaseTime string `json:"release_time"`
	ConfigURL   string `json:"config_url"`
	Stats       struct {
		Plays *int64 `json:"plays"`
	} `json:"stats"`
	Metadata struct {
		Connections struct {
			Comments struct {
				Total *int64 `json:"total"`
			} `json:"comments"`
			Likes struct {
				Total *int64 `json:"total"`
			} `json:"likes"`
		} `json:"connections"`
	} `json:"metadata"`
}

// vimeoUnlistedAPIEndpoint joins the validated numeric ID and unlisted
// hash into the canonical authenticated API path. Both components have
// already passed strict validation; the function is a tiny seam so the
// query string and origin stay under one control point.
func vimeoUnlistedAPIEndpoint(numericID, unlistedHash string) string {
	return vimeoAuthenticatedVideoAPIEndpoint(numericID, unlistedHash,
		"config_url,uri,created_time,description,license,metadata.connections.comments.total,metadata.connections.likes.total,release_time,stats.plays")
}

func vimeoAuthenticatedVideoAPIEndpoint(numericID, unlistedHash, fields string) string {
	values := url.Values{}
	values.Set("fields", fields)
	return "https://api.vimeo.com/videos/" + numericID + ":" + unlistedHash + "?" + values.Encode()
}

// fetchVimeoUnlistedAPI executes the single scoped authenticated API call
// required for an unlisted Vimeo video. It uses the existing provider's
// withVimeoAuthenticatedViewerToken helper for transport-level origin
// isolation, JWT refresh on 401/403, and exactly-one retry semantics.
// The 4xx body is read only for categorization; it is never echoed.
func fetchVimeoUnlistedAPI(ctx context.Context, provider *vimeoAuthenticatedViewerTokenProvider, transport Transport, numericID, unlistedHash string) (vimeoUnlistedAPIResponse, error) {
	if err := contextError(ctx); err != nil {
		return vimeoUnlistedAPIResponse{}, err
	}
	scoped, ok := transport.(ScopedAuthorizationNoRedirectTransport)
	if !ok {
		return vimeoUnlistedAPIResponse{}, ErrTransportIsolation
	}
	endpoint := vimeoUnlistedAPIEndpoint(numericID, unlistedHash)
	var response vimeoUnlistedAPIResponse
	err := withVimeoAuthenticatedViewerToken(ctx, provider, func(jwt string) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if reqErr != nil {
			return fmt.Errorf("%w: malformed Vimeo authenticated video request", ErrInvalidMetadata)
		}
		req.Header = vimeoUnlistedAPIHeaders(jwt)
		httpResp, httpErr := scoped.DoWithScopedAuthorizationNoRedirect(ctx, req)
		if httpErr != nil {
			return httpErr
		}
		defer httpResp.Body.Close()
		code := httpResp.StatusCode
		if code >= http.StatusOK && code < http.StatusMultipleChoices {
			reader := &io.LimitedReader{R: httpResp.Body, N: vimeoUnlistedAPIMaxBytes + 1}
			data, readErr := io.ReadAll(reader)
			if readErr != nil {
				return fmt.Errorf("%w: Vimeo authenticated video response", ErrInvalidMetadata)
			}
			if int64(len(data)) > vimeoUnlistedAPIMaxBytes {
				return ErrJSONResponseTooLarge
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			if decodeErr := decoder.Decode(&response); decodeErr != nil {
				return fmt.Errorf("%w: Vimeo authenticated video response", ErrInvalidMetadata)
			}
			if err := ensureJSONEOF(decoder); err != nil {
				return fmt.Errorf("%w: Vimeo authenticated video response", ErrInvalidMetadata)
			}
			return nil
		}
		// Non-2xx: bounded body read for categorization only. The body bytes
		// are never copied into the returned error string.
		errorBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, vimeoUnlistedAPIStatusReadBytes+1))
		if int64(len(errorBody)) > vimeoUnlistedAPIStatusReadBytes {
			errorBody = errorBody[:vimeoUnlistedAPIStatusReadBytes]
		}
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			return &HTTPStatusError{Code: code}
		}
		if code == http.StatusNotFound || code == http.StatusGone {
			if matchesVimeoUnlistedErrorCode(errorBody, vimeoUnlistedAPIErrorCode) {
				return ErrAuthentication
			}
			return ErrUnavailable
		}
		if code == http.StatusBadRequest {
			return ErrAuthentication
		}
		return &HTTPStatusError{Code: code}
	})
	if err != nil {
		return vimeoUnlistedAPIResponse{}, err
	}
	return response, nil
}

// vimeoUnlistedAPIHeaders builds the scoped request headers for the
// authenticated Vimeo API call. The viewer JWT is the only credential on
// the wire; no cookie, Proxy-Authorization, or referer is set. The Accept
// header mirrors the existing vimeoAPIHeaders helper so the two paths emit
// the same wire signature on api.vimeo.com.
func vimeoUnlistedAPIHeaders(jwt string) http.Header {
	headers := http.Header{}
	headers.Set("Accept", "application/json")
	headers.Set("Authorization", "jwt "+jwt)
	return headers
}

// matchesVimeoUnlistedErrorCode inspects a bounded non-2xx body for a
// numeric error_code field. The raw JSON token must be the canonical base-10
// integer spelling; quoted, floating, exponential, overflowing, trailing,
// malformed, and oversized forms are rejected. The body bytes are never
// copied into the result.
func matchesVimeoUnlistedErrorCode(body []byte, want int64) bool {
	if len(body) == 0 || int64(len(body)) > vimeoUnlistedAPIStatusReadBytes {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var probe struct {
		ErrorCode json.RawMessage `json:"error_code"`
	}
	if err := decoder.Decode(&probe); err != nil {
		return false
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return false
	}
	rawCode := bytes.TrimSpace(probe.ErrorCode)
	if len(rawCode) == 0 || string(rawCode) != strconv.FormatInt(want, 10) {
		return false
	}
	value, err := strconv.ParseInt(string(rawCode), 10, 64)
	if err != nil {
		return false
	}
	return value == want
}

type vimeoAuthenticatedPrivacyResponse struct {
	Privacy struct {
		Download *bool `json:"download"`
	} `json:"privacy"`
}

type vimeoAuthenticatedDownloadResponse struct {
	Download []struct {
		Link       string `json:"link"`
		Quality    string `json:"quality"`
		PublicName string `json:"public_name"`
		Width      int64  `json:"width"`
		Height     int64  `json:"height"`
		FPS        int64  `json:"fps"`
		Size       int64  `json:"size"`
	} `json:"download"`
}

// extractVimeoAuthenticatedOriginalFormat implements the pinned web client's
// logged-in source-format path without copying any embedded OAuth client
// secret. Both capability checks use the authenticated viewer JWT on the
// exact api.vimeo.com video resource; the resulting CDN URL is emitted as a
// credential-free format.
func extractVimeoAuthenticatedOriginalFormat(ctx context.Context, provider *vimeoAuthenticatedViewerTokenProvider, transport Transport, videoID, unlistedHash string) (value.Value, error) {
	var privacy vimeoAuthenticatedPrivacyResponse
	err := fetchVimeoAuthenticatedVideoFields(ctx, provider, transport, videoID, unlistedHash, "privacy", &privacy)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return value.Missing(), err
		}
		return value.Missing(), nil
	}
	if privacy.Privacy.Download == nil || !*privacy.Privacy.Download {
		return value.Missing(), nil
	}
	var downloads vimeoAuthenticatedDownloadResponse
	err = fetchVimeoAuthenticatedVideoFields(ctx, provider, transport, videoID, unlistedHash, "download", &downloads)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return value.Missing(), err
		}
		return value.Missing(), nil
	}
	for _, download := range downloads.Download {
		if download.Quality != "source" || !validHTTPURL(download.Link) {
			continue
		}
		formatID := strings.TrimSpace(download.PublicName)
		if formatID == "" || len(formatID) > vimeoMaxTextName || strings.ContainsAny(formatID, "\r\n\x00") {
			formatID = "Original"
		}
		extension := vimeoOriginalExtension(download.Link)
		format := value.NewObject(
			value.Field{Key: "url", Value: value.String(download.Link)},
			value.Field{Key: "ext", Value: value.String(extension)},
			value.Field{Key: "format_id", Value: value.String(formatID)},
			value.Field{Key: "quality", Value: value.Int(1)},
		)
		setPositiveInt(format, "width", download.Width)
		setPositiveInt(format, "height", download.Height)
		setPositiveInt(format, "fps", download.FPS)
		setPositiveInt(format, "filesize", download.Size)
		return value.ObjectValue(format), nil
	}
	return value.Missing(), nil
}

func fetchVimeoAuthenticatedVideoFields(ctx context.Context, provider *vimeoAuthenticatedViewerTokenProvider, transport Transport, videoID, unlistedHash, fields string, target any) error {
	scoped, ok := transport.(ScopedAuthorizationNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	endpoint := vimeoAuthenticatedVideoAPIEndpoint(videoID, unlistedHash, fields)
	return withVimeoAuthenticatedViewerToken(ctx, provider, func(jwt string) error {
		return requestJSON(ctx, scoped.DoWithScopedAuthorizationNoRedirect, http.MethodGet,
			endpoint, nil, vimeoUnlistedAPIHeaders(jwt), target)
	})
}

func vimeoOriginalExtension(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "unknown_video"
	}
	if filename := parsed.Query().Get("filename"); filename != "" {
		if extension := strings.ToLower(strings.TrimPrefix(path.Ext(filename), ".")); extension != "" {
			return extension
		}
	}
	if extension := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), ".")); extension != "" {
		return extension
	}
	return "unknown_video"
}

func applyVimeoAuthenticatedMetadata(info *value.Info, response vimeoUnlistedAPIResponse) {
	if info == nil {
		return
	}
	info.Set("description", value.String(response.Description))
	if response.License != "" {
		info.Set("license", value.String(response.License))
	}
	if timestamp := vimeoAPITimestamp(response.CreatedTime); timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	if timestamp := vimeoAPITimestamp(response.ReleaseTime); timestamp > 0 {
		info.Set("release_timestamp", value.Int(timestamp))
	}
	setVimeoNonNegativeCount(info, "view_count", response.Stats.Plays)
	setVimeoNonNegativeCount(info, "comment_count", response.Metadata.Connections.Comments.Total)
	setVimeoNonNegativeCount(info, "like_count", response.Metadata.Connections.Likes.Total)
}

func vimeoAPITimestamp(raw string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

func setVimeoNonNegativeCount(info *value.Info, key string, count *int64) {
	if info != nil && count != nil && *count >= 0 {
		info.Set(key, value.Int(*count))
	}
}

// fetchVimeoUnlistedConfig fetches the player config URL returned by the
// authenticated API. It uses the credential-isolated, no-redirect executor
// so neither the viewer cookie nor the API JWT can leak into the config
// origin. On non-2xx the helper returns a secret-safe typed error so the
// caller cannot be tricked into revealing the URL.
func fetchVimeoUnlistedConfig(ctx context.Context, transport CredentialIsolatedNoRedirectTransport, normalizedConfigURL, webpageURL string) (vimeoConfig, error) {
	if err := contextError(ctx); err != nil {
		return vimeoConfig{}, err
	}
	var config vimeoConfig
	headers := http.Header{}
	headers.Set("Accept", "application/json")
	headers.Set("Referer", configRefererURL(webpageURL, webpageURL))
	if err := requestJSON(ctx, transport.DoWithoutCredentialsNoRedirect, http.MethodGet, normalizedConfigURL, nil, headers, &config); err != nil {
		return vimeoConfig{}, err
	}
	return config, nil
}

// parseVimeoUnlistedConfigContext reuses parseVimeoConfigContext with the
// authenticated path's options. The only behavioral change vs. the public
// path is to disable the anonymous texttracks API fallback so the merged
// JWT cannot be silently reattached at that boundary.
func parseVimeoUnlistedConfigContext(ctx context.Context, transport Transport, config vimeoConfig, videoID, webpageURL, referer string) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	files := config.Video.Files
	if len(files.Progressive) == 0 && len(files.HLS.CDNs) == 0 && len(files.DASH.CDNs) == 0 {
		files = config.Request.Files
	}
	formats, err := buildVimeoFormats(ctx, files)
	if err != nil {
		return Extraction{}, err
	}
	liveStatus := map[string]string{"pending": "is_upcoming", "active": "is_upcoming", "started": "is_live", "ended": "post_live"}[config.Video.LiveEvent.Status]
	if len(formats) == 0 {
		if liveStatus == "is_upcoming" {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: no Vimeo formats", ErrInvalidMetadata)
	}
	if config.View == 4 {
		return Extraction{}, ErrAuthentication
	}
	if config.Video.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Vimeo title", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(config.Video.Title)},
		value.Field{Key: "description", Value: value.String(config.Video.Description)},
		value.Field{Key: "uploader", Value: value.String(config.Video.Owner.Name)},
		value.Field{Key: "uploader_url", Value: value.String(config.Video.Owner.URL)},
		value.Field{Key: "webpage_url", Value: value.String(vimeoPublicWebpageURL(webpageURL))},
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
	if subtitles, err := mergeVimeoSubtitles(ctx, transport, videoID, config, files, vimeoSubtitleMergeOptions{skipAnonymousAPI: true}); err != nil {
		return Extraction{}, err
	} else if subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

// buildVimeoFormats is a small shared helper used by both the public and
// authenticated config parsers to convert the vimeoFiles envelope into a
// bounded list of value formats. Centralizing the construction keeps the
// progressive/HLS/DASH output identical across routes.
func buildVimeoFormats(ctx context.Context, files vimeoFiles) ([]value.Value, error) {
	formats := make([]value.Value, 0, len(files.Progressive)+len(files.HLS.CDNs)+len(files.DASH.CDNs))
	for _, format := range files.Progressive {
		if err := contextError(ctx); err != nil {
			return nil, err
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
			return nil, err
		}
		cdn := files.HLS.CDNs[name]
		if validHTTPURL(cdn.URL) {
			formats = append(formats, value.ObjectValue(manifestFormat("hls-"+name, cdn.URL, "m3u8_native")))
		}
	}
	for _, name := range sortedVimeoCDNs(files.DASH.CDNs) {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		cdn := files.DASH.CDNs[name]
		if validHTTPURL(cdn.URL) {
			manifestURL := strings.Replace(cdn.URL, "/master.json", "/master.mpd", 1)
			formats = append(formats, value.ObjectValue(manifestFormat("dash-"+name, manifestURL, "http_dash_segments")))
		}
	}
	return formats, nil
}

func configRefererURL(webpageURL, referer string) string {
	if validated, ok := validVimeoReferer(referer); ok {
		return validated
	}
	return webpageURL
}

func classifyVimeoURL(parsed *url.URL) (vimeoRouteKind, vimeoPlaylistTarget) {
	if parsed == nil {
		return vimeoRouteNone, vimeoPlaylistTarget{}
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "player.vimeo.com" {
		if target, ok := classifyVimeoPlayerVideoURL(parsed); ok {
			return vimeoRouteVideo, target
		}
		return vimeoRouteNone, vimeoPlaylistTarget{}
	}
	if host != "vimeo.com" && host != "www.vimeo.com" {
		return vimeoRouteNone, vimeoPlaylistTarget{}
	}
	if target, ok := classifyVimeoUnlistedURL(parsed); ok {
		return vimeoRouteVideo, target
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

// classifyVimeoPlayerVideoURL preserves the bounded player request context for
// the page fetch while canonicalizing the origin. Query parameters are needed
// by some embeds, but are removed before the password endpoint is constructed.
func classifyVimeoPlayerVideoURL(parsed *url.URL) (vimeoPlaylistTarget, bool) {
	if parsed == nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Port() != "" ||
		parsed.Opaque != "" || parsed.RawPath != "" || parsed.Fragment != "" || parsed.RawFragment != "" ||
		len(parsed.RawQuery) > vimeoMaxConfigURL || strings.ContainsAny(parsed.RawQuery, "\\\x00\r\n") || vimeoUnsafePath(parsed) {
		return vimeoPlaylistTarget{}, false
	}
	match := vimeoURLPattern.FindStringSubmatch(parsed.Path)
	if len(match) != 2 || !validVimeoNumericVideoID(match[1]) {
		return vimeoPlaylistTarget{}, false
	}
	canonical := &url.URL{
		Scheme:   "https",
		Host:     "player.vimeo.com",
		Path:     parsed.Path,
		RawQuery: parsed.RawQuery,
	}
	if len(canonical.String()) > vimeoMaxConfigURL {
		return vimeoPlaylistTarget{}, false
	}
	return vimeoPlaylistTarget{kind: vimeoRouteVideo, id: match[1], canonical: canonical.String()}, true
}

// classifyVimeoUnlistedURL accepts only direct canonical Vimeo URLs of the
// form https://vimeo.com/{numeric_id}/{unlisted_hash} where unlisted_hash is
// exactly 10 lowercase hex bytes. Player URLs, contextual routes, embedded
// share slugs, alternate host layouts, encoded variants, userinfo, ports,
// and extra path segments are rejected before the parser returns. Upstream-
// compatible query and fragment forms are accepted but deliberately stripped
// from the canonical URL, so caller tokens can never reach the viewer, API,
// config, or CDN credential boundaries. The returned target carries the
// validated unlistedHash exactly as supplied on the URL line.
func classifyVimeoUnlistedURL(parsed *url.URL) (vimeoPlaylistTarget, bool) {
	if parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Opaque != "" || strings.Contains(parsed.String(), "\x00") {
		return vimeoPlaylistTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "vimeo.com" && host != "www.vimeo.com" {
		return vimeoPlaylistTarget{}, false
	}
	if parsed.RawPath != "" || vimeoUnsafePath(parsed) {
		return vimeoPlaylistTarget{}, false
	}
	parts := splitVimeoPath(parsed.Path)
	if len(parts) != 2 {
		return vimeoPlaylistTarget{}, false
	}
	numericID, hash := parts[0], parts[1]
	if !validVimeoNumericVideoID(numericID) || len(hash) != vimeoUnlistedHashLen || !vimeoUnlistedHashPattern.MatchString(hash) {
		return vimeoPlaylistTarget{}, false
	}
	return vimeoPlaylistTarget{
		kind:         vimeoRouteVideo,
		id:           numericID,
		canonical:    "https://vimeo.com/" + numericID + "/" + hash,
		unlistedHash: hash,
	}, true
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
	config, err := fetchVimeoConfig(ctx, transport, configURL, headers)
	if err != nil {
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

// fetchVimeoConfig recognizes only Vimeo's bounded, structured 400 password
// signal. Other 400s remain ordinary HTTP errors and therefore cannot trigger
// a secret-bearing retry.
func fetchVimeoConfig(ctx context.Context, transport Transport, rawURL string, headers http.Header) (vimeoConfig, error) {
	if err := contextError(ctx); err != nil {
		return vimeoConfig{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return vimeoConfig{}, ErrInvalidMetadata
	}
	request.Header = headers.Clone()
	// Player config URLs carry signed query bytes.  When the transport exposes
	// the credential-isolated no-redirect boundary, keep those bytes on this
	// exact request only; never let cookies or a redirect target inherit them.
	isolate, ok := transport.(RefererCredentialIsolatedNoRedirectTransport)
	if !ok {
		return vimeoConfig{}, ErrTransportIsolation
	}
	execute := isolate.DoWithoutCredentialsNoRedirectWithReferer
	response, err := execute(ctx, request)
	if err != nil {
		return vimeoConfig{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxExtractorJSONBytes+1))
	if err != nil || int64(len(data)) > maxExtractorJSONBytes {
		return vimeoConfig{}, ErrJSONResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusBadRequest && vimeoConfigPasswordRequired(data) {
			return vimeoConfig{}, ErrAuthentication
		}
		if categorized := categorizeVimeoResponseStatus(rawURL, response.StatusCode, data); categorized != nil {
			return vimeoConfig{}, categorized
		}
		return vimeoConfig{}, &HTTPStatusError{Code: response.StatusCode}
	}
	var config vimeoConfig
	if json.Unmarshal(data, &config) != nil {
		return vimeoConfig{}, fmt.Errorf("%w: invalid JSON response", ErrInvalidMetadata)
	}
	return config, nil
}

// categorizeVimeoResponseStatus intentionally recognizes only the two
// pinned fingerprint/DC-IP response contexts.  In particular, a vimeo.com
// 429 and a player.vimeo.com 403 remain ordinary HTTP failures: neither is a
// generic rate-limit signal.  A bounded 5460 JSON signal remains the
// established authentication category regardless of its enclosing status.
func categorizeVimeoResponseStatus(rawURL string, status int, body []byte) error {
	if matchesVimeoUnlistedErrorCode(body, vimeoUnlistedAPIErrorCode) {
		return ErrAuthentication
	}
	// This exact pinned privacy message is an embed-only authorization signal
	// only in the two retry contexts.  A phrase in an arbitrary 3xx/5xx body is
	// ordinary server output and must not change its public error category.
	if isVimeoPrivacyRetryStatus(rawURL, status) && isVimeoEmbedOnlyBody(body) {
		return ErrAuthentication
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return nil
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "vimeo.com", "www.vimeo.com":
		if status == http.StatusForbidden {
			return ErrTransportProfile
		}
	case "player.vimeo.com":
		if status == http.StatusTooManyRequests {
			return ErrTransportProfile
		}
	}
	return nil
}

func vimeoConfigPasswordRequired(data []byte) bool {
	if len(data) == 0 || len(data) > 8<<10 {
		return false
	}
	var response struct {
		InvalidParameters []struct {
			Field string `json:"field"`
		} `json:"invalid_parameters"`
	}
	if json.Unmarshal(data, &response) != nil {
		return false
	}
	for _, parameter := range response.InvalidParameters {
		if parameter.Field == "password" {
			return true
		}
	}
	return false
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
	return parseVimeoConfigContext(context.Background(), nil, config, videoID, webpageURL, "")
}

func parseVimeoConfigContext(ctx context.Context, transport Transport, config vimeoConfig, videoID, webpageURL, referer string) (Extraction, error) {
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
		value.Field{Key: "webpage_url", Value: value.String(vimeoPublicWebpageURL(webpageURL))},
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
	if subtitles, err := mergeVimeoSubtitles(ctx, transport, videoID, config, files, vimeoSubtitleMergeOptions{}); err != nil {
		return Extraction{}, err
	} else if subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

// vimeoSubtitleMergeOptions bounds which subtitle sources participate in a
// merge. The zero value matches the public extractor: primary config text
// tracks, an anonymous API fallback when the transport supports it, and
// the credential-isolated manifest fallback. The authenticated unlisted
// path sets skipAnonymousAPI to forbid the anonymous JWT (texttracks) call
// so the auth flow does not silently re-attach the leaked JWT.
type vimeoSubtitleMergeOptions struct {
	skipAnonymousAPI bool
}

func validVimeoReferer(rawReferer string) (string, bool) {
	rawReferer = strings.TrimSpace(rawReferer)
	if rawReferer == "" || len(rawReferer) > vimeoMaxReferer || strings.ContainsAny(rawReferer, "\x00\r\n") {
		return "", false
	}
	parsed, err := url.Parse(rawReferer)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", false
	}
	if strictURLPolicyRejects(parsed) || parsed.Hostname() == "" {
		return "", false
	}
	parsed.Scheme = "https"
	parsed.Host = parsed.Hostname()
	return parsed.String(), true
}

func vimeoPublicWebpageURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parsed.RawQuery, parsed.ForceQuery, parsed.Fragment, parsed.RawFragment = "", false, "", ""
	return parsed.String()
}

func vimeoReferrerHostname(referer string) string {
	validated, ok := validVimeoReferer(referer)
	if !ok {
		return ""
	}
	parsed, _ := url.Parse(validated)
	return parsed.Hostname()
}

type vimeoSubtitleCandidate struct {
	language string
	url      string
	name     string
	ext      string
	primary  bool
	isolated bool
}

func mergeVimeoSubtitles(ctx context.Context, transport Transport, videoID string, config vimeoConfig, files vimeoFiles, opts vimeoSubtitleMergeOptions) (*value.Object, error) {
	candidates := make([]vimeoSubtitleCandidate, 0, vimeoMaxTextTracks)
	if primary, err := vimeoSubtitleCandidatesFromTracks(ctx, config.Request.TextTracks, true); err != nil {
		return nil, err
	} else if candidates, err = appendBoundedVimeoSubtitleCandidates(candidates, primary); err != nil {
		return nil, err
	}
	if transport != nil && !opts.skipAnonymousAPI {
		if apiTracks, err := vimeoSubtitleCandidatesFromAPI(ctx, transport, videoID); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
		} else if candidates, err = appendBoundedVimeoSubtitleCandidates(candidates, apiTracks); err != nil {
			return nil, err
		}
	}
	if transport != nil {
		if manifestTracks, err := vimeoSubtitleCandidatesFromManifests(ctx, transport, files); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
		} else if candidates, err = appendBoundedVimeoSubtitleCandidates(candidates, manifestTracks); err != nil {
			return nil, err
		}
	}
	return vimeoSubtitlesFromCandidates(candidates)
}

func appendBoundedVimeoSubtitleCandidates(existing, incoming []vimeoSubtitleCandidate) ([]vimeoSubtitleCandidate, error) {
	if len(incoming) > vimeoMaxTextTracks {
		return nil, fmt.Errorf("%w: Vimeo text-track limit", ErrInvalidMetadata)
	}
	remaining := vimeoMaxTextTracks - len(existing)
	if remaining <= 0 {
		return existing, nil
	}
	if len(incoming) > remaining {
		incoming = incoming[:remaining]
	}
	return append(existing, incoming...), nil
}

func vimeoSubtitleCandidatesFromTracks(ctx context.Context, tracks []vimeoTextTrack, primary bool) ([]vimeoSubtitleCandidate, error) {
	if len(tracks) > vimeoMaxTextTracks {
		return nil, fmt.Errorf("%w: Vimeo text-track limit", ErrInvalidMetadata)
	}
	out := make([]vimeoSubtitleCandidate, 0, len(tracks))
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
		out = append(out, vimeoSubtitleCandidate{language: language, url: trackURL, name: name, ext: "vtt", primary: primary})
	}
	return out, nil
}

func vimeoSubtitleCandidatesFromAPI(ctx context.Context, transport Transport, videoID string) ([]vimeoSubtitleCandidate, error) {
	viewerTransport, viewerOK := transport.(CredentialIsolatedNoRedirectTransport)
	_, scopedOK := transport.(ScopedAuthorizationNoRedirectTransport)
	if !viewerOK || !scopedOK {
		return nil, nil
	}
	provider := &vimeoViewerTokenProvider{transport: viewerTransport, now: time.Now}
	var payload struct {
		Data []struct {
			Language        string `json:"language"`
			Link            string `json:"link"`
			DisplayLanguage string `json:"display_language"`
		} `json:"data"`
	}
	query := url.Values{
		"include_transcript": {"true"},
		"fields":             {"active,display_language,id,language,link,name,type,uri"},
	}
	endpoint := "https://api.vimeo.com/videos/" + videoID + "/texttracks?" + query.Encode()
	err := withVimeoViewerToken(ctx, provider, func(jwt string) error {
		return RequestJSONWithScopedAuthorizationNoRedirect(
			ctx, transport, http.MethodGet, endpoint, nil, vimeoAPIHeaders(jwt), &payload)
	})
	if err != nil {
		var status *HTTPStatusError
		if errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden || status.Code == http.StatusNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if len(payload.Data) > vimeoMaxTextTracks {
		return nil, fmt.Errorf("%w: Vimeo text-track limit", ErrInvalidMetadata)
	}
	out := make([]vimeoSubtitleCandidate, 0, len(payload.Data))
	for _, track := range payload.Data {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		language := boundedVimeoText(track.Language, vimeoMaxTextLang)
		trackURL := normalizeVimeoManifestSubtitleURL(track.Link)
		if !validVimeoLanguage(language) || trackURL == "" {
			continue
		}
		out = append(out, vimeoSubtitleCandidate{
			language: language,
			url:      trackURL,
			name:     boundedVimeoText(track.DisplayLanguage, vimeoMaxTextName),
			ext:      vimeoSubtitleExtension(trackURL),
			isolated: true,
		})
	}
	return out, nil
}

func vimeoSubtitleCandidatesFromManifests(ctx context.Context, transport Transport, files vimeoFiles) ([]vimeoSubtitleCandidate, error) {
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, nil
	}
	out := make([]vimeoSubtitleCandidate, 0, 8)
	for _, name := range sortedVimeoCDNs(files.HLS.CDNs) {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		cdn := files.HLS.CDNs[name]
		if !validHTTPURL(cdn.URL) {
			continue
		}
		payload, err := fetchVimeoManifestWithoutCredentials(ctx, isolated, cdn.URL)
		if err != nil || len(payload) == 0 || int64(len(payload)) > vimeoMaxManifest {
			continue
		}
		renditions, err := hls.ParseMasterSubtitles(cdn.URL, payload)
		if err != nil {
			continue
		}
		for _, rendition := range renditions {
			trackURL := normalizeVimeoManifestSubtitleURL(rendition.URL)
			language := boundedVimeoText(rendition.Language, vimeoMaxTextLang)
			if trackURL == "" || !validVimeoLanguage(language) || !vimeoHLSSubtitlePlaylistURL(trackURL) {
				continue
			}
			if len(out) >= vimeoMaxTextTracks {
				break
			}
			out = append(out, vimeoSubtitleCandidate{
				language: language,
				url:      trackURL,
				name:     boundedVimeoText(rendition.Name, vimeoMaxTextName),
				ext:      "vtt",
				isolated: true,
			})
		}
	}
	for _, name := range sortedVimeoCDNs(files.DASH.CDNs) {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		cdn := files.DASH.CDNs[name]
		if !validHTTPURL(cdn.URL) {
			continue
		}
		manifestURL := strings.Replace(cdn.URL, "/master.json", "/master.mpd", 1)
		payload, err := fetchVimeoManifestWithoutCredentials(ctx, isolated, manifestURL)
		if err != nil || len(payload) == 0 || int64(len(payload)) > vimeoMaxManifest {
			continue
		}
		representations, err := dash.ParseTextRepresentations(manifestURL, payload)
		if err != nil {
			continue
		}
		for _, representation := range representations {
			trackURL := normalizeVimeoManifestSubtitleURL(representation.URL)
			language := boundedVimeoText(representation.Language, vimeoMaxTextLang)
			if trackURL == "" || !validVimeoLanguage(language) {
				continue
			}
			if len(out) >= vimeoMaxTextTracks {
				break
			}
			out = append(out, vimeoSubtitleCandidate{
				language: language,
				url:      trackURL,
				name:     boundedVimeoText(representation.Name, vimeoMaxTextName),
				ext:      vimeoSubtitleExtension(trackURL),
				isolated: true,
			})
		}
	}
	return out, nil
}

func fetchVimeoManifestWithoutCredentials(ctx context.Context, transport CredentialIsolatedNoRedirectTransport, rawURL string) ([]byte, error) {
	if !strictValidHostedHTTPURL(rawURL) {
		return nil, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil
	}
	response, err := transport.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(response.Body, vimeoMaxManifest+1))
}

func vimeoSubtitlesFromCandidates(candidates []vimeoSubtitleCandidate) (*value.Object, error) {
	if len(candidates) > vimeoMaxTextTracks {
		return nil, fmt.Errorf("%w: Vimeo text-track limit", ErrInvalidMetadata)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].primary == candidates[j].primary {
			return false
		}
		return candidates[i].primary
	})
	grouped := make(map[string][]value.Value)
	order := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		key := strings.ToLower(candidate.language) + "\x00" + candidate.url
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if _, exists := grouped[candidate.language]; !exists {
			order = append(order, candidate.language)
		}
		entry := value.NewObject(
			value.Field{Key: "url", Value: value.String(candidate.url)},
			value.Field{Key: "ext", Value: value.String(candidate.ext)},
		)
		if candidate.name != "" {
			entry.Set("name", value.String(candidate.name))
		}
		if candidate.isolated {
			entry.Set("_credential_isolated", value.Bool(true))
		}
		grouped[candidate.language] = append(grouped[candidate.language], value.ObjectValue(entry))
	}
	result := value.NewObject()
	for _, language := range order {
		result.Set(language, value.List(grouped[language]...))
	}
	return result, nil
}

func normalizeVimeoManifestSubtitleURL(rawURL string) string {
	if len(rawURL) == 0 || len(rawURL) > vimeoMaxTextURL || strings.ContainsAny(rawURL, "\\\x00\r\n") {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || strictURLPolicyRejects(parsed) || parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

func vimeoSubtitleExtension(rawURL string) string {
	extension := strings.TrimPrefix(path.Ext(mustURLPath(rawURL)), ".")
	switch strings.ToLower(extension) {
	case "m3u8":
		return "vtt"
	case "vtt", "srt", "ttml", "dfxp", "srv1", "srv2", "srv3":
		if strings.EqualFold(extension, "dfxp") {
			return "ttml"
		}
		return strings.ToLower(extension)
	default:
		return "vtt"
	}
}

func vimeoHLSSubtitlePlaylistURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(path.Ext(parsed.Path), ".m3u8")
}

type vimeoViewerTokenProvider struct {
	transport CredentialIsolatedNoRedirectTransport
	now       func() time.Time
	mu        sync.Mutex
	token     string
	expires   int64
}

func (provider *vimeoViewerTokenProvider) get(ctx context.Context) (string, error) {
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

func (provider *vimeoViewerTokenProvider) invalidate(token string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.token == token {
		provider.token, provider.expires = "", 0
	}
}

func withVimeoViewerToken(ctx context.Context, provider *vimeoViewerTokenProvider, request func(string) error) error {
	if provider == nil || request == nil {
		return fmt.Errorf("%w: missing Vimeo viewer token provider", ErrInvalidMetadata)
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

func vimeoAPIHeaders(jwt string) http.Header {
	return http.Header{
		"Accept":        {"application/json"},
		"Authorization": {"jwt " + jwt},
	}
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

// parseVimeoViewerXSRFT extracts a bounded token from exactly one Vimeo
// _next/viewer JSON object. It is pure and all failures use one fixed message so
// payload and token bytes cannot enter diagnostics.
func parseVimeoViewerXSRFT(payload []byte) (string, error) {
	invalid := func() (string, error) {
		return "", fmt.Errorf("%w: invalid Vimeo viewer xsrft", ErrInvalidMetadata)
	}
	if len(payload) == 0 || int64(len(payload)) > maxExtractorJSONBytes {
		return invalid()
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return invalid()
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return invalid()
	}
	rawToken, ok := raw["xsrft"]
	if !ok || !utf8.Valid(rawToken) {
		return invalid()
	}
	var token string
	if err := json.Unmarshal(rawToken, &token); err != nil || token == "" ||
		len(token) > maxVimeoViewerXSRFTBytes || !utf8.ValidString(token) ||
		strings.ContainsAny(token, "\x00\r\n") || strings.TrimSpace(token) != token {
		return invalid()
	}
	return token, nil
}
