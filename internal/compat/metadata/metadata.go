// Package metadata implements the bounded, Python-free subset of yt-dlp's
// MetadataParser postprocessor used by --parse-metadata and
// --replace-in-metadata. Regex compilation is deliberately isolated here: it
// uses Go's linear-time RE2 engine and rejects Python-only syntax until the
// shared compatibility adapter owns it.
package metadata

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxActionBytes      = 8192
	maxRenderedInput    = 256 << 10
	maxReplacementBytes = 256 << 10
	maxActions          = 128
)

var (
	ErrInvalidAction    = errors.New("invalid metadata action")
	ErrUnsupportedStage = errors.New("unsupported metadata lifecycle stage")
	ErrUnsupportedRegex = errors.New("unsupported metadata regex")
)

// SyntaxError carries a stable, source-local byte span for user diagnostics.
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

// Stage is the accepted yt-dlp MetadataParser lifecycle key. The product owns
// pre_process today; other valid upstream keys are rejected explicitly instead
// of silently running at an unsafe time.
type Stage string

const StagePreProcess Stage = "pre_process"

type Action struct {
	Kind                                 Kind
	Stage                                Stage
	From, To, Field, Search, Replacement string
	expression                           *regexp.Regexp
	captures                             []string // capture name by regexp subexpression index
}

type Result struct{ Changed, Warnings []string }

// ParseFromField parses [WHEN:]FROM:TO. The separator is the first colon not
// immediately escaped with a backslash, matching MetadataFromFieldPP.to_action.
func ParseFromField(input string) (Action, error) {
	if len(input) == 0 || len(input) > maxActionBytes {
		return Action{}, syntax(0, len(input), "empty or oversized parse action")
	}
	stage, body, err := splitStage(input)
	if err != nil {
		return Action{}, err
	}
	separator := firstUnescapedColon(body)
	if separator < 0 || separator == len(body)-1 {
		return Action{}, syntax(0, len(input), "expected FROM:TO")
	}
	from, to := strings.ReplaceAll(body[:separator], `\:`, `:`), body[separator+1:]
	templateInput := fieldToTemplate(from)
	if err := template.Validate(templateInput); err != nil {
		return Action{}, fmt.Errorf("%w: input template: %v", ErrInvalidAction, err)
	}
	re, captures, err := compileFormat(to)
	if err != nil {
		return Action{}, fmt.Errorf("%w: output pattern: %v", ErrInvalidAction, err)
	}
	return Action{Kind: Interpret, Stage: stage, From: from, To: to, expression: re, captures: captures}, nil
}

// ParseReplace parses the legacy FIELD:REGEX:REPLACEMENT programmatic form.
// The CLI uses ParseReplaceFields because upstream takes three shell arguments.
func ParseReplace(input string) (Action, error) {
	parts := splitEscaped(input, ':')
	if len(parts) != 3 {
		return Action{}, syntax(0, len(input), "expected FIELD:REGEX:REPLACEMENT")
	}
	actions, err := ParseReplaceFields(parts[0], parts[1], parts[2])
	if err != nil {
		return Action{}, err
	}
	if len(actions) != 1 {
		return Action{}, fmt.Errorf("%w: legacy replacement accepts one field", ErrInvalidAction)
	}
	return actions[0], nil
}

// ParseReplaceFields parses [WHEN:]FIELDS REGEX REPLACEMENT. FIELDS may be a
// comma-separated list; it expands to ordered independent replacements.
func ParseReplaceFields(fields, search, replacement string) ([]Action, error) {
	if len(fields) == 0 || len(fields) > maxActionBytes || len(search) > 2048 || len(replacement) > maxActionBytes {
		return nil, fmt.Errorf("%w: invalid replacement arguments", ErrInvalidAction)
	}
	stage, fields, err := splitStage(fields)
	if err != nil {
		return nil, err
	}
	re, err := compileRegex(search)
	if err != nil {
		return nil, err
	}
	goReplacement, err := translateReplacement(replacement)
	if err != nil {
		return nil, err
	}
	var actions []Action
	for _, field := range strings.Split(fields, ",") {
		if field == "" || len(field) > maxActionBytes {
			return nil, fmt.Errorf("%w: invalid metadata field %q", ErrInvalidAction, field)
		}
		actions = append(actions, Action{Kind: Replace, Stage: stage, Field: field, Search: search, Replacement: goReplacement, expression: re})
	}
	return actions, nil
}

// Apply is retained for callers without cancellation requirements.
func Apply(info *value.Info, actions []Action) (Result, error) {
	return ApplyContext(context.Background(), info, actions)
}

// ApplyContext performs actions in order. It mutates only the supplied ordered
// Info envelope; values written by one action are immediately visible to the
// next action, matching MetadataParserPP.
func ApplyContext(ctx context.Context, info *value.Info, actions []Action) (Result, error) {
	if info == nil || len(actions) > maxActions {
		return Result{}, fmt.Errorf("%w: nil info or too many actions", ErrInvalidAction)
	}
	result := Result{}
	for _, action := range actions {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if action.Stage != "" && action.Stage != StagePreProcess {
			return result, fmt.Errorf("%w: %q", ErrUnsupportedStage, action.Stage)
		}
		switch action.Kind {
		case Interpret:
			if action.expression == nil || len(action.From) > maxActionBytes || len(action.To) > maxActionBytes {
				return result, fmt.Errorf("%w: invalid interpret action", ErrInvalidAction)
			}
			rendered, err := template.Render(fieldToTemplate(action.From), *info)
			if err != nil {
				return result, fmt.Errorf("render metadata input: %w", err)
			}
			if len(rendered) > maxRenderedInput {
				return result, fmt.Errorf("%w: rendered metadata input exceeds %d bytes", ErrInvalidAction, maxRenderedInput)
			}
			match := action.expression.FindStringSubmatchIndex(rendered)
			if match == nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("could not interpret %q as %q", action.From, action.To))
				continue
			}
			for index, field := range action.captures {
				if index == 0 || field == "" || index*2+1 >= len(match) || match[index*2] < 0 {
					continue
				}
				info.Set(field, value.String(rendered[match[index*2]:match[index*2+1]]))
				result.Changed = append(result.Changed, field)
			}
		case Replace:
			if action.expression == nil || action.Field == "" || len(action.Search) > maxActionBytes || len(action.Replacement) > maxActionBytes {
				return result, fmt.Errorf("%w: invalid replace action", ErrInvalidAction)
			}
			current := info.Lookup(action.Field)
			if current.IsMissing() || current.IsNull() {
				result.Warnings = append(result.Warnings, fmt.Sprintf("video does not have a %s", action.Field))
				continue
			}
			text, ok := current.StringValue()
			if !ok {
				result.Warnings = append(result.Warnings, fmt.Sprintf("cannot replace in field %q since it is a %s", action.Field, current.Kind()))
				continue
			}
			if len(text) > maxRenderedInput {
				return result, fmt.Errorf("%w: replacement source exceeds %d bytes", ErrInvalidAction, maxRenderedInput)
			}
			replaced, err := boundedReplace(action.expression, text, action.Replacement)
			if err != nil {
				return result, err
			}
			if replaced != text {
				info.Set(action.Field, value.String(replaced))
				result.Changed = append(result.Changed, action.Field)
			}
		default:
			return result, fmt.Errorf("%w: unknown action", ErrInvalidAction)
		}
	}
	return result, nil
}

func firstUnescapedColon(input string) int {
	for i := range input {
		if input[i] == ':' && (i == 0 || input[i-1] != '\\') {
			return i
		}
	}
	return -1
}
func splitStage(input string) (Stage, string, error) {
	for _, stage := range []Stage{"pre_process", "after_filter", "video", "before_dl", "post_process", "after_move", "after_video", "playlist"} {
		prefix := string(stage) + ":"
		if strings.HasPrefix(input, prefix) {
			if stage != StagePreProcess {
				return "", "", fmt.Errorf("%w: %s", ErrUnsupportedStage, stage)
			}
			return stage, strings.TrimPrefix(input, prefix), nil
		}
	}
	return StagePreProcess, input, nil
}
func fieldToTemplate(input string) string {
	if asciiField(input) {
		return "%(" + input + ")s"
	}
	return input
}
func asciiField(input string) bool {
	if input == "" {
		return false
	}
	for _, r := range input {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}
func isCaptureField(input string) bool {
	if input == "" {
		return false
	}
	for _, r := range input {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func compileFormat(format string) (*regexp.Regexp, []string, error) {
	if len(format) == 0 || len(format) > maxActionBytes {
		return nil, nil, errors.New("empty or oversized output pattern")
	}
	if isCaptureField(format) {
		re, err := compileRegex("(?s)(.+)")
		return re, []string{"", format}, err
	}
	var pattern strings.Builder
	pattern.WriteString("(?s)")
	captures := []string{""}
	cursor, found := 0, false
	for cursor < len(format) {
		start := strings.Index(format[cursor:], "%(")
		if start < 0 {
			break
		}
		start += cursor
		endOffset := strings.Index(format[start+2:], ")s")
		if endOffset < 0 {
			break
		}
		end := start + 2 + endOffset
		field := format[start+2 : end]
		if !isCaptureField(field) {
			cursor = start + 2
			continue
		}
		pattern.WriteString(regexp.QuoteMeta(format[cursor:start]))
		pattern.WriteString("(.+)")
		captures = append(captures, field)
		cursor = end + 2
		found = true
	}
	if !found {
		re, err := compileRegex(format)
		if err != nil {
			return nil, nil, err
		}
		return re, re.SubexpNames(), nil
	}
	pattern.WriteString(regexp.QuoteMeta(format[cursor:]))
	re, err := compileRegex(pattern.String())
	return re, captures, err
}

// compileRegex is the metadata-only adapter boundary. It must be replaced by
// the shared bounded Python-regex adapter when that Track 1 public API lands.
func compileRegex(pattern string) (*regexp.Regexp, error) {
	if strings.Contains(pattern, "(?=") || strings.Contains(pattern, "(?!") || strings.Contains(pattern, "(?<=") || strings.Contains(pattern, "(?<!") || regexp.MustCompile(`\\[1-9]`).MatchString(pattern) {
		return nil, fmt.Errorf("%w: Python-only construct in %q", ErrUnsupportedRegex, pattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedRegex, err)
	}
	return re, nil
}

// translateReplacement converts the normal Python re.sub replacement grammar
// needed by MetadataParserPP into regexp.ExpandString's syntax.
func translateReplacement(replacement string) (string, error) {
	var output strings.Builder
	for i := 0; i < len(replacement); {
		if replacement[i] == '$' {
			output.WriteString("$$")
			i++
			continue
		}
		if replacement[i] != '\\' {
			output.WriteByte(replacement[i])
			i++
			continue
		}
		if i+1 == len(replacement) {
			return "", fmt.Errorf("%w: trailing backslash in replacement", ErrInvalidAction)
		}
		next := replacement[i+1]
		switch {
		case next >= '0' && next <= '9':
			output.WriteString("${")
			output.WriteByte(next)
			output.WriteByte('}')
			i += 2
		case next == 'g' && i+3 < len(replacement) && replacement[i+2] == '<':
			end := strings.IndexByte(replacement[i+3:], '>')
			if end < 0 {
				return "", fmt.Errorf("%w: unclosed replacement group", ErrInvalidAction)
			}
			name := replacement[i+3 : i+3+end]
			if name == "" {
				return "", fmt.Errorf("%w: empty replacement group", ErrInvalidAction)
			}
			output.WriteString("${" + name + "}")
			i += 4 + end
		case next == 'n':
			output.WriteByte('\n')
			i += 2
		case next == 'r':
			output.WriteByte('\r')
			i += 2
		case next == 't':
			output.WriteByte('\t')
			i += 2
		case next == '\\':
			output.WriteByte('\\')
			i += 2
		default:
			return "", fmt.Errorf("%w: unsupported replacement escape \\%c", ErrUnsupportedRegex, next)
		}
	}
	return output.String(), nil
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
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
