// Package matchfilter implements the bounded, non-interactive subset of
// yt-dlp's --match-filters language. It never evaluates code. Regular
// expressions use Go's linear-time RE2 engine and are compiled during Parse.
package matchfilter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxFilters              = 64
	maxBytes                = 4096
	maxConditionsPerFilter  = 64
	maxConditions           = 256
	maxComparisonBytes      = maxBytes / 2
	maxRegexBytes           = 512
	maxEvaluatedStringBytes = 1 << 20
	maxIncompleteFields     = 256
)

var (
	ErrInvalidFilter   = errors.New("invalid match filter")
	ErrEvaluationLimit = errors.New("match filter evaluation limit exceeded")
	ErrEvaluation      = errors.New("match filter evaluation failed")
)

// SyntaxError identifies the byte range rejected by the parser.
type SyntaxError struct {
	Start, End int
	Message    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%v at bytes %d:%d: %s", ErrInvalidFilter, e.Start, e.End, e.Message)
}
func (e *SyntaxError) Unwrap() error { return ErrInvalidFilter }

// EvaluationError identifies the condition that could not be evaluated
// safely. Callers can distinguish resource limits from type mismatches with
// errors.Is.
type EvaluationError struct {
	Start, End int
	Field      string
	Err        error
}

func (e *EvaluationError) Error() string {
	return fmt.Sprintf("%v at bytes %d:%d for field %q: %v", ErrEvaluation, e.Start, e.End, e.Field, e.Err)
}
func (e *EvaluationError) Unwrap() []error { return []error{ErrEvaluation, e.Err} }

// EvaluationOptions models yt-dlp's incomplete argument. IncompleteAll treats
// every absent field as not-yet-known. IncompleteFields does so only for the
// named fields.
type EvaluationOptions struct {
	IncompleteAll    bool
	IncompleteFields map[string]struct{}
}

func (options EvaluationOptions) isIncomplete(field string) bool {
	if options.IncompleteAll {
		return true
	}
	_, ok := options.IncompleteFields[field]
	return ok
}

func (options EvaluationOptions) incomplete() bool {
	return options.IncompleteAll || len(options.IncompleteFields) != 0
}

// Program is an OR-list of filter expressions; a media entry passes when any
// expression passes. Conditions inside one expression are ANDed.
type Program struct{ expressions []expression }

type expression struct {
	source     string
	conditions []condition
}

type condition struct {
	field, operator, raw string
	start, end           int
	unaryNegated         bool
	comparisonNegated    bool
	noneInclusive        bool
	expression           *regexp.Regexp
}

type Decision struct {
	Pass       bool
	Reason     string
	Incomplete bool
}

// Parse compiles user-supplied filters with hard size, count, and regex bounds.
// Interactive "-" filters are intentionally outside this package.
func Parse(filters []string) (Program, error) {
	if len(filters) > maxFilters {
		return Program{}, fmt.Errorf("%w: more than %d expressions", ErrInvalidFilter, maxFilters)
	}
	program := Program{}
	totalConditions := 0
	for _, input := range filters {
		if len(input) == 0 || len(input) > maxBytes {
			return Program{}, fmt.Errorf("%w: empty or oversized expression", ErrInvalidFilter)
		}
		if input == "-" {
			return Program{}, syntax(0, 1, "interactive filters are unsupported")
		}
		parts, err := splitAnd(input)
		if err != nil {
			return Program{}, err
		}
		if len(parts) > maxConditionsPerFilter {
			return Program{}, syntax(0, len(input), "too many conditions")
		}
		totalConditions += len(parts)
		if totalConditions > maxConditions {
			return Program{}, fmt.Errorf("%w: more than %d total conditions", ErrInvalidFilter, maxConditions)
		}
		expr := expression{source: input, conditions: make([]condition, 0, len(parts))}
		for _, part := range parts {
			parsed, err := parseCondition(part)
			if err != nil {
				return Program{}, err
			}
			expr.conditions = append(expr.conditions, parsed)
		}
		program.expressions = append(program.expressions, expr)
	}
	return program, nil
}

// Evaluate preserves the original API. New integrations should use
// EvaluateContext so cancellation and evaluation-limit failures are explicit.
// A legacy evaluation failure becomes a deterministic rejection.
func (p Program) Evaluate(info value.Info, incomplete bool) Decision {
	decision, err := p.EvaluateContext(context.Background(), info, EvaluationOptions{IncompleteAll: incomplete})
	if err != nil {
		return rejection(info, incomplete, "cannot be evaluated safely")
	}
	return decision
}

// EvaluateContext evaluates the program with cancellation and field-specific
// incomplete-value semantics.
func (p Program) EvaluateContext(ctx context.Context, info value.Info, options EvaluationOptions) (Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(options.IncompleteFields) > maxIncompleteFields {
		return Decision{}, fmt.Errorf("%w: more than %d incomplete fields", ErrEvaluationLimit, maxIncompleteFields)
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if len(p.expressions) == 0 {
		return Decision{Pass: true, Incomplete: options.incomplete()}, nil
	}
	for _, expr := range p.expressions {
		if err := ctx.Err(); err != nil {
			return Decision{}, err
		}
		pass := true
		for _, condition := range expr.conditions {
			if err := ctx.Err(); err != nil {
				return Decision{}, err
			}
			matched, err := condition.matches(info, options)
			if err != nil {
				return Decision{}, &EvaluationError{
					Start: condition.start, End: condition.end, Field: condition.field, Err: err,
				}
			}
			if !matched {
				pass = false
				break
			}
		}
		if pass {
			return Decision{Pass: true, Incomplete: options.incomplete()}, nil
		}
	}
	return rejection(info, options.incomplete(), ""), nil
}

func rejection(info value.Info, incomplete bool, suffix string) Decision {
	title, _ := info.Title()
	if title == "" {
		title, _ = info.ID()
	}
	if title == "" {
		title = "entry"
	}
	reason := fmt.Sprintf("%s does not pass filter, skipping", title)
	if suffix != "" {
		reason = fmt.Sprintf("%s: %s", reason, suffix)
	}
	return Decision{Reason: reason, Incomplete: incomplete}
}

type segment struct {
	text       string
	start, end int
}

// splitAnd follows yt-dlp's \& escape at the language boundary. Other
// backslashes are retained for quote and regular-expression parsing.
func splitAnd(input string) ([]segment, error) {
	var result []segment
	start := 0
	for index := 0; index < len(input); index++ {
		if input[index] != '&' || index > 0 && input[index-1] == '\\' {
			continue
		}
		if strings.TrimSpace(input[start:index]) == "" {
			return nil, syntax(index, index+1, "empty condition")
		}
		result = append(result, segment{text: input[start:index], start: start, end: index})
		start = index + 1
	}
	if strings.TrimSpace(input[start:]) == "" {
		position := max(0, start-1)
		return nil, syntax(position, max(position+1, len(input)), "empty condition")
	}
	return append(result, segment{text: input[start:], start: start, end: len(input)}), nil
}

var validField = regexp.MustCompile(`^[a-z_]+$`)

var comparisonOperators = []string{"*=", "^=", "$=", "~=", "<=", ">=", "=", "<", ">"}

func parseCondition(input segment) (condition, error) {
	part := strings.TrimSpace(input.text)
	leftTrim := len(input.text) - len(strings.TrimLeft(input.text, " \t\r\n"))
	offset := input.start + leftTrim
	end := offset + len(part)
	if part == "" {
		return condition{}, syntax(offset, max(offset+1, input.end), "empty condition")
	}

	if strings.HasPrefix(part, "!") && !containsComparisonOperator(part[1:]) {
		field := strings.TrimSpace(part[1:])
		if !validField.MatchString(field) {
			return condition{}, syntax(offset, end, "invalid field")
		}
		return condition{field: field, unaryNegated: true, start: offset, end: end}, nil
	}
	if validField.MatchString(part) {
		return condition{field: part, start: offset, end: end}, nil
	}

	fieldEnd := 0
	for fieldEnd < len(part) && isFieldByte(part[fieldEnd], fieldEnd == 0) {
		fieldEnd++
	}
	field := part[:fieldEnd]
	if !validField.MatchString(field) {
		return condition{}, syntax(offset, end, "invalid field or comparison")
	}
	cursor := skipSpace(part, fieldEnd)
	comparisonNegated := false
	if cursor < len(part) && part[cursor] == '!' {
		comparisonNegated = true
		cursor = skipSpace(part, cursor+1)
	}
	operator := ""
	for _, candidate := range comparisonOperators {
		if strings.HasPrefix(part[cursor:], candidate) {
			operator = candidate
			cursor += len(candidate)
			break
		}
	}
	if operator == "" {
		return condition{}, syntax(offset+cursor, end, "comparison operator is missing or invalid")
	}
	cursor = skipSpace(part, cursor)
	noneInclusive := false
	if cursor < len(part) && part[cursor] == '?' {
		noneInclusive = true
		cursor = skipSpace(part, cursor+1)
	}
	if cursor >= len(part) {
		return condition{}, syntax(offset+cursor, end, "comparison value is missing")
	}
	rawStart := cursor
	raw := strings.TrimSpace(part[cursor:])
	if len(raw) > maxComparisonBytes {
		return condition{}, syntax(offset+rawStart, end, "comparison value exceeds size limit")
	}
	raw, err := parseComparisonValue(raw)
	if err != nil {
		return condition{}, syntax(offset+rawStart, end, err.Error())
	}
	raw = strings.ReplaceAll(raw, `\&`, "&")

	parsed := condition{
		field: field, operator: operator, raw: raw, start: offset, end: end,
		comparisonNegated: comparisonNegated, noneInclusive: noneInclusive,
	}
	if operator == "~=" {
		if len(raw) > maxRegexBytes {
			return condition{}, syntax(offset+rawStart, end, "regular expression exceeds size limit")
		}
		parsed.expression, err = regexp.Compile(raw)
		if err != nil {
			return condition{}, syntax(offset+rawStart, end, "invalid or unsupported RE2 regular expression")
		}
	}
	return parsed, nil
}

func parseComparisonValue(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("comparison value is missing")
	}
	if raw[0] != '\'' && raw[0] != '"' {
		return raw, nil
	}
	quote := raw[0]
	if len(raw) < 2 || raw[len(raw)-1] != quote {
		return "", errors.New("unterminated quoted value")
	}
	if len(raw) == 2 {
		return "", errors.New("quoted value must not be empty")
	}
	return strings.ReplaceAll(raw[1:len(raw)-1], `\`+string(quote), string(quote)), nil
}

func containsComparisonOperator(input string) bool {
	for _, operator := range comparisonOperators {
		if strings.Contains(input, operator) {
			return true
		}
	}
	return false
}

func isFieldByte(input byte, first bool) bool {
	return input == '_' || input >= 'a' && input <= 'z'
}

func skipSpace(input string, index int) int {
	for index < len(input) && strings.ContainsRune(" \t\r\n", rune(input[index])) {
		index++
	}
	return index
}

func (c condition) matches(info value.Info, options EvaluationOptions) (bool, error) {
	input := info.Lookup(c.field)
	if input.IsMissing() || input.IsNull() {
		if options.isIncomplete(c.field) {
			return true, nil
		}
		if c.operator != "" {
			return c.noneInclusive, nil
		}
		return c.unaryNegated, nil
	}
	if c.operator == "" {
		if boolean, ok := input.Bool(); ok {
			return boolean != c.unaryNegated, nil
		}
		return !c.unaryNegated, nil
	}

	var matched bool
	if text, ok := input.StringValue(); ok {
		if len(text) > maxEvaluatedStringBytes {
			return false, fmt.Errorf("%w: string exceeds %d bytes", ErrEvaluationLimit, maxEvaluatedStringBytes)
		}
		matched = c.matchString(text)
	} else if left, ok := number(input); ok {
		if isStringOperator(c.operator) {
			return false, fmt.Errorf("%w: operator %s only supports string values", ErrEvaluation, c.operator)
		}
		right, ok := parseNumericValue(c.raw)
		if !ok {
			return false, fmt.Errorf("%w: %q is not a bounded number, filesize, or duration", ErrEvaluation, c.raw)
		}
		matched = compareNumbers(left, right, c.operator)
	} else {
		matched = false
	}
	if c.comparisonNegated {
		matched = !matched
	}
	return matched, nil
}

func (c condition) matchString(input string) bool {
	switch c.operator {
	case "=":
		return input == c.raw
	case "<":
		return input < c.raw
	case "<=":
		return input <= c.raw
	case ">":
		return input > c.raw
	case ">=":
		return input >= c.raw
	case "*=":
		return strings.Contains(input, c.raw)
	case "^=":
		return strings.HasPrefix(input, c.raw)
	case "$=":
		return strings.HasSuffix(input, c.raw)
	case "~=":
		return c.expression.MatchString(input)
	default:
		return false
	}
}

func isStringOperator(operator string) bool {
	switch operator {
	case "*=", "^=", "$=", "~=":
		return true
	default:
		return false
	}
}

type numericValue struct {
	integer  int64
	floating float64
	isInt    bool
}

func compareNumbers(left, right numericValue, operator string) bool {
	if left.isInt && right.isInt {
		switch operator {
		case "=":
			return left.integer == right.integer
		case "<":
			return left.integer < right.integer
		case "<=":
			return left.integer <= right.integer
		case ">":
			return left.integer > right.integer
		case ">=":
			return left.integer >= right.integer
		default:
			return false
		}
	}
	leftFloat := left.floating
	if left.isInt {
		leftFloat = float64(left.integer)
	}
	rightFloat := right.floating
	if right.isInt {
		rightFloat = float64(right.integer)
	}
	switch operator {
	case "=":
		return leftFloat == rightFloat
	case "<":
		return leftFloat < rightFloat
	case "<=":
		return leftFloat <= rightFloat
	case ">":
		return leftFloat > rightFloat
	case ">=":
		return leftFloat >= rightFloat
	default:
		return false
	}
}

func number(input value.Value) (numericValue, bool) {
	if boolean, ok := input.Bool(); ok {
		if boolean {
			return numericValue{integer: 1, isInt: true}, true
		}
		return numericValue{integer: 0, isInt: true}, true
	}
	if integer, ok := input.Int(); ok {
		return numericValue{integer: integer, isInt: true}, true
	}
	floating, ok := input.Float()
	return numericValue{floating: floating}, ok && !math.IsNaN(floating) && !math.IsInf(floating, 0)
}

func parseNumericComparison(raw string) (float64, bool) {
	parsed, ok := parseNumericValue(raw)
	if !ok {
		return 0, false
	}
	if parsed.isInt {
		return float64(parsed.integer), true
	}
	return parsed.floating, true
}

func parseNumericValue(raw string) (numericValue, bool) {
	if integer, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return numericValue{integer: integer, isInt: true}, true
	}
	if size, ok := parseFileSize(raw); ok {
		return numericValue{integer: size, isInt: true}, true
	}
	if size, ok := parseFileSize(raw + "B"); ok {
		return numericValue{integer: size, isInt: true}, true
	}
	duration, ok := parseDuration(raw)
	if !ok {
		return numericValue{}, false
	}
	if math.Trunc(duration) == duration && duration <= math.MaxInt64 {
		return numericValue{integer: int64(duration), isInt: true}, true
	}
	return numericValue{floating: duration}, true
}

// parseNumber is retained for compatibility with the original package tests
// and callers. Match-filter comparisons use parseNumericComparison, which adds
// the reference language's filesize and duration coercions.
func parseNumber(raw string) (float64, bool) {
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

var filesizePattern = regexp.MustCompile(`^([+]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+))\s*([A-Za-z]+)$`)

var filesizeUnits = map[string]float64{
	"B": 1, "b": 1, "bytes": 1,
	"KiB": 1 << 10, "KB": 1e3, "kB": 1 << 10, "Kb": 1e3, "kb": 1e3,
	"kilobytes": 1e3, "kibibytes": 1 << 10,
	"MiB": 1 << 20, "MB": 1e6, "mB": 1 << 20, "Mb": 1e6, "mb": 1e6,
	"megabytes": 1e6, "mebibytes": 1 << 20,
	"GiB": 1 << 30, "GB": 1e9, "gB": 1 << 30, "Gb": 1e9, "gb": 1e9,
	"gigabytes": 1e9, "gibibytes": 1 << 30,
	"TiB": 1 << 40, "TB": 1e12, "tB": 1 << 40, "Tb": 1e12, "tb": 1e12,
	"terabytes": 1e12, "tebibytes": 1 << 40,
	"PiB": 1 << 50, "PB": 1e15, "pB": 1 << 50, "Pb": 1e15, "pb": 1e15,
	"petabytes": 1e15, "pebibytes": 1 << 50,
	"EiB": 1 << 60, "EB": 1e18, "eB": 1 << 60, "Eb": 1e18, "eb": 1e18,
	"exabytes": 1e18, "exbibytes": 1 << 60,
}

func parseFileSize(raw string) (int64, bool) {
	match := filesizePattern.FindStringSubmatch(strings.TrimSpace(raw))
	if match == nil {
		return 0, false
	}
	number, err := strconv.ParseFloat(match[1], 64)
	unit, ok := filesizeUnits[match[2]]
	if err != nil || !ok || number < 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	result := math.RoundToEven(number * unit)
	if math.IsNaN(result) || math.IsInf(result, 0) || result > math.MaxInt64 {
		return 0, false
	}
	return int64(result), true
}

var durationUnitPattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*(days?|d|hours?|hrs?|h|minutes?|mins?\.?|m|seconds?|secs?|s)`)

func parseDuration(raw string) (float64, bool) {
	input := strings.TrimSpace(strings.TrimSuffix(raw, "Z"))
	if input == "" {
		return 0, false
	}
	if strings.Contains(input, ":") {
		parts := strings.Split(input, ":")
		if len(parts) < 2 || len(parts) > 4 {
			return 0, false
		}
		multipliers := []float64{1, 60, 3600, 86400}
		var total float64
		for index := len(parts) - 1; index >= 0; index-- {
			part, err := strconv.ParseFloat(parts[index], 64)
			if err != nil || part < 0 || index != len(parts)-1 && math.Trunc(part) != part {
				return 0, false
			}
			total += part * multipliers[len(parts)-1-index]
		}
		return finiteDuration(total)
	}
	if number, err := strconv.ParseFloat(input, 64); err == nil {
		return finiteDuration(number)
	}
	matches := durationUnitPattern.FindAllStringSubmatchIndex(input, -1)
	if len(matches) == 0 {
		return 0, false
	}
	position := 0
	var total float64
	for _, match := range matches {
		if strings.TrimSpace(input[position:match[0]]) != "" {
			return 0, false
		}
		number, err := strconv.ParseFloat(input[match[2]:match[3]], 64)
		if err != nil {
			return 0, false
		}
		switch strings.ToLower(strings.TrimSuffix(input[match[4]:match[5]], ".")) {
		case "d", "day", "days":
			total += number * 86400
		case "h", "hr", "hrs", "hour", "hours":
			total += number * 3600
		case "m", "min", "mins", "minute", "minutes":
			total += number * 60
		case "s", "sec", "secs", "second", "seconds":
			total += number
		default:
			return 0, false
		}
		position = match[1]
	}
	if strings.TrimSpace(input[position:]) != "" {
		return 0, false
	}
	return finiteDuration(total)
}

func finiteDuration(value float64) (float64, bool) {
	if value < 0 || value > math.MaxInt64 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func syntax(start, end int, message string) error {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	return &SyntaxError{Start: start, End: end, Message: message}
}
