package extractor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	bandcampMaxURLBytes      = 4 << 10
	bandcampMaxHTMLBytes     = 4 << 20
	bandcampMaxArtistBytes   = 201
	bandcampMaxShowIDDigits  = 12
	bandcampMaxShowIDValue   = 1_000_000_000_000
	bandcampMaxImageIDBytes  = 32
	bandcampMaxTextBytes     = 64 << 10
	bandcampWeeklyAPIURL     = "https://bandcamp.com/api/player/2/player_data_web"
	bandcampWeeklyThumbnail  = "https://f4.bcbits.com/img/%s_0.jpg"
	bandcampDiscographyLimit = defaultMaxPlaylistEntries
)

var ErrBandcampPageNetwork = errors.New("Bandcamp page network failure")

type bandcampArtistTarget struct {
	artist     string
	webpageURL string
}

func classifyBandcampArtistURL(parsed *url.URL) (bandcampArtistTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > bandcampMaxURLBytes ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" {
		return bandcampArtistTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.HasSuffix(host, ".bandcamp.com") || host == "bandcamp.com" || host == "www.bandcamp.com" {
		return bandcampArtistTarget{}, false
	}
	artist := strings.TrimSuffix(host, ".bandcamp.com")
	if artist == "" || artist == "www" || !bandcampSlug.MatchString(artist) || len(artist) > bandcampMaxArtistBytes {
		return bandcampArtistTarget{}, false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return bandcampArtistTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 1 && parts[0] == "" {
		// artist root
	} else if len(parts) == 1 && parts[0] == "music" {
		// /music route
	} else {
		return bandcampArtistTarget{}, false
	}
	if !utf8.ValidString(parsed.RawQuery) || !utf8.ValidString(parsed.Fragment) {
		return bandcampArtistTarget{}, false
	}
	webpage := parsed.Scheme + "://" + host
	if len(parts) == 1 && parts[0] == "music" {
		webpage += "/music"
	}
	return bandcampArtistTarget{artist: artist, webpageURL: webpage}, true
}

type bandcampWeeklyTarget struct {
	showID     string
	webpageURL string
}

func classifyBandcampWeeklyURL(parsed *url.URL) (bandcampWeeklyTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > bandcampMaxURLBytes ||
		parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return bandcampWeeklyTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "bandcamp.com" && host != "www.bandcamp.com" {
		return bandcampWeeklyTarget{}, false
	}
	if parsed.Fragment != "" {
		return bandcampWeeklyTarget{}, false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if escaped != "/radio" && escaped != "/radio/" {
		return bandcampWeeklyTarget{}, false
	}
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return bandcampWeeklyTarget{}, false
	}
	showID, ok := bandcampWeeklyShowFromQuery(parsed.RawQuery)
	if !ok {
		return bandcampWeeklyTarget{}, false
	}
	return bandcampWeeklyTarget{
		showID:     showID,
		webpageURL: parsed.Scheme + "://" + host + parsed.EscapedPath() + "?" + parsed.RawQuery,
	}, true
}

func bandcampWeeklyShowFromQuery(rawQuery string) (string, bool) {
	if rawQuery == "" || !utf8.ValidString(rawQuery) || !bandcampValidPercentEncoding(rawQuery) {
		return "", false
	}
	showID := ""
	showCount := 0
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			return "", false
		}
		name, value, found := strings.Cut(part, "=")
		if !found {
			return "", false
		}
		key, err := url.QueryUnescape(name)
		if err != nil || key == "" {
			return "", false
		}
		decodedValue, err := url.QueryUnescape(value)
		if err != nil {
			return "", false
		}
		if key != "show" {
			continue
		}
		showCount++
		if showCount > 1 || !bandcampWeeklyShowID(decodedValue) {
			return "", false
		}
		showID = decodedValue
	}
	if showCount != 1 {
		return "", false
	}
	return showID, true
}

func bandcampValidPercentEncoding(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '%' {
			continue
		}
		if i+2 >= len(raw) {
			return false
		}
		for _, digit := range []byte{raw[i+1], raw[i+2]} {
			if !((digit >= '0' && digit <= '9') || (digit >= 'a' && digit <= 'f') || (digit >= 'A' && digit <= 'F')) {
				return false
			}
		}
	}
	return true
}

func bandcampWeeklyShowID(showID string) bool {
	if showID == "" || len(showID) > bandcampMaxShowIDDigits {
		return false
	}
	for i := 0; i < len(showID); i++ {
		if showID[i] < '0' || showID[i] > '9' {
			return false
		}
	}
	if showID[0] == '0' {
		return false
	}
	value, err := strconv.ParseInt(showID, 10, 64)
	return err == nil && value > 0 && value < bandcampMaxShowIDValue
}

func requestBandcampIsolatedPage(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if transport == nil {
		return nil, ErrUnsupported
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrInvalidMetadata
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("%w: empty Bandcamp page response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, categorizeBandcampPageStatus(response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, bandcampMaxHTMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read Bandcamp page failed", ErrBandcampPageNetwork)
	}
	if len(body) > bandcampMaxHTMLBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return body, nil
}

func categorizeBandcampPageStatus(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuthentication
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		return &HTTPStatusError{Code: status}
	}
}

func bandcampJoinURL(base *url.URL, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	resolved, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if !resolved.IsAbs() {
		resolved = base.ResolveReference(resolved)
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	resolved.Fragment = ""
	return resolved.String(), true
}

func bandcampSameArtistChild(artist string, raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Port() != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != artist+".bandcamp.com" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return false
	}
	switch parts[0] {
	case "track", "album":
		return bandcampSlug.MatchString(parts[1])
	default:
		return false
	}
}

func bandcampCanonicalChildURL(artist, raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Port() != "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != artist+".bandcamp.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || !bandcampSlug.MatchString(parts[1]) {
		return "", false
	}
	switch parts[0] {
	case "track", "album":
	default:
		return "", false
	}
	return parsed.Scheme + "://" + host + "/" + parts[0] + "/" + parts[1], true
}

func bandcampSafeThumbnailURL(imageID string) (string, bool) {
	imageID = strings.TrimSpace(imageID)
	if imageID == "" || len(imageID) > bandcampMaxImageIDBytes {
		return "", false
	}
	for i := 0; i < len(imageID); i++ {
		c := imageID[i]
		if c < '0' || (c > '9' && c < 'A') || (c > 'Z' && c < 'a') || c > 'z' {
			return "", false
		}
	}
	raw := fmt.Sprintf(bandcampWeeklyThumbnail, imageID)
	if !validHTTPURL(raw) {
		return "", false
	}
	return raw, true
}

func bandcampSafeStreamURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return "", false
	}
	if !bandcampTrustedMediaHost(parsed.Hostname()) {
		return "", false
	}
	if parsed.Fragment != "" {
		return "", false
	}
	return raw, true
}

func bandcampTrustedMediaHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || strings.Contains(host, "..") {
		return false
	}
	switch {
	case host == "bandcamp.com":
		return true
	case strings.HasSuffix(host, ".bandcamp.com"):
		label := strings.TrimSuffix(host, ".bandcamp.com")
		return label != "" && !strings.Contains(label, ".")
	case strings.HasSuffix(host, ".bcbits.com"):
		label := strings.TrimSuffix(host, ".bcbits.com")
		return label != "" && !strings.Contains(label, ".")
	default:
		return false
	}
}

func bandcampFormatIDFromStreamURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Query().Get("enc")
}

func bandcampBoundedText(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > bandcampMaxTextBytes || !utf8.ValidString(raw) {
		return "", false
	}
	return raw, true
}
