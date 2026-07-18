package extractor

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

const riskExtractorMaxJSONBytes int64 = 16 << 20

// requestRiskJSON is the bounded JSON boundary shared by the Phase 2 risk
// extractors. In particular, it never includes request or response bodies in
// diagnostics because both commonly contain bearer tokens or signed URLs.
func requestRiskJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, profile string, target any) error {
	if transport == nil || target == nil {
		return fmt.Errorf("%w: invalid JSON request", ErrInvalidMetadata)
	}
	request, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: invalid JSON request", ErrInvalidMetadata)
	}
	request.Header = headers.Clone()
	response, err := DoWithProfile(ctx, transport, request, profile)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPStatusError{Code: response.StatusCode}
	}
	reader := &io.LimitedReader{R: response.Body, N: riskExtractorMaxJSONBytes + 1}
	data, err := io.ReadAll(reader)
	if err != nil {
		return errors.New("read extractor JSON response failed")
	}
	if int64(len(data)) > riskExtractorMaxJSONBytes {
		return ErrJSONResponseTooLarge
	}
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

func riskHTTPStatus(err error) int {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return status.Code
	}
	return 0
}

func riskFormat(rawURL, formatID string) (*value.Object, bool) {
	if !validHTTPURL(rawURL) {
		return nil, false
	}
	extension := strings.ToLower(strings.TrimPrefix(path.Ext(mustURLPath(rawURL)), "."))
	if formatID == "" {
		formatID = "http"
	}
	switch extension {
	case "m3u8":
		return manifestFormat(formatID, rawURL, "m3u8_native"), true
	case "mpd":
		return manifestFormat(formatID, rawURL, "http_dash_segments"), true
	}
	if extension == "" {
		extension = "mp4"
	}
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String(formatID)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "protocol", Value: value.String(strings.ToLower(mustURLScheme(rawURL)))},
	), true
}

func riskString(object *value.Object, key, text string) {
	if text != "" {
		object.Set(key, value.String(text))
	}
}

func riskInt(object *value.Object, key string, number int64) {
	if number >= 0 {
		object.Set(key, value.Int(number))
	}
}

func riskPositiveInt(object *value.Object, key string, number int64) {
	if number > 0 {
		object.Set(key, value.Int(number))
	}
}

func riskFloat(object *value.Object, key string, number float64) {
	if number > 0 {
		object.Set(key, value.Float(number))
	}
}

func riskTimestamp(text string) int64 {
	if text == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z0700"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func riskFlexibleInt(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number int64
	if json.Unmarshal(raw, &number) == nil {
		return number
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		number, _ = strconv.ParseInt(text, 10, 64)
	}
	return number
}

func riskAbsoluteURL(baseURL, reference string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(reference)
	if err != nil {
		return ""
	}
	result := base.ResolveReference(parsed).String()
	if !validHTTPURL(result) {
		return ""
	}
	return result
}
