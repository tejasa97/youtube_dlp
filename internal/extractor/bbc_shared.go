package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	bbcMediaSelectorBase  = "https://open.live.bbc.co.uk/mediaselector/6/select/version/2.0/mediaset/"
	bbcPlaylistGraphQL    = "https://graph.ibl.api.bbc.co.uk/"
	bbcGroupAPIBase       = "https://ibl.api.bbc.co.uk/ibl/v1/groups/"
	bbcEpisodesPageSize   = 100
	bbcGroupPageSize      = 200
	bbcExplicitPageSize   = 36
	bbcGraphQLQueryID     = "5692d93d5aac8d796a0305e895e61551"
	bbcMaxHTMLBytes       = 4 << 20
	bbcMaxPlaylistEntries = defaultMaxPlaylistEntries
	bbcCanonicalHost      = "www.bbc.co.uk"
	bbcCanonicalOrigin    = "https://www.bbc.co.uk"
)

var (
	bbcPIDPattern          = regexp.MustCompile(`^[pbmlw][0-9a-z]{7,14}$`)
	bbcArticleIDPattern    = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
	bbcVpidPattern         = regexp.MustCompile(`(?i)["']vpid["']\s*:\s*["']([pbmlw][0-9a-z]{7,14})["']`)
	bbcOGTitle             = regexp.MustCompile(`(?is)<meta\b[^>]*(?:property|name)=["']og:title["'][^>]*content=["']([^"']+)["']`)
	bbcMetaDesc            = regexp.MustCompile(`(?is)<meta\b[^>]*name=["']description["'][^>]*content=["']([^"']*)["']`)
	bbcEpisodePath         = regexp.MustCompile(`^/iplayer/episode/([pbmlw][0-9a-z]{7,14})(?:/[^/?#]*)?/?$`)
	bbcProgrammePath       = regexp.MustCompile(`^/programmes/([pbmlw][0-9a-z]{7,14})(?:/player)?/?$`)
	bbcArticlePath         = regexp.MustCompile(`^/programmes/articles/([a-zA-Z0-9]+)(?:/[^/?#]*)?/?$`)
	bbcProgrammesListPath  = regexp.MustCompile(`^/programmes/([pbmlw][0-9a-z]{7,14})/(episodes|broadcasts|clips)(?:/[^/?#]*)?/?$`)
	bbcIPlayerEpisodes     = regexp.MustCompile(`^/iplayer/episodes/([pbmlw][0-9a-z]{7,14})(?:/[^/?#]*)?/?$`)
	bbcIPlayerGroup        = regexp.MustCompile(`^/iplayer/group/([pbmlw][0-9a-z]{7,14})(?:/[^/?#]*)?/?$`)
	bbcArticleClipPattern  = regexp.MustCompile(`(?is)<div[^>]+typeof=["']Clip["'][^>]+resource=["']([^"']+)["']`)
	bbcProgrammePIDPattern = regexp.MustCompile(`(?is)data-pid=["']([pbmlw][0-9a-z]{7,14})["']`)
	bbcPaginationNext      = regexp.MustCompile(`(?is)<li[^>]+class=["']pagination_+next["'][^>]*><a[^>]+href=["']([^"']+)["']`)
)

func bbcValidHost(parsed *url.URL) bool {
	if parsed == nil || hostedRejectUnsafeURL(parsed) {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "bbc.co.uk" || host == bbcCanonicalHost
}

func bbcCanonicalEpisodeURL(pid string) string {
	return bbcCanonicalOrigin + "/iplayer/episode/" + pid
}

func bbcCanonicalProgrammeURL(pid string) string {
	return bbcCanonicalOrigin + "/programmes/" + pid
}

func bbcTrustedContinuationURL(base *url.URL, reference string) (string, bool) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", false
	}
	resolved, err := url.Parse(reference)
	if err != nil {
		return "", false
	}
	if !resolved.IsAbs() {
		resolved = base.ResolveReference(resolved)
	}
	if !bbcValidHost(resolved) {
		return "", false
	}
	resolved.Fragment = ""
	return resolved.String(), true
}

func bbcTrustedProgrammeURL(raw string) (string, string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !bbcValidHost(parsed) {
		return "", "", false
	}
	parsed.Fragment = ""
	parsed.RawQuery = ""
	if match := bbcProgrammePath.FindStringSubmatch(parsed.Path); len(match) == 2 && bbcPIDPattern.MatchString(match[1]) {
		return bbcCanonicalProgrammeURL(match[1]), match[1], true
	}
	if match := bbcEpisodePath.FindStringSubmatch(parsed.Path); len(match) == 2 && bbcPIDPattern.MatchString(match[1]) {
		return bbcCanonicalEpisodeURL(match[1]), match[1], true
	}
	return "", "", false
}

func bbcEpisodeChildEntry(episodeID, title string) Entry {
	return Entry{
		URL:          bbcCanonicalEpisodeURL(episodeID),
		ExtractorKey: "bbciplayer",
		ID:           episodeID,
		Title:        title,
		Transparent:  true,
	}
}

func bbcProgrammeChildEntry(programmeID string) Entry {
	return Entry{
		URL:          bbcCanonicalProgrammeURL(programmeID),
		ExtractorKey: "bbciplayer",
		ID:           programmeID,
		Transparent:  true,
	}
}

func bbcMarkCredentialIsolated(object *value.Object) {
	object.Set("_credential_isolated", value.Bool(true))
}

func requestBBCIsolatedPage(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if transport == nil {
		return nil, ErrUnsupported
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid BBC page request", ErrInvalidMetadata)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, fmt.Errorf("%w: empty BBC page response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.Body == nil {
		return nil, fmt.Errorf("%w: empty BBC page response body", ErrInvalidMetadata)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, categorizeBBCPageError(&HTTPStatusError{Code: response.StatusCode})
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, bbcMaxHTMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read BBC page failed", ErrInvalidMetadata)
	}
	if len(body) > bbcMaxHTMLBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return body, nil
}

func requestBBCIsolatedJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	if transport == nil || target == nil {
		return fmt.Errorf("%w: invalid BBC JSON request", ErrInvalidMetadata)
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return fmt.Errorf("%w: invalid BBC JSON request", ErrInvalidMetadata)
	}
	request.Header = headers.Clone()
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return err
	}
	if response == nil {
		return fmt.Errorf("%w: empty BBC JSON response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.Body == nil {
		return fmt.Errorf("%w: empty BBC JSON response body", ErrInvalidMetadata)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPStatusError{Code: response.StatusCode}
	}
	reader := &io.LimitedReader{R: response.Body, N: riskExtractorMaxJSONBytes + 1}
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("%w: read BBC JSON response failed", ErrInvalidMetadata)
	}
	if int64(len(data)) > riskExtractorMaxJSONBytes {
		return ErrJSONResponseTooLarge
	}
	return decodeRiskJSON(data, target)
}

func decodeRiskJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON response", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing JSON response", ErrInvalidMetadata)
	}
	return nil
}

func categorizeBBCPageError(err error) error {
	switch riskHTTPStatus(err) {
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		return err
	}
}

func categorizeBBCAPIError(err error) error {
	switch riskHTTPStatus(err) {
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		return err
	}
}

func bbcHTMLField(page []byte, pattern *regexp.Regexp) string {
	match := pattern.FindSubmatch(page)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(match[1])))
}

func firstBBCSynopsis(synopsis map[string]string) string {
	for _, key := range []string{"large", "medium", "small"} {
		if synopsis[key] != "" {
			return synopsis[key]
		}
	}
	return ""
}

func bbcPlaylistInfo(pid, title, webpageURL, description string) value.Info {
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(pid)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
	)
	riskString(info, "description", description)
	return value.NewInfo(info)
}

func bbcPageFingerprint(entries []Entry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, entry.ID)
	}
	return strings.Join(parts, ",")
}

func bbcDedupeEntries(entries []Entry) []Entry {
	seen := make(map[string]struct{}, len(entries))
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}
		if _, exists := seen[entry.ID]; exists {
			continue
		}
		seen[entry.ID] = struct{}{}
		out = append(out, entry)
	}
	return out
}
