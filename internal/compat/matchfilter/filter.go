// Package matchfilter implements the safe declarative subset of yt-dlp's
// --match-filter language. It never evaluates code or executes regular
// expressions supplied by an unbounded source.
package matchfilter

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxFilters = 64
	maxBytes   = 4096
)

var ErrInvalidFilter = errors.New("invalid match filter")

type SyntaxError struct {
	Start, End int
	Message    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%v at bytes %d:%d: %s", ErrInvalidFilter, e.Start, e.End, e.Message)
}
func (e *SyntaxError) Unwrap() error { return ErrInvalidFilter }

// Program is an OR-list of filter expressions; a video passes when any
// expression passes, matching the repeated --match-filter user experience.
type Program struct{ expressions []expression }
type expression struct {
	source     string
	conditions []condition
}
type condition struct {
	field, operator, raw string
	start, end           int
	negated              bool
}
type Decision struct {
	Pass       bool
	Reason     string
	Incomplete bool
}

// Parse compiles user-supplied filters with hard size/count bounds.
func Parse(filters []string) (Program, error) {
	if len(filters) > maxFilters {
		return Program{}, fmt.Errorf("%w: more than %d expressions", ErrInvalidFilter, maxFilters)
	}
	program := Program{}
	for _, input := range filters {
		if len(input) == 0 || len(input) > maxBytes {
			return Program{}, fmt.Errorf("%w: empty or oversized expression", ErrInvalidFilter)
		}
		parts, err := splitAnd(input)
		if err != nil {
			return Program{}, err
		}
		expr := expression{source: input}
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

// Evaluate returns a non-error rejection decision, keeping a policy skip
// distinct from parsing/network/extractor failures.
func (p Program) Evaluate(info value.Info, incomplete bool) Decision {
	if len(p.expressions) == 0 {
		return Decision{Pass: true, Incomplete: incomplete}
	}
	for _, expr := range p.expressions {
		pass := true
		for _, condition := range expr.conditions {
			if !condition.matches(info, incomplete) {
				pass = false
				break
			}
		}
		if pass {
			return Decision{Pass: true, Incomplete: incomplete}
		}
	}
	title, _ := info.Title()
	if title == "" {
		title, _ = info.ID()
	}
	if title == "" {
		title = "entry"
	}
	return Decision{Reason: fmt.Sprintf("%s does not pass filter, skipping", title), Incomplete: incomplete}
}

type segment struct {
	text       string
	start, end int
}

func splitAnd(input string) ([]segment, error) {
	var result []segment
	start := 0
	escaped := false
	for index := range input {
		if escaped {
			escaped = false
			continue
		}
		if input[index] == '\\' {
			escaped = true
			continue
		}
		if input[index] == '&' {
			if index == start {
				return nil, syntax(index, index+1, "empty condition")
			}
			result = append(result, segment{text: input[start:index], start: start, end: index})
			start = index + 1
		}
	}
	if escaped {
		return nil, syntax(len(input)-1, len(input), "trailing escape")
	}
	if start == len(input) {
		return nil, syntax(start-1, start, "empty condition")
	}
	return append(result, segment{text: input[start:], start: start, end: len(input)}), nil
}

var validField = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func parseCondition(input segment) (condition, error) {
	part := strings.TrimSpace(input.text)
	offset := input.start + len(input.text) - len(strings.TrimLeft(input.text, " \t"))
	if strings.HasPrefix(part, "!") && !strings.ContainsAny(part[1:], "<>=!~") {
		field := strings.TrimSpace(part[1:])
		if !validField.MatchString(field) {
			return condition{}, syntax(offset, input.end, "invalid field")
		}
		return condition{field: field, negated: true, start: offset, end: input.end}, nil
	}
	for _, operator := range []string{"!~=", "~=", "!=", ">=", "<=", "=", ">", "<"} {
		if at := strings.Index(part, operator); at > 0 {
			field, raw := strings.TrimSpace(part[:at]), strings.TrimSpace(part[at+len(operator):])
			if !validField.MatchString(field) || raw == "" {
				return condition{}, syntax(offset, input.end, "malformed comparison")
			}
			if len(raw) > maxBytes/2 {
				return condition{}, syntax(offset+at+len(operator), input.end, "value too large")
			}
			if len(raw) >= 2 && ((raw[0] == '\'' && raw[len(raw)-1] == '\'') || (raw[0] == '"' && raw[len(raw)-1] == '"')) {
				raw = raw[1 : len(raw)-1]
			}
			if strings.Contains(operator, "~") {
				if len(raw) > 512 {
					return condition{}, syntax(offset+at+len(operator), input.end, "regular expression too large")
				}
				if _, err := regexp.Compile(raw); err != nil {
					return condition{}, syntax(offset+at+len(operator), input.end, "invalid regular expression")
				}
			}
			return condition{field: field, operator: operator, raw: raw, start: offset, end: input.end}, nil
		}
	}
	if !validField.MatchString(part) {
		return condition{}, syntax(offset, input.end, "invalid field or comparison")
	}
	return condition{field: part, start: offset, end: input.end}, nil
}

func (c condition) matches(info value.Info, incomplete bool) bool {
	input := info.Lookup(c.field)
	if input.IsMissing() || input.IsNull() {
		if incomplete {
			return true
		}
		return c.operator == "" && c.negated
	}
	if c.operator == "" {
		if boolean, ok := input.Bool(); ok {
			return boolean != c.negated
		}
		return !c.negated
	}
	text, textOK := input.StringValue()
	left, numericOK := number(input)
	right, rightOK := parseNumber(c.raw)
	switch c.operator {
	case "=":
		return (textOK && text == c.raw) || (numericOK && rightOK && left == right)
	case "!=":
		return (!textOK || text != c.raw) && (!numericOK || !rightOK || left != right)
	case ">":
		return numericOK && rightOK && left > right
	case ">=":
		return numericOK && rightOK && left >= right
	case "<":
		return numericOK && rightOK && left < right
	case "<=":
		return numericOK && rightOK && left <= right
	case "~=", "!~=":
		re, _ := regexp.Compile(c.raw)
		match := textOK && re.MatchString(text)
		if c.operator == "!~=" {
			return !match
		}
		return match
	}
	return false
}
func number(v value.Value) (float64, bool) {
	if n, ok := v.Int(); ok {
		return float64(n), true
	}
	return v.Float()
}
func parseNumber(raw string) (float64, bool) {
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	return n, true
}
func syntax(start, end int, message string) error {
	return &SyntaxError{Start: start, End: end, Message: message}
}
