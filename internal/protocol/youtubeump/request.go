package youtubeump

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ValidateSABRURL enforces the trusted GoogleVideo host policy for SABR POSTs.
func ValidateSABRURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("%w: SABR endpoint must be HTTPS", ErrUnsupportedURL)
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: SABR endpoint has unsafe URL components", ErrUnsupportedURL)
	}
	escapedPath := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escapedPath, "%00") || strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") {
		return nil, fmt.Errorf("%w: encoded path separator in SABR URL", ErrUnsupportedURL)
	}
	if strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, `\`) {
		return nil, fmt.Errorf("%w: ambiguous SABR path", ErrUnsupportedURL)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if parsed.Port() != "" {
		return nil, fmt.Errorf("%w: SABR endpoint must not specify a port", ErrUnsupportedURL)
	}
	if strings.HasSuffix(parsed.Host, ":") || strings.Count(parsed.Host, ":") > 0 {
		return nil, fmt.Errorf("%w: SABR endpoint has explicit port", ErrUnsupportedURL)
	}
	if host != "googlevideo.com" && !strings.HasSuffix(host, ".googlevideo.com") {
		return nil, fmt.Errorf("%w: untrusted SABR host", ErrUnsupportedURL)
	}
	return parsed, nil
}

func requestURL(serverURL string, requestNumber int) (string, error) {
	if _, err := ValidateSABRURL(serverURL); err != nil {
		return "", err
	}
	separator := "&"
	if !strings.Contains(serverURL, "?") {
		separator = "?"
	}
	return serverURL + separator + "rn=" + strconv.Itoa(requestNumber), nil
}

func validateResponseContentType(header string) error {
	if header == "" {
		return fmt.Errorf("%w: missing content type", ErrInvalidContentType)
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return fmt.Errorf("%w: malformed content type", ErrInvalidContentType)
	}
	if mediaType != "application/vnd.yt-ump" {
		return fmt.Errorf("%w: expected application/vnd.yt-ump", ErrInvalidContentType)
	}
	return nil
}

func newSABRRequest(ctx context.Context, serverURL string, requestNumber int, body []byte, userAgent, acceptLanguage string) (*http.Request, error) {
	target, err := requestURL(serverURL, requestNumber)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("Accept", "application/vnd.yt-ump")
	request.Header.Set("Accept-Encoding", "identity")
	if userAgent != "" {
		request.Header.Set("User-Agent", userAgent)
	}
	if acceptLanguage != "" {
		request.Header.Set("Accept-Language", acceptLanguage)
	}
	request.ContentLength = int64(len(body))
	return request, nil
}
