// Package template implements the Phase 0 output-template compatibility subset.
// Package template implements the output-template compatibility layers.
package template

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrInvalidTemplate = errors.New("invalid output template")
	ErrUnsafePath      = errors.New("output path escapes its root")
)

var formatSpecPattern = regexp.MustCompile(`^[-+0 #]*[0-9]*(\.[0-9]+)?[sdf]$`)

// Render supports literal text, %%, traversal/alternative/default expressions,
// replacement templates, date conversion, and bounded scalar format specs.
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
		specEnd := closeIndex + 1
		for specEnd < len(pattern) && !strings.ContainsRune("sdf", rune(pattern[specEnd])) {
			specEnd++
		}
		if specEnd >= len(pattern) {
			return "", fmt.Errorf("%w at byte %d: missing conversion type", ErrInvalidTemplate, index)
		}
		spec := pattern[closeIndex+1 : specEnd+1]
		if !formatSpecPattern.MatchString(spec) {
			return "", fmt.Errorf("%w at byte %d: invalid format spec %q", ErrInvalidTemplate, closeIndex+1, spec)
		}
		expression := pattern[index+2 : closeIndex]
		rendered, err := renderExpression(expression, spec, info)
		if err != nil {
			return "", fmt.Errorf("%w at byte %d: expression %q: %v", ErrInvalidTemplate, index, expression, err)
		}
		output.WriteString(rendered)
		index = specEnd + 1
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

func renderExpression(expression, spec string, info value.Info) (string, error) {
	if expression == "" {
		return "", errors.New("empty expression")
	}
	source, defaultValue, hasDefault := strings.Cut(expression, "|")
	source, replacement, hasReplacement := strings.Cut(source, "&")
	var selected value.Value
	var dateFormat string
	for _, alternative := range strings.Split(source, ",") {
		path, format, hasDateFormat := strings.Cut(strings.TrimSpace(alternative), ">")
		candidate, err := traverse(info, path)
		if err != nil {
			return "", err
		}
		if candidate.IsMissing() || candidate.IsNull() {
			continue
		}
		selected = candidate
		if hasDateFormat {
			dateFormat = format
		}
		break
	}
	if selected.IsMissing() || selected.IsNull() {
		if hasDefault {
			selected = value.String(defaultValue)
		} else {
			selected = value.String("NA")
		}
	}
	if dateFormat != "" {
		converted, err := renderDate(selected, dateFormat)
		if err != nil {
			return "", err
		}
		selected = value.String(converted)
	}
	if hasReplacement {
		if !strings.Contains(replacement, "{}") {
			return "", errors.New("replacement must contain {}")
		}
		raw, err := scalarString(selected)
		if err != nil {
			return "", err
		}
		selected = value.String(strings.ReplaceAll(replacement, "{}", raw))
	}
	return formatValue(selected, spec)
}

func traverse(info value.Info, path string) (value.Value, error) {
	parts := strings.Split(path, ".")
	if len(parts) == 0 || parts[0] == "" {
		return value.Missing(), errors.New("empty traversal path")
	}
	current := info.Lookup(parts[0])
	for _, part := range parts[1:] {
		switch current.Kind() {
		case value.KindObject:
			object, _ := current.Object()
			current = object.Lookup(part)
		case value.KindList:
			items, _ := current.ListValue()
			index, err := strconv.Atoi(part)
			if err != nil {
				return value.Missing(), fmt.Errorf("list index %q is not an integer", part)
			}
			if index < 0 {
				index += len(items)
			}
			if index < 0 || index >= len(items) {
				return value.Missing(), nil
			}
			current = items[index]
		case value.KindMissing, value.KindNull:
			return value.Missing(), nil
		default:
			return value.Missing(), fmt.Errorf("cannot traverse %q through %s", part, current.Kind())
		}
	}
	return current, nil
}

func formatValue(input value.Value, spec string) (string, error) {
	conversion := spec[len(spec)-1]
	format := "%" + spec
	switch conversion {
	case 's':
		raw, err := scalarString(input)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(format, raw), nil
	case 'd':
		integer, ok := input.Int()
		if !ok {
			if floating, floatOK := input.Float(); floatOK {
				integer, ok = int64(floating), true
			}
		}
		if !ok {
			return "", fmt.Errorf("kind %s is not numeric", input.Kind())
		}
		return fmt.Sprintf(format, integer), nil
	case 'f':
		floating, ok := input.Float()
		if !ok {
			if integer, intOK := input.Int(); intOK {
				floating, ok = float64(integer), true
			}
		}
		if !ok {
			return "", fmt.Errorf("kind %s is not numeric", input.Kind())
		}
		return fmt.Sprintf(format, floating), nil
	default:
		return "", fmt.Errorf("unsupported conversion %q", conversion)
	}
}

func scalarString(input value.Value) (string, error) {
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

func renderDate(input value.Value, format string) (string, error) {
	raw, ok := input.StringValue()
	if !ok {
		return "", fmt.Errorf("date value has kind %s", input.Kind())
	}
	var parsed time.Time
	var err error
	for _, layout := range []string{"20060102", "20060102150405", time.RFC3339} {
		parsed, err = time.Parse(layout, raw)
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("unsupported date %q", raw)
	}
	const escapedPercent = "\x00"
	escaped := strings.ReplaceAll(format, "%%", escapedPercent)
	replacer := strings.NewReplacer(
		"%Y", "2006", "%m", "01", "%d", "02", "%H", "15", "%M", "04", "%S", "05",
	)
	converted := replacer.Replace(escaped)
	if strings.Contains(converted, "%") {
		return "", fmt.Errorf("unsupported date format %q", format)
	}
	converted = strings.ReplaceAll(converted, escapedPercent, "%")
	return parsed.Format(converted), nil
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
