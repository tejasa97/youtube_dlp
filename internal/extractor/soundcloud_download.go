package extractor

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const soundCloudOriginalMaxRedirects = 10

var ErrSoundCloudOriginalRateLimited = errors.New("SoundCloud original download rate limited")

type soundCloudDownloadResponse struct {
	RedirectURI string `json:"redirectUri"`
}

func (extractor *SoundCloud) resolveOriginalDownload(
	ctx context.Context,
	transport Transport,
	trackID, secretToken string,
) (*value.Object, error) {
	clientID, err := extractor.discoverClientID(ctx, transport, false)
	if err != nil {
		return nil, err
	}
	endpoint := soundCloudAPIBase + "tracks/" + trackID + "/download"
	endpoint = addSoundCloudQuery(endpoint, "secret_token", secretToken)
	endpoint = addSoundCloudQuery(endpoint, "client_id", clientID)
	var download soundCloudDownloadResponse
	err = RequestJSON(ctx, transport, http.MethodGet, endpoint, nil, nil, &download)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		var status *HTTPStatusError
		if errors.As(err, &status) {
			if status.Code == http.StatusTooManyRequests {
				return nil, fmt.Errorf("%w: HTTP status %d", ErrSoundCloudOriginalRateLimited, status.Code)
			}
			return nil, nil
		}
		// Original downloads are optional. Match yt-dlp's nonfatal warning path
		// for unavailable, malformed, or oversized download metadata.
		return nil, nil
	}
	if download.RedirectURI == "" {
		return nil, nil
	}
	finalURL, headers, err := resolveSoundCloudOriginalRedirect(ctx, transport, download.RedirectURI)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, nil
	}
	extension := soundCloudOriginalExtension(headers)
	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String("download")},
		value.Field{Key: "url", Value: value.String(finalURL)},
		value.Field{Key: "quality", Value: value.Int(10)},
		value.Field{Key: "format_note", Value: value.String("Original")},
		value.Field{Key: "vcodec", Value: value.String("none")},
		value.Field{Key: "protocol", Value: value.String("http")},
	)
	if extension != "" {
		format.Set("ext", value.String(extension))
	}
	if size, err := strconv.ParseInt(strings.TrimSpace(headers.Get("Content-Length")), 10, 64); err == nil && size > 0 {
		format.Set("filesize", value.Int(size))
	}
	return format, nil
}

func resolveSoundCloudOriginalRedirect(
	ctx context.Context,
	transport Transport,
	rawURL string,
) (string, http.Header, error) {
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return "", nil, ErrTransportIsolation
	}
	current, err := validateSoundCloudOriginalURL(rawURL)
	if err != nil {
		return "", nil, err
	}
	seen := make(map[string]struct{})
	for hop := 0; hop <= soundCloudOriginalMaxRedirects; hop++ {
		if err := contextError(ctx); err != nil {
			return "", nil, err
		}
		key := soundCloudOriginalURLKey(current)
		if _, duplicate := seen[key]; duplicate {
			return "", nil, fmt.Errorf("%w: SoundCloud original redirect loop", ErrInvalidMetadata)
		}
		seen[key] = struct{}{}
		request, err := http.NewRequestWithContext(ctx, http.MethodHead, current.String(), nil)
		if err != nil {
			return "", nil, fmt.Errorf("%w: invalid SoundCloud original request", ErrInvalidMetadata)
		}
		response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
		if err != nil {
			return "", nil, err
		}
		if response == nil {
			return "", nil, errors.New("empty SoundCloud original response")
		}
		if response.Body != nil {
			_ = response.Body.Close()
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return current.String(), response.Header.Clone(), nil
		}
		if response.StatusCode < 300 || response.StatusCode >= 400 {
			return "", nil, &HTTPStatusError{Code: response.StatusCode}
		}
		location := strings.TrimSpace(response.Header.Get("Location"))
		if location == "" {
			return "", nil, &HTTPStatusError{Code: response.StatusCode}
		}
		reference, parseErr := url.Parse(location)
		if parseErr != nil {
			return "", nil, fmt.Errorf("%w: invalid SoundCloud original redirect", ErrInvalidMetadata)
		}
		next := current.ResolveReference(reference)
		current, err = validateSoundCloudOriginalURL(next.String())
		if err != nil {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("%w: SoundCloud original redirect limit", ErrInvalidMetadata)
}

func validateSoundCloudOriginalURL(rawURL string) (*url.URL, error) {
	if len(rawURL) == 0 || len(rawURL) > soundCloudMaxURLBytes || strings.ContainsRune(rawURL, 0) {
		return nil, fmt.Errorf("%w: invalid SoundCloud original URL", ErrInvalidMetadata)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || !strictValidHostedHTTPURL(rawURL) ||
		soundCloudEncodedSeparators(parsed) || !soundCloudOriginalASCIIHost(parsed.Hostname()) {
		return nil, fmt.Errorf("%w: invalid SoundCloud original URL", ErrInvalidMetadata)
	}
	parsed.Scheme = "https"
	parsed.Host = strings.ToLower(parsed.Hostname())
	if len(parsed.String()) > soundCloudMaxURLBytes {
		return nil, fmt.Errorf("%w: oversized SoundCloud original URL", ErrInvalidMetadata)
	}
	return parsed, nil
}

func soundCloudOriginalASCIIHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, character := range host {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

func soundCloudOriginalURLKey(parsed *url.URL) string {
	return parsed.String()
}

func soundCloudOriginalExtension(headers http.Header) string {
	if mediaType, parameters, err := mime.ParseMediaType(headers.Get("Content-Disposition")); err == nil &&
		strings.EqualFold(mediaType, "attachment") {
		if extension := safeSoundCloudOriginalExtension(parameters["filename"]); extension != "" {
			return extension
		}
	}
	if extension := safeSoundCloudOriginalExtension(headers.Get("x-amz-meta-name")); extension != "" {
		return extension
	}
	if extension := normalizeSoundCloudOriginalExtension(headers.Get("x-amz-meta-file-type")); extension != "" {
		return extension
	}
	mediaType, _, _ := mime.ParseMediaType(strings.ToLower(strings.TrimSpace(headers.Get("Content-Type"))))
	switch mediaType {
	case "audio/aac", "audio/aacp", "audio/x-aac":
		return "aac"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/midi":
		return "mid"
	case "audio/mpeg":
		return "mp3"
	case "audio/mp4", "audio/x-m4a":
		return "m4a"
	case "audio/ogg", "application/ogg":
		return "ogg"
	case "audio/webm":
		return "webm"
	case "audio/x-matroska":
		return "mka"
	case "audio/x-realaudio":
		return "ra"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	}
	return ""
}

func safeSoundCloudOriginalExtension(filename string) string {
	return normalizeSoundCloudOriginalExtension(strings.TrimPrefix(path.Ext(filename), "."))
}

func normalizeSoundCloudOriginalExtension(extension string) string {
	extension = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(extension, ".")))
	if extension == "" || len(extension) > 16 {
		return ""
	}
	for _, character := range extension {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return ""
		}
	}
	return extension
}
