package engine

import (
	"errors"
	"strings"
	"unicode"
)

var errInvalidMergeOutputFormat = errors.New("invalid merge output format")

// MergeOutputFormatSupported lists containers accepted by pinned FFmpegMergerPP.
var MergeOutputFormatSupported = []string{"avi", "flv", "mkv", "mov", "mp4", "webm"}

const (
	maxMergeOutputFormatBytes   = 64
	maxMergeOutputFormatEntries = 16
)

var mergeOutputFormatAllowed = func() map[string]struct{} {
	allowed := make(map[string]struct{}, len(MergeOutputFormatSupported))
	for _, extension := range MergeOutputFormatSupported {
		allowed[extension] = struct{}{}
	}
	return allowed
}()

// ParseMergeOutputFormat validates Request.MergeOutputFormat and returns the
// ordered preference list. An empty input means no explicit preference.
func ParseMergeOutputFormat(explicit string) ([]string, error) {
	if explicit == "" {
		return nil, nil
	}
	if len(explicit) > maxMergeOutputFormatBytes {
		return nil, errInvalidMergeOutputFormat
	}
	if strings.ContainsAny(explicit, "\x00\r\n") {
		return nil, errInvalidMergeOutputFormat
	}
	for _, character := range explicit {
		if character < 0x20 || character == 0x7f {
			return nil, errInvalidMergeOutputFormat
		}
	}
	parts := strings.Split(explicit, "/")
	if len(parts) == 0 || len(parts) > maxMergeOutputFormatEntries {
		return nil, errInvalidMergeOutputFormat
	}
	preferences := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || strings.TrimSpace(part) != part || containsUnicodeSpace(part) {
			return nil, errInvalidMergeOutputFormat
		}
		if _, ok := mergeOutputFormatAllowed[part]; !ok {
			return nil, errInvalidMergeOutputFormat
		}
		preferences = append(preferences, part)
	}
	return preferences, nil
}

func mergeOutputFormatPreferences(explicit string) []string {
	preferences, err := ParseMergeOutputFormat(explicit)
	if err != nil {
		return nil
	}
	return preferences
}

func containsUnicodeSpace(value string) bool {
	for _, character := range value {
		if unicode.IsSpace(character) {
			return true
		}
	}
	return false
}
