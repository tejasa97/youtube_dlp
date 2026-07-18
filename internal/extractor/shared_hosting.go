package extractor

// Shared, deliberately small helpers for hosted-video backends.  They keep
// error classification and format construction consistent without turning the
// independent public APIs into one opaque "generic hosting" extractor.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const sharedHostingMaxURLBytes = 8 << 10

// hostingNumber accepts provider APIs that inconsistently serialise scalar
// numbers as JSON strings. It remains bounded by the surrounding JSON reader.
type hostingNumber string

func (number *hostingNumber) UnmarshalJSON(data []byte) error {
	var stringValue string
	if err := json.Unmarshal(data, &stringValue); err == nil {
		*number = hostingNumber(stringValue)
		return nil
	}
	var numeric json.Number
	if err := json.Unmarshal(data, &numeric); err != nil {
		return err
	}
	*number = hostingNumber(numeric.String())
	return nil
}

func (number hostingNumber) string() string { return string(number) }
func (number hostingNumber) int64() int64 {
	parsed, _ := strconv.ParseInt(string(number), 10, 64)
	return parsed
}
func (number hostingNumber) float64() float64 {
	parsed, _ := strconv.ParseFloat(string(number), 64)
	return parsed
}

func validHostedHTTPURL(rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > sharedHostingMaxURLBytes {
		return false
	}
	parsed, err := url.Parse(rawURL)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil
}

func hostedURLFormat(formatID, rawURL string) (*value.Object, bool) {
	if !validHostedHTTPURL(rawURL) {
		return nil, false
	}
	parsed, _ := url.Parse(rawURL)
	extension := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), "."))
	switch extension {
	case "m3u8":
		return manifestFormat(formatID, rawURL, "m3u8_native"), true
	case "mpd":
		return manifestFormat(formatID, rawURL, "http_dash_segments"), true
	case "":
		extension = "mp4"
	}
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String(formatID)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "protocol", Value: value.String(parsed.Scheme)},
	), true
}

func hostedRequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if transport == nil || target == nil {
		return errors.New("invalid hosted JSON request")
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid hosted JSON request")
	}
	request.Header = headers.Clone()
	response, err := transport.Do(ctx, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	reader := &io.LimitedReader{R: response.Body, N: maxExtractorJSONBytes + 1}
	data, err := io.ReadAll(reader)
	if err != nil {
		return errors.New("read hosted JSON response failed")
	}
	if int64(len(data)) > maxExtractorJSONBytes {
		return ErrJSONResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return hostedStatusError(response.StatusCode, data)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid hosted JSON response", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing hosted JSON response", ErrInvalidMetadata)
	}
	return nil
}

func hostedStatusError(status int, responseBody []byte) error {
	switch status {
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden:
		// Inspect only an unreturned, bounded body for generic provider error
		// codes. Response text, tokens, and signatures never reach an error.
		if bytes.Contains(bytes.ToLower(responseBody), []byte("geo")) {
			return ErrRegionRestricted
		}
		return ErrAuthentication
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	case http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	default:
		return fmt.Errorf("hosted media API: HTTP status %d", status)
	}
}

func hostedSetString(object *value.Object, key, input string) {
	if strings.TrimSpace(input) != "" {
		object.Set(key, value.String(input))
	}
}

func hostedSetInt(object *value.Object, key string, input int64) {
	if input > 0 {
		object.Set(key, value.Int(input))
	}
}

func hostedSetFloat(object *value.Object, key string, input float64) {
	if input > 0 {
		object.Set(key, value.Float(input))
	}
}

func hostedUnixTimestamp(input string) int64 {
	if input == "" {
		return 0
	}
	if number, err := strconv.ParseInt(input, 10, 64); err == nil && number > 0 {
		return number
	}
	parsed, err := time.Parse(time.RFC3339, input)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

func hostedHeadersValue(headers http.Header) value.Value {
	filtered := make(http.Header)
	for key, entries := range headers {
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Cookie") || strings.EqualFold(key, "Proxy-Authorization") {
			continue
		}
		filtered[key] = append([]string(nil), entries...)
	}
	return value.ObjectValue(headersValue(filtered))
}
