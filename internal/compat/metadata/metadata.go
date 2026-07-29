// Package metadata implements the bounded, Python-free subset of yt-dlp's
// MetadataParser postprocessor used by --parse-metadata and
// --replace-in-metadata. Regex compilation is deliberately isolated here and
// delegates Python syntax translation to the shared compatibility adapter.
package metadata

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
	"github.com/ytdlp-go/ytdlp/internal/compat/pyregex"
	"github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxActionBytes       = 8192
	maxRenderedInput     = 256 << 10
	maxReplacementBytes  = 256 << 10
	maxActions           = 128
	maxRegexBytes        = 8192
	maxTranslatedBytes   = 16 << 10
	maxRegexInputBytes   = 64 << 10
	maxRegexAttempts     = 1024
	maxRegexBytesScanned = 4 << 20
	regexMatchTimeout    = 25 * time.Millisecond
	regexWallBudget      = 250 * time.Millisecond
)

var (
	ErrInvalidAction    = errors.New("invalid metadata action")
	ErrUnsupportedStage = errors.New("unsupported metadata lifecycle stage")
	ErrUnsupportedRegex = errors.New("unsupported metadata regex")
	errRegexTimeout     = errors.New("metadata regular expression timed out")
	errRegexBudget      = errors.New("metadata regular expression budget exhausted")
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
	expression                           *regexp2.Regexp
	captures                             []capture
}

type capture struct {
	number int
	field  string
}

type regexBudget struct {
	attempts, inspectedBytes int
	wall                     time.Duration
	started                  time.Time
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
	goReplacement, err := translateReplacement(replacement, search)
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
			match, err := findRegex(ctx, action.expression, rendered, newRegexBudget())
			if err != nil {
				return result, err
			}
			if match == nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("could not interpret %q as %q", action.From, action.To))
				continue
			}
			for _, target := range action.captures {
				group := match.GroupByNumber(target.number)
				if group == nil || len(group.Captures) == 0 {
					continue
				}
				info.Set(target.field, value.String(group.String()))
				result.Changed = append(result.Changed, target.field)
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
			replaced, err := boundedReplace(ctx, action.expression, text, action.Replacement)
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

func compileFormat(format string) (*regexp2.Regexp, []capture, error) {
	if len(format) == 0 || len(format) > maxActionBytes {
		return nil, nil, errors.New("empty or oversized output pattern")
	}
	if isCaptureField(format) {
		re, err := compileRegex("(?s)(.+)")
		return re, []capture{{number: 1, field: format}}, err
	}
	type placeholder struct {
		start, end int
		field      string
	}
	var placeholders []placeholder
	for cursor := 0; cursor < len(format); {
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
		placeholders = append(placeholders, placeholder{start: start, end: end + 2, field: field})
		cursor = end + 2
	}
	if len(placeholders) == 0 {
		re, err := compileRegex(format)
		if err != nil {
			return nil, nil, err
		}
		return re, namedCaptures(format), nil
	}
	var pattern strings.Builder
	pattern.WriteString("(?s)")
	captures := make([]capture, 0, len(placeholders))
	cursor := 0
	for index, item := range placeholders {
		pattern.WriteString(regexp.QuoteMeta(format[cursor:item.start]))
		if index+1 < len(placeholders) {
			pattern.WriteString("(.+?)")
		} else {
			pattern.WriteString("(.+)")
		}
		captures = append(captures, capture{number: index + 1, field: item.field})
		cursor = item.end
	}
	pattern.WriteString(regexp.QuoteMeta(format[cursor:]))
	re, err := compileRegex(pattern.String())
	return re, captures, err
}

// namedCaptures maps only Python named-group declarations to their ordinal
// capture numbers. It does not translate regex syntax; pyregex owns that.
func namedCaptures(pattern string) []capture {
	var captures []capture
	group, escaped, inClass := 0, false, false
	for index := 0; index < len(pattern); index++ {
		if escaped {
			escaped = false
			continue
		}
		if pattern[index] == '\\' {
			escaped = true
			continue
		}
		if pattern[index] == '[' {
			inClass = true
			continue
		}
		if pattern[index] == ']' {
			inClass = false
			continue
		}
		if inClass || pattern[index] != '(' {
			continue
		}
		if index+1 >= len(pattern) || pattern[index+1] != '?' {
			group++
			continue
		}
		if !strings.HasPrefix(pattern[index:], "(?P<") {
			continue
		}
		end := strings.IndexByte(pattern[index+4:], '>')
		if end < 0 {
			continue
		}
		group++
		captures = append(captures, capture{number: group, field: pattern[index+4 : index+4+end]})
	}
	return captures
}

func compileRegex(pattern string) (*regexp2.Regexp, error) {
	if len(pattern) == 0 || len(pattern) > maxRegexBytes {
		return nil, fmt.Errorf("%w: pattern exceeds size limit", ErrUnsupportedRegex)
	}
	if !utf8.ValidString(pattern) {
		return nil, fmt.Errorf("%w: pattern is not valid UTF-8", ErrUnsupportedRegex)
	}
	translated, err := pyregex.Translate(pattern)
	if err != nil || len(translated) > maxTranslatedBytes {
		return nil, fmt.Errorf("%w: invalid or oversized Python pattern", ErrUnsupportedRegex)
	}
	expression, err := regexp2.Compile(translated, regexp2.None)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Python pattern", ErrUnsupportedRegex)
	}
	expression.MatchTimeout = regexMatchTimeout
	return expression, nil
}

// translateReplacement converts the normal Python re.sub replacement grammar
// needed by MetadataParserPP into regexp.ExpandString's syntax.
func translateReplacement(replacement, pattern string) (string, error) {
	named := make(map[string]int)
	for _, target := range namedCaptures(pattern) {
		named[target.field] = target.number
	}
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
			if number, ok := named[name]; ok {
				output.WriteString("${" + strconv.Itoa(number) + "}")
			} else if _, err := strconv.Atoi(name); err == nil {
				output.WriteString("${" + name + "}")
			} else {
				return "", fmt.Errorf("%w: unknown replacement group", ErrInvalidAction)
			}
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

func newRegexBudget() *regexBudget { return &regexBudget{started: time.Now()} }

func chargeRegex(ctx context.Context, budget *regexBudget, input string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(input) > maxRegexInputBytes {
		return fmt.Errorf("%w: input exceeds size limit", ErrUnsupportedRegex)
	}
	if !utf8.ValidString(input) {
		return fmt.Errorf("%w: input is not valid UTF-8", ErrUnsupportedRegex)
	}
	if budget == nil {
		return nil
	}
	if budget.started.IsZero() {
		budget.started = time.Now()
	}
	budget.attempts++
	budget.inspectedBytes += len(input)
	if budget.attempts > maxRegexAttempts || budget.inspectedBytes > maxRegexBytesScanned || time.Since(budget.started) > regexWallBudget {
		return fmt.Errorf("%w: %w", ErrUnsupportedRegex, errRegexBudget)
	}
	return nil
}

func sanitizeRegexError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "timeout") || strings.Contains(message, "timed out") {
		return fmt.Errorf("%w: %w", ErrUnsupportedRegex, errRegexTimeout)
	}
	return fmt.Errorf("%w: %w", ErrUnsupportedRegex, errRegexBudget)
}

func findRegex(ctx context.Context, expression *regexp2.Regexp, input string, budget *regexBudget) (*regexp2.Match, error) {
	if expression == nil {
		return nil, fmt.Errorf("%w: missing expression", ErrUnsupportedRegex)
	}
	if err := chargeRegex(ctx, budget, input); err != nil {
		return nil, err
	}
	started := time.Now()
	match, err := expression.FindStringMatch(input)
	if budget != nil {
		budget.wall += time.Since(started)
		if budget.wall > regexWallBudget {
			return nil, fmt.Errorf("%w: %w", ErrUnsupportedRegex, errRegexBudget)
		}
	}
	if err != nil {
		return nil, sanitizeRegexError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return match, nil
}

func nextRegex(ctx context.Context, expression *regexp2.Regexp, previous *regexp2.Match, input string, budget *regexBudget) (*regexp2.Match, error) {
	if err := chargeRegex(ctx, budget, input); err != nil {
		return nil, err
	}
	started := time.Now()
	match, err := expression.FindNextMatch(previous)
	if budget != nil {
		budget.wall += time.Since(started)
		if budget.wall > regexWallBudget {
			return nil, fmt.Errorf("%w: %w", ErrUnsupportedRegex, errRegexBudget)
		}
	}
	if err != nil {
		return nil, sanitizeRegexError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return match, nil
}

func boundedReplace(ctx context.Context, expression *regexp2.Regexp, source, replacement string) (string, error) {
	budget := newRegexBudget()
	match, err := findRegex(ctx, expression, source, budget)
	if err != nil || match == nil {
		return source, err
	}
	runes := []rune(source)
	var output strings.Builder
	output.Grow(min(len(source), maxReplacementBytes))
	cursor := 0
	for match != nil {
		if match.Index < cursor || match.Index+match.Length > len(runes) {
			return "", fmt.Errorf("%w: invalid replacement match", ErrUnsupportedRegex)
		}
		if err := appendReplacement(&output, string(runes[cursor:match.Index])); err != nil {
			return "", err
		}
		expanded, err := expandReplacement(replacement, match)
		if err != nil {
			return "", err
		}
		if err := appendReplacement(&output, expanded); err != nil {
			return "", err
		}
		cursor = match.Index + match.Length
		match, err = nextRegex(ctx, expression, match, source, budget)
		if err != nil {
			return "", err
		}
	}
	if err := appendReplacement(&output, string(runes[cursor:])); err != nil {
		return "", err
	}
	return output.String(), nil
}

func appendReplacement(output *strings.Builder, text string) error {
	if len(text) > maxReplacementBytes-output.Len() {
		return fmt.Errorf("%w: replacement output exceeds %d bytes", ErrInvalidAction, maxReplacementBytes)
	}
	output.WriteString(text)
	return nil
}

func expandReplacement(replacement string, match *regexp2.Match) (string, error) {
	var output strings.Builder
	for index := 0; index < len(replacement); {
		if replacement[index] != '$' {
			output.WriteByte(replacement[index])
			index++
			continue
		}
		if index+1 == len(replacement) {
			output.WriteByte('$')
			break
		}
		next := replacement[index+1]
		if next == '$' {
			output.WriteByte('$')
			index += 2
			continue
		}
		if next != '{' {
			output.WriteByte('$')
			index++
			continue
		}
		end := strings.IndexByte(replacement[index+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("%w: unclosed replacement group", ErrInvalidAction)
		}
		name := replacement[index+2 : index+2+end]
		var group *regexp2.Group
		if number, err := strconv.Atoi(name); err == nil {
			group = match.GroupByNumber(number)
		} else {
			group = match.GroupByName(name)
		}
		if group != nil && len(group.Captures) != 0 {
			output.WriteString(group.String())
		}
		index += end + 3
	}
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
