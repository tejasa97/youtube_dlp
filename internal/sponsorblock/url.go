package sponsorblock

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strings"
)

const maxVideoIDBytes = 256

// hashPrefix returns the first four lowercase hex characters of
// SHA-256(videoID). The pinned reference recommends only four characters
// to spread load; the returned string is exactly four bytes long.
func hashPrefix(videoID string) (string, error) {
	if len(videoID) == 0 || len(videoID) > maxVideoIDBytes {
		return "", errorf(ErrInvalidInput, "empty video id")
	}
	for index := 0; index < len(videoID); index++ {
		if videoID[index] < 0x21 || videoID[index] > 0x7e {
			return "", errorf(ErrInvalidInput, "invalid video id")
		}
	}
	sum := sha256.Sum256([]byte(videoID))
	encoded := hex.EncodeToString(sum[:])
	return encoded[:4], nil
}

// buildEndpointURL composes the canonical SponsorBlock API URL for one
// request. The query is built with net/url so values are properly
// percent-encoded. The returned URL is always absolute.
func buildEndpointURL(apiBase, prefix string, categories []Category, actions []ActionType) (*url.URL, error) {
	if apiBase == "" {
		return nil, errorf(ErrInvalidInput, "empty api base")
	}
	parsed, err := url.Parse(apiBase)
	if err != nil {
		return nil, errorf(ErrInvalidInput, "invalid api base")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errorf(ErrInvalidInput, "unsupported api base scheme")
	}
	if parsed.Host == "" {
		return nil, errorf(ErrInvalidInput, "api base missing host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errorf(ErrInvalidInput, "api base contains credentials or suffix")
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return nil, errorf(ErrInvalidInput, "api base contains unsafe path")
	}
	categoryStrings := make([]string, len(categories))
	for i, category := range categories {
		categoryStrings[i] = string(category)
	}
	actionStrings := make([]string, len(actions))
	for i, action := range actions {
		actionStrings[i] = string(action)
	}
	categoriesJSON, err := encodeJSONArray(categoryStrings)
	if err != nil {
		return nil, err
	}
	actionsJSON, err := encodeJSONArray(actionStrings)
	if err != nil {
		return nil, err
	}
	parsed.Path = path.Join("/", parsed.Path, "api", "skipSegments", prefix)
	parsed.RawPath = ""
	query := make(url.Values)
	query.Set("service", "YouTube")
	query.Set("categories", categoriesJSON)
	query.Set("actionTypes", actionsJSON)
	parsed.RawQuery = query.Encode()
	return parsed, nil
}

// encodeJSONArray renders a string slice as a JSON array literal without
// depending on a third-party library. The output is consumed by the
// SponsorBlock API which expects a JSON-encoded value for the
// categories and actionTypes query parameters.
func encodeJSONArray(values []string) (string, error) {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		if err := appendJSONString(&builder, value); err != nil {
			return "", err
		}
	}
	builder.WriteByte(']')
	return builder.String(), nil
}

// appendJSONString writes value as a JSON string literal into builder.
// A minimal hand-rolled JSON string encoder keeps the package free of
// any third-party dependency and lets the response decoder stay
// symmetric.
func appendJSONString(builder *strings.Builder, value string) error {
	if err := builder.WriteByte('"'); err != nil {
		return err
	}
	for _, r := range value {
		switch r {
		case '"', '\\':
			if _, err := builder.WriteRune('\\'); err != nil {
				return err
			}
			if _, err := builder.WriteRune(r); err != nil {
				return err
			}
		case '\b':
			if _, err := builder.WriteString(`\b`); err != nil {
				return err
			}
		case '\f':
			if _, err := builder.WriteString(`\f`); err != nil {
				return err
			}
		case '\n':
			if _, err := builder.WriteString(`\n`); err != nil {
				return err
			}
		case '\r':
			if _, err := builder.WriteString(`\r`); err != nil {
				return err
			}
		case '\t':
			if _, err := builder.WriteString(`\t`); err != nil {
				return err
			}
		default:
			if r < 0x20 {
				if _, err := fmt.Fprintf(builder, `\u%04x`, r); err != nil {
					return err
				}
			} else {
				if _, err := builder.WriteRune(r); err != nil {
					return err
				}
			}
		}
	}
	if err := builder.WriteByte('"'); err != nil {
		return err
	}
	return nil
}
