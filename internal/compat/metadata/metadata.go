// Package metadata provides bounded, declarative parse-metadata and
// replace-in-metadata operations. It is a Go-native equivalent of the common
// MetadataParser postprocessor actions, intended for product integration.
package metadata

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxActionBytes      = 8192
	maxRenderedInput    = 256 << 10
	maxReplacementBytes = 256 << 10
)

var ErrInvalidAction = errors.New("invalid metadata action")

type SyntaxError struct {
	Start, End int
	Message    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%v at bytes %d:%d: %s", ErrInvalidAction, e.Start, e.End, e.Message)
}
func (e *SyntaxError) Unwrap() error { return ErrInvalidAction }

type Kind uint8

const (
	Interpret Kind = iota + 1
	Replace
)

type Action struct {
	Kind                                 Kind
	From, To, Field, Search, Replacement string
	expression                           *regexp.Regexp
}
type Result struct {
	Changed  []string
	Warnings []string
}

// ParseFromField parses FROM:TO with a backslash-escaped colon, as accepted by
// --parse-metadata. FROM may be a metadata key or an output template.
func ParseFromField(input string) (Action, error) {
	if len(input) == 0 || len(input) > maxActionBytes {
		return Action{}, fmt.Errorf("%w: empty or oversized parse action", ErrInvalidAction)
	}
	separator := -1
	escaped := false
	for i := range input {
		if escaped {
			escaped = false
			continue
		}
		if input[i] == '\\' {
			escaped = true
			continue
		}
		if input[i] == ':' {
			separator = i
		}
	}
	if separator <= 0 || separator == len(input)-1 {
		return Action{}, syntax(0, len(input), "expected FROM:TO")
	}
	from, to := strings.ReplaceAll(input[:separator], `\:`, `:`), input[separator+1:]
	re, err := formatToRegexp(to)
	if err != nil {
		return Action{}, fmt.Errorf("%w: output pattern: %v", ErrInvalidAction, err)
	}
	return Action{Kind: Interpret, From: from, To: to, expression: re}, nil
}

// ParseReplace parses FIELD:REGEX:REPLACEMENT. Separators may be escaped.
func ParseReplace(input string) (Action, error) {
	parts := splitEscaped(input, ':')
	if len(input) == 0 || len(input) > maxActionBytes || len(parts) != 3 || !fieldName.MatchString(parts[0]) {
		return Action{}, fmt.Errorf("%w: expected FIELD:REGEX:REPLACEMENT", ErrInvalidAction)
	}
	if len(parts[1]) > 2048 {
		return Action{}, fmt.Errorf("%w: replacement regular expression too large", ErrInvalidAction)
	}
	re, err := regexp.Compile(parts[1])
	if err != nil {
		return Action{}, fmt.Errorf("%w: invalid replacement regular expression: %v", ErrInvalidAction, err)
	}
	return Action{Kind: Replace, Field: parts[0], Search: parts[1], Replacement: parts[2], expression: re}, nil
}

// Apply performs actions in order. A nonmatching interpretation or a missing
// string replacement source becomes a warning, not a fatal extraction error.
func Apply(info *value.Info, actions []Action) (Result, error) {
	if info == nil || len(actions) > 128 {
		return Result{}, fmt.Errorf("%w: nil info or too many actions", ErrInvalidAction)
	}
	result := Result{}
	for _, action := range actions {
		switch action.Kind {
		case Interpret:
			if action.expression == nil || len(action.From) > maxActionBytes || len(action.To) > maxActionBytes {
				return result, fmt.Errorf("%w: invalid interpret action", ErrInvalidAction)
			}
			input := action.From
			if fieldName.MatchString(input) {
				input = "%(" + input + ")s"
			}
			rendered, err := template.Render(input, *info)
			if err != nil {
				return result, fmt.Errorf("render metadata input: %w", err)
			}
			if len(rendered) > maxRenderedInput {
				return result, fmt.Errorf("%w: rendered metadata input exceeds %d bytes", ErrInvalidAction, maxRenderedInput)
			}
			match := action.expression.FindStringSubmatch(rendered)
			if match == nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("could not interpret %q as %q", action.From, action.To))
				continue
			}
			for index, field := range action.expression.SubexpNames() {
				if index != 0 && field != "" && match[index] != "" {
					info.Set(field, value.String(match[index]))
					result.Changed = append(result.Changed, field)
				}
			}
		case Replace:
			if action.expression == nil || !fieldName.MatchString(action.Field) || len(action.Search) > maxActionBytes || len(action.Replacement) > maxActionBytes {
				return result, fmt.Errorf("%w: invalid replace action", ErrInvalidAction)
			}
			current, ok := info.Lookup(action.Field).StringValue()
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("field %q is not a string", action.Field))
				continue
			}
			if len(current) > maxRenderedInput {
				return result, fmt.Errorf("%w: replacement source exceeds %d bytes", ErrInvalidAction, maxRenderedInput)
			}
			replaced, err := boundedReplace(action.expression, current, action.Replacement)
			if err != nil {
				return result, err
			}
			if replaced != current {
				info.Set(action.Field, value.String(replaced))
				result.Changed = append(result.Changed, action.Field)
			}
		default:
			return result, fmt.Errorf("%w: unknown action", ErrInvalidAction)
		}
	}
	return result, nil
}

func boundedReplace(expression *regexp.Regexp, source, replacement string) (string, error) {
	matches := expression.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 {
		return source, nil
	}
	var output strings.Builder
	output.Grow(min(len(source), maxReplacementBytes))
	cursor := 0
	for _, match := range matches {
		if match[0] < cursor {
			continue
		}
		if output.Len()+match[0]-cursor > maxReplacementBytes {
			return "", fmt.Errorf("%w: replacement output exceeds %d bytes", ErrInvalidAction, maxReplacementBytes)
		}
		output.WriteString(source[cursor:match[0]])
		expanded := expression.ExpandString(nil, replacement, source, match)
		if len(expanded) > maxReplacementBytes-output.Len() {
			return "", fmt.Errorf("%w: replacement output exceeds %d bytes", ErrInvalidAction, maxReplacementBytes)
		}
		output.Write(expanded)
		cursor = match[1]
	}
	if len(source)-cursor > maxReplacementBytes-output.Len() {
		return "", fmt.Errorf("%w: replacement output exceeds %d bytes", ErrInvalidAction, maxReplacementBytes)
	}
	output.WriteString(source[cursor:])
	return output.String(), nil
}

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var placeholder = regexp.MustCompile(`%\(([A-Za-z_][A-Za-z0-9_]*)\)s`)

func formatToRegexp(format string) (*regexp.Regexp, error) {
	if len(format) == 0 || len(format) > maxActionBytes {
		return nil, errors.New("empty or oversized output pattern")
	}
	if fieldName.MatchString(format) {
		return regexp.Compile(`(?s)^(?P<` + format + `>.+)$`)
	}
	matches := placeholder.FindAllStringSubmatchIndex(format, -1)
	if len(matches) == 0 {
		return regexp.Compile(format)
	}
	var b strings.Builder
	b.WriteString("(?s)")
	cursor := 0
	for _, match := range matches {
		b.WriteString(regexp.QuoteMeta(format[cursor:match[0]]))
		b.WriteString("(?P<")
		b.WriteString(format[match[2]:match[3]])
		b.WriteString(">.+?)")
		cursor = match[1]
	}
	b.WriteString(regexp.QuoteMeta(format[cursor:]))
	return regexp.Compile(b.String())
}
func splitEscaped(input string, separator byte) []string {
	var result []string
	var b strings.Builder
	escaped := false
	for i := range input {
		if escaped {
			b.WriteByte(input[i])
			escaped = false
			continue
		}
		if input[i] == '\\' {
			escaped = true
			continue
		}
		if input[i] == separator {
			result = append(result, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(input[i])
	}
	if escaped {
		b.WriteByte('\\')
	}
	return append(result, b.String())
}
func syntax(start, end int, message string) error {
	return &SyntaxError{Start: start, End: end, Message: message}
}
