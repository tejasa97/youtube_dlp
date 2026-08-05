package format

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

type filterOperation uint8

const (
	filterOpEQ filterOperation = iota
	filterOpNE
	filterOpGT
	filterOpGE
	filterOpLT
	filterOpLE
	filterOpPrefix
	filterOpSuffix
	filterOpContains
	filterOpRegex
)

type filterKind uint8

const (
	filterKindNumeric filterKind = iota
	filterKindString
)

// compiledFilter is an immutable predicate compiled from raw filter syntax.
type compiledFilter struct {
	kind          filterKind
	field         string
	operation     filterOperation
	negated       bool
	noneInclusive bool
	number        filterNumericValue
	text          string
	expression    *pythonRegex
	span          span
}

func compileNodeFilters(node *astNode) error {
	if node == nil {
		return nil
	}
	for index := range node.filters {
		if err := compileFilter(&node.filters[index]); err != nil {
			return err
		}
	}
	for index := range node.children {
		if err := compileNodeFilters(&node.children[index]); err != nil {
			return err
		}
	}
	return nil
}

func compileFilter(filter *Filter) error {
	if filter.predicate != nil {
		return nil
	}
	raw := filter.raw
	start, end := filter.span.start, filter.span.end
	if raw == "" {
		raw = reconstructLegacyFilter(*filter)
		start, end = 0, len(raw)
		filter.raw = raw
		filter.span = span{start: start, end: end}
	}
	predicate, err := compileFilterSpec(raw, start, end)
	if err != nil {
		return err
	}
	filter.predicate = predicate
	if filter.Field == "" {
		filter.Field = predicate.field
	}
	return nil
}

func reconstructLegacyFilter(filter Filter) string {
	value := filter.Value
	switch filter.Operator {
	case "~=", "!~=", "^=", "!^=", "$=", "!$=", "*=", "!*=", "=", "!=":
		if needsLegacyQuotes(value) {
			value = `"` + escapeLegacyQuotes(value) + `"`
		}
	}
	return filter.Field + filter.Operator + value
}

func needsLegacyQuotes(value string) bool {
	if value == "" {
		return true
	}
	for _, r := range value {
		if !(r == '.' || r == '-' || isPythonWordRune(r)) {
			return true
		}
	}
	return false
}

func escapeLegacyQuotes(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func compileFilterSpec(raw string, start, end int) (*compiledFilter, error) {
	if !utf8.ValidString(raw) {
		return nil, selectorSyntax(start, end, "invalid UTF-8 in filter")
	}
	if predicate, ok, err := compileNumericFilter(raw, start, end); err != nil || ok {
		return predicate, err
	}
	return compileStringFilter(raw, start, end)
}

func compileNumericFilter(raw string, start, end int) (*compiledFilter, bool, error) {
	trimmed := strings.TrimSpace(raw)
	ops := []struct {
		text string
		op   filterOperation
	}{
		{"<=", filterOpLE},
		{">=", filterOpGE},
		{"!=", filterOpNE},
		{"=", filterOpEQ},
		{"<", filterOpLT},
		{">", filterOpGT},
	}
	for _, candidate := range ops {
		index := strings.Index(trimmed, candidate.text)
		if index <= 0 {
			continue
		}
		field := strings.TrimSpace(trimmed[:index])
		rest := strings.TrimSpace(trimmed[index+len(candidate.text):])
		noneInclusive := false
		if strings.HasPrefix(rest, "?") {
			noneInclusive = true
			rest = strings.TrimSpace(rest[1:])
		}
		if !isNumericFilterKey(field) || !isNumericFilterValue(rest) {
			continue
		}
		number, ok := parseFilterNumber(rest)
		if !ok {
			return nil, true, selectorSyntax(start, end, fmt.Sprintf("Invalid value %q in format specification %q", rest, raw))
		}
		return &compiledFilter{
			kind:          filterKindNumeric,
			field:         field,
			operation:     candidate.op,
			noneInclusive: noneInclusive,
			number:        number,
			span:          span{start: start, end: end},
		}, true, nil
	}
	return nil, false, nil
}

func compileStringFilter(raw string, start, end int) (*compiledFilter, error) {
	trimmed := strings.TrimSpace(raw)
	ops := []struct {
		text string
		op   filterOperation
	}{
		{"^=", filterOpPrefix},
		{"$=", filterOpSuffix},
		{"*=", filterOpContains},
		{"~=", filterOpRegex},
		{"=", filterOpEQ},
	}
	for _, candidate := range ops {
		for _, match := range findStringOperator(trimmed, candidate.text) {
			field := strings.TrimSpace(trimmed[:match.negStart])
			if !isStringFilterKey(field) {
				continue
			}
			negated := match.negStart < match.opStart
			if negated && !isNegationPrefix(trimmed[match.negStart:match.opStart]) {
				continue
			}
			rest := trimLeftSpaces(trimmed[match.opEnd:])
			noneInclusive := false
			if strings.HasPrefix(rest, "?") {
				noneInclusive = true
				rest = trimLeftSpaces(rest[1:])
			}
			captured, consumed, err := parseStringFilterValue(rest, start, end)
			if err != nil {
				return nil, err
			}
			if consumed == 0 || strings.TrimSpace(rest[consumed:]) != "" {
				continue
			}
			text := captured
			if candidate.op != filterOpRegex {
				text = unescapeFilterString(captured)
			}
			predicate := &compiledFilter{
				kind:          filterKindString,
				field:         field,
				operation:     candidate.op,
				negated:       negated,
				noneInclusive: noneInclusive,
				text:          text,
				span:          span{start: start, end: end},
			}
			if candidate.op == filterOpRegex {
				expression, err := compilePythonRegex(text, start, end)
				if err != nil {
					return nil, err
				}
				predicate.expression = expression
			}
			return predicate, nil
		}
	}
	return nil, selectorSyntax(start, end, fmt.Sprintf("Invalid filter specification %q", raw))
}

type stringOpMatch struct {
	negStart int
	opStart  int
	opEnd    int
}

func findStringOperator(input, op string) []stringOpMatch {
	var matches []stringOpMatch
	for index := 0; index+len(op) <= len(input); index++ {
		if input[index:index+len(op)] != op {
			continue
		}
		negStart := index
		bang := index - 1
		for bang >= 0 && unicode.IsSpace(rune(input[bang])) {
			bang--
		}
		if bang >= 0 && input[bang] == '!' {
			negStart = bang
		}
		matches = append(matches, stringOpMatch{negStart: negStart, opStart: index, opEnd: index + len(op)})
	}
	return matches
}

func isNegationPrefix(text string) bool {
	if text == "" {
		return false
	}
	if text[0] != '!' {
		return false
	}
	return isOnlySpaces(text[1:])
}

func isOnlySpaces(text string) bool {
	for _, r := range text {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func trimLeftSpaces(text string) string {
	return strings.TrimLeftFunc(text, unicode.IsSpace)
}

func parseStringFilterValue(rest string, start, end int) (string, int, error) {
	if rest == "" {
		return "", 0, nil
	}
	if rest[0] == '"' || rest[0] == '\'' {
		quote := rest[0]
		var builder strings.Builder
		index := 1
		for index < len(rest) {
			ch := rest[index]
			if ch == '\\' && index+1 < len(rest) {
				// Filter grammar includes both characters of `\.` in the capture.
				builder.WriteByte('\\')
				builder.WriteByte(rest[index+1])
				index += 2
				continue
			}
			if ch == quote {
				if builder.Len() == 0 {
					return "", 0, selectorSyntax(start, end, "empty quoted filter value")
				}
				return builder.String(), index + 1, nil
			}
			builder.WriteByte(ch)
			index++
		}
		return "", 0, selectorSyntax(start, end, "unclosed quoted filter value")
	}
	index := 0
	for index < len(rest) {
		r, width := utf8.DecodeRuneInString(rest[index:])
		if r == '.' || r == '-' || isPythonWordRune(r) {
			index += width
			continue
		}
		break
	}
	if index == 0 {
		return "", 0, nil
	}
	return rest[:index], index, nil
}

func unescapeFilterString(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == '\\' && index+1 < len(value) {
			next := value[index+1]
			if next == '\\' || next == '"' || next == '\'' {
				builder.WriteByte(next)
				index++
				continue
			}
		}
		builder.WriteByte(value[index])
	}
	return builder.String()
}

func isNumericFilterKey(field string) bool {
	if field == "" {
		return false
	}
	for _, r := range field {
		if r == '.' || r == '-' || isPythonWordRune(r) {
			continue
		}
		return false
	}
	return true
}

func isStringFilterKey(field string) bool {
	if field == "" {
		return false
	}
	for _, r := range field {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

func isNumericFilterValue(value string) bool {
	if value == "" {
		return false
	}
	index := 0
	for index < len(value) {
		ch := value[index]
		if ch >= '0' && ch <= '9' || ch == '.' {
			index++
			continue
		}
		break
	}
	if index == 0 {
		return false
	}
	rest := value[index:]
	if rest == "" {
		return true
	}
	if !strings.ContainsRune("kKmMgGtTpPeEzZyY", rune(rest[0])) {
		return false
	}
	rest = rest[1:]
	if rest == "" {
		return true
	}
	if rest[0] == 'i' {
		rest = rest[1:]
	}
	if rest == "" {
		return true
	}
	return len(rest) == 1 && (rest[0] == 'B' || rest[0] == 'b')
}

func isPythonWordRune(r rune) bool {
	if r == '_' || unicode.IsLetter(r) {
		return true
	}
	return unicode.In(r, unicode.Nd, unicode.Nl, unicode.No)
}

func (filter *Filter) match(object *value.Object, budget *regexEvalBudget) (bool, error) {
	if filter.predicate == nil {
		if err := compileFilter(filter); err != nil {
			return false, err
		}
	}
	return filter.predicate.match(object, budget)
}

func (predicate *compiledFilter) match(object *value.Object, budget *regexEvalBudget) (bool, error) {
	actual := object.Lookup(predicate.field)
	if actual.IsMissing() || actual.IsNull() {
		return predicate.noneInclusive, nil
	}
	switch predicate.kind {
	case filterKindNumeric:
		return predicate.matchNumeric(actual)
	case filterKindString:
		return predicate.matchString(actual, budget)
	default:
		return false, fmtFilterEval("unknown filter kind")
	}
}

func (predicate *compiledFilter) matchNumeric(actual value.Value) (bool, error) {
	left, ok := actualNumericForFilter(actual)
	if !ok {
		switch predicate.operation {
		case filterOpEQ:
			return false, nil
		case filterOpNE:
			return true, nil
		default:
			return false, filterEval(predicate.span, predicate.field, fmt.Errorf("%w: incompatible types for numeric comparison", ErrFilterEvaluation))
		}
	}
	return compareFilterNumbers(left, predicate.number, predicate.operation)
}

func actualNumericForFilter(actual value.Value) (filterNumericValue, bool) {
	if integer, ok := actual.Int(); ok {
		return int64Numeric(integer), true
	}
	if floating, ok := actual.Float(); ok {
		return filterNumericValue{floating: floating}, true
	}
	if boolean, ok := actual.Bool(); ok {
		var integer int64
		if boolean {
			integer = 1
		}
		return int64Numeric(integer), true
	}
	return filterNumericValue{}, false
}

func (predicate *compiledFilter) matchString(actual value.Value, budget *regexEvalBudget) (bool, error) {
	text, ok := actual.StringValue()
	if !ok {
		// Python string equality uses == / != across types. Other string
		// operators call str methods and raise on non-strings.
		if predicate.operation == filterOpEQ {
			matched := false
			if predicate.negated {
				matched = !matched
			}
			return matched, nil
		}
		return false, filterEval(predicate.span, predicate.field, fmt.Errorf("%w: field is not a string", ErrFilterEvaluation))
	}
	var matched bool
	var err error
	switch predicate.operation {
	case filterOpEQ:
		matched = text == predicate.text
	case filterOpPrefix:
		matched = strings.HasPrefix(text, predicate.text)
	case filterOpSuffix:
		matched = strings.HasSuffix(text, predicate.text)
	case filterOpContains:
		matched = strings.Contains(text, predicate.text)
	case filterOpRegex:
		if predicate.expression == nil {
			return false, filterEval(predicate.span, predicate.field, fmt.Errorf("%w: missing regular expression", ErrFilterEvaluation))
		}
		matched, err = predicate.expression.search(text, budget)
		if err != nil {
			return false, err
		}
	default:
		return false, fmtFilterEval("unknown string operator")
	}
	if predicate.negated {
		matched = !matched
	}
	return matched, nil
}

func fmtFilterEval(message string) error {
	return fmt.Errorf("%w: %s", ErrFilterEvaluation, message)
}

func filterEval(span span, field string, err error) error {
	return fmt.Errorf("%w at bytes %d:%d for field %q: %v", ErrFilterEvaluation, span.start, span.end, field, err)
}
