// Package template implements the Phase 0 output-template compatibility subset.
package template

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrInvalidTemplate = errors.New("invalid output template")
	ErrUnsafePath      = errors.New("output path escapes its root")
)

// Render supports literal text, %%, and simple %(field)s substitutions.
func Render(pattern string, info value.Info) (string, error) {
	var output strings.Builder
	for index := 0; index < len(pattern); {
		if pattern[index] != '%' {
			output.WriteByte(pattern[index])
			index++
			continue
		}
		if index+1 < len(pattern) && pattern[index+1] == '%' {
			output.WriteByte('%')
			index += 2
			continue
		}
		if index+2 >= len(pattern) || pattern[index+1] != '(' {
			return "", fmt.Errorf("%w at byte %d: expected %% or %%(field)s", ErrInvalidTemplate, index)
		}
		closeOffset := strings.IndexByte(pattern[index+2:], ')')
		if closeOffset < 0 {
			return "", fmt.Errorf("%w at byte %d: unclosed field", ErrInvalidTemplate, index)
		}
		closeIndex := index + 2 + closeOffset
		if closeIndex+1 >= len(pattern) || pattern[closeIndex+1] != 's' {
			return "", fmt.Errorf("%w at byte %d: only string conversion is supported", ErrInvalidTemplate, index)
		}
		field := pattern[index+2 : closeIndex]
		if !validField(field) {
			return "", fmt.Errorf("%w at byte %d: invalid field %q", ErrInvalidTemplate, index, field)
		}
		rendered, err := renderValue(info.Lookup(field))
		if err != nil {
			return "", fmt.Errorf("%w: field %q: %v", ErrInvalidTemplate, field, err)
		}
		output.WriteString(rendered)
		index = closeIndex + 2
	}
	return output.String(), nil
}

// Resolve renders and sanitizes a relative template beneath outputRoot.
func Resolve(outputRoot, pattern string, info value.Info) (string, error) {
	rendered, err := Render(pattern, info)
	if err != nil {
		return "", err
	}
	normalized := strings.ReplaceAll(rendered, `\`, "/")
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(rendered) || filepath.VolumeName(rendered) != "" {
		return "", ErrUnsafePath
	}
	parts := strings.Split(normalized, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", ErrUnsafePath
		}
		cleaned = append(cleaned, sanitizeSegment(part))
	}
	if len(cleaned) == 0 {
		return "", fmt.Errorf("%w: template produced an empty path", ErrInvalidTemplate)
	}
	root, err := filepath.Abs(outputRoot)
	if err != nil {
		return "", fmt.Errorf("resolve output root: %w", err)
	}
	destination := filepath.Join(append([]string{root}, cleaned...)...)
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrUnsafePath
	}
	return destination, nil
}

func validField(field string) bool {
	if field == "" {
		return false
	}
	for _, character := range field {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' {
			return false
		}
	}
	return true
}

func renderValue(input value.Value) (string, error) {
	switch input.Kind() {
	case value.KindMissing, value.KindNull:
		return "NA", nil
	case value.KindString:
		result, _ := input.StringValue()
		return result, nil
	case value.KindInt:
		result, _ := input.Int()
		return strconv.FormatInt(result, 10), nil
	case value.KindFloat:
		result, _ := input.Float()
		return strconv.FormatFloat(result, 'g', -1, 64), nil
	case value.KindBool:
		result, _ := input.Bool()
		return strconv.FormatBool(result), nil
	default:
		return "", fmt.Errorf("kind %s cannot be rendered as a string", input.Kind())
	}
}

var windowsReserved = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {}, "COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {}, "LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

func sanitizeSegment(segment string) string {
	var output strings.Builder
	for _, character := range segment {
		if character < 32 || strings.ContainsRune(`<>:"|?*`, character) {
			output.WriteByte('_')
			continue
		}
		output.WriteRune(character)
	}
	result := strings.TrimRight(output.String(), " .")
	if result == "" {
		result = "_"
	}
	base := strings.ToUpper(strings.SplitN(result, ".", 2)[0])
	if _, reserved := windowsReserved[base]; reserved {
		result = "_" + result
	}
	return result
}
