// Package template implements the output-template compatibility layers.
package template

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalidTemplate = errors.New("invalid output template")
	ErrUnsafePath      = errors.New("output path escapes its root")
	errTraversalBudget = errors.New("traversal expansion exceeds item limit")
)

// SyntaxError identifies the byte range rejected by the pilot parser.
type SyntaxError struct {
	Start   int
	End     int
	Message string
}

func (err *SyntaxError) Error() string {
	return fmt.Sprintf("%v at bytes %d:%d: %s", ErrInvalidTemplate, err.Start, err.End, err.Message)
}

func (err *SyntaxError) Unwrap() error { return ErrInvalidTemplate }

const (
	maxTemplateBytes    = 64 << 10
	maxExpressions      = 256
	maxRenderedBytes    = 1 << 20
	maxScalarBytes      = 256 << 10
	maxFormatWidth      = 4096
	maxFormatPrecision  = 4096
	maxProjectionFields = 64
	maxTraversalItems   = 4096
	maxTraversalSteps   = 64
	maxArithmeticOps    = 64
	maxJSONDepth        = 64
)

var formatSpecPattern = regexp.MustCompile(
	`^[-+0 #]*[0-9]*(\.[0-9]+)?[sdfhlBUDqSrac]$` +
		`|^[#+]*j$`,
)

// Render supports literal text, %%, traversal/alternative/default expressions,
// object projections, replacement templates, date conversion, and bounded
// scalar and JSON format specs.
func Render(pattern string, info value.Info) (string, error) {
	if len(pattern) > maxTemplateBytes {
		return "", templateSyntax(0, len(pattern), "template exceeds size limit")
	}
	var output strings.Builder
	expressions := 0
	for index := 0; index < len(pattern); {
		if pattern[index] != '%' {
			if err := appendBounded(&output, pattern[index:index+1]); err != nil {
				return "", err
			}
			index++
			continue
		}
		if index+1 < len(pattern) && pattern[index+1] == '%' {
			if err := appendBounded(&output, "%"); err != nil {
				return "", err
			}
			index += 2
			continue
		}
		if index+2 >= len(pattern) || pattern[index+1] != '(' {
			end := min(index+2, len(pattern))
			return "", templateSyntax(index, end, "expected % or %(field)s")
		}
		closeOffset := strings.IndexByte(pattern[index+2:], ')')
		if closeOffset < 0 {
			return "", templateSyntax(index, len(pattern), "unclosed field")
		}
		closeIndex := index + 2 + closeOffset
		specEnd := closeIndex + 1
		for specEnd < len(pattern) && !strings.ContainsRune("sdfjlhUDBcqSra", rune(pattern[specEnd])) {
			specEnd++
		}
		if specEnd >= len(pattern) {
			return "", templateSyntax(closeIndex+1, len(pattern), "missing conversion type")
		}
		spec := pattern[closeIndex+1 : specEnd+1]
		if !formatSpecPattern.MatchString(spec) {
			return "", templateSyntax(closeIndex+1, specEnd+1, fmt.Sprintf("invalid format spec %q", spec))
		}
		expression := pattern[index+2 : closeIndex]
		expressions++
		if expressions > maxExpressions {
			return "", templateSyntax(index, closeIndex+1, "too many template expressions")
		}
		rendered, err := renderExpression(expression, spec, info)
		if err != nil {
			return "", templateSyntax(index+2, closeIndex, fmt.Sprintf("expression %q: %v", expression, err))
		}
		if err := appendBounded(&output, rendered); err != nil {
			return "", err
		}
		index = specEnd + 1
	}
	return output.String(), nil
}

func appendBounded(output *strings.Builder, text string) error {
	if len(text) > maxRenderedBytes-output.Len() {
		return fmt.Errorf("%w: rendered output exceeds %d bytes", ErrInvalidTemplate, maxRenderedBytes)
	}
	output.WriteString(text)
	return nil
}

func templateSyntax(start, end int, message string) error {
	if end < start {
		end = start
	}
	return &SyntaxError{Start: start, End: end, Message: message}
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
		if spec[len(spec)-1] != 'j' {
			return "", errors.New("empty expression requires JSON conversion")
		}
		return formatValue(value.ObjectValue(info.Fields()), spec)
	}
	source, defaultValue, hasDefault := strings.Cut(expression, "|")
	source, replacement, hasReplacement := strings.Cut(source, "&")
	var selected value.Value
	var dateFormat string
	for _, alternative := range splitAlternatives(source) {
		path, format, hasDateFormat := strings.Cut(strings.TrimSpace(alternative), ">")
		candidate, err := evaluateCandidate(info, path)
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
		replaced, err := replaceBounded(replacement, raw)
		if err != nil {
			return "", err
		}
		selected = value.String(replaced)
	}
	return formatValue(selected, spec)
}

type arithmeticNumber struct {
	integer bool
	int64   int64
	float64 float64
}

func evaluateCandidate(info value.Info, expression string) (value.Value, error) {
	operands, operators, negate, arithmetic, err := parseArithmetic(expression)
	if err != nil {
		return value.Missing(), err
	}
	if !arithmetic {
		return traverse(info, expression)
	}
	var current arithmeticNumber
	ok, negationApplied := false, false
	if negate {
		current, ok, err = parseArithmeticNumber("-" + operands[0])
		negationApplied = ok
	}
	if err == nil && !ok {
		current, ok, err = arithmeticOperand(info, operands[0])
	}
	if err != nil || !ok {
		return value.Missing(), err
	}
	if negate && !negationApplied {
		if current.integer {
			if current.int64 == math.MinInt64 {
				return value.Missing(), errors.New("integer arithmetic overflow")
			}
			current.int64 = -current.int64
		} else {
			current.float64 = -current.float64
		}
	}
	for index, operator := range operators {
		next, ok, err := arithmeticOperand(info, operands[index+1])
		if err != nil || !ok {
			return value.Missing(), err
		}
		current, err = applyArithmetic(current, next, operator)
		if err != nil {
			return value.Missing(), err
		}
	}
	if current.integer {
		return value.Int(current.int64), nil
	}
	return value.Float(current.float64), nil
}

// parseArithmetic recognizes yt-dlp's deliberately left-to-right arithmetic.
// A minus after a dot or slice colon remains part of traversal syntax.
func parseArithmetic(expression string) ([]string, []byte, bool, bool, error) {
	if expression == "" {
		return nil, nil, false, false, errors.New("empty arithmetic expression")
	}
	if strings.ContainsAny(expression, "/()%^\t\r\n ") {
		return nil, nil, false, false, errors.New("unsupported arithmetic syntax")
	}
	negate := expression[0] == '-'
	start := 0
	if negate {
		start = 1
		if start == len(expression) {
			return nil, nil, false, false, errors.New("dangling unary negation")
		}
	}
	operands := make([]string, 0, 2)
	operators := make([]byte, 0, 1)
	operandStart, braceDepth := start, 0
	for index := start; index < len(expression); index++ {
		character := expression[index]
		switch character {
		case '{':
			braceDepth++
			continue
		case '}':
			if braceDepth == 0 {
				return nil, nil, false, false, errors.New("malformed arithmetic expression")
			}
			braceDepth--
			continue
		}
		if braceDepth != 0 || (character != '+' && character != '-' && character != '*') {
			continue
		}
		if (character == '+' || character == '-') && index > operandStart {
			previous := expression[index-1]
			if (previous == '.' || previous == ':') && index+1 < len(expression) &&
				expression[index+1] >= '0' && expression[index+1] <= '9' {
				continue
			}
			if previous == 'e' || previous == 'E' {
				if _, err := strconv.ParseFloat(expression[operandStart:index-1], 64); err == nil {
					continue
				}
			}
		}
		if index == operandStart {
			return nil, nil, false, false, errors.New("repeated arithmetic operator")
		}
		operands = append(operands, expression[operandStart:index])
		operators = append(operators, character)
		if len(operators) > maxArithmeticOps {
			return nil, nil, false, false, errors.New("too many arithmetic operations")
		}
		operandStart = index + 1
	}
	if braceDepth != 0 {
		return nil, nil, false, false, errors.New("malformed arithmetic expression")
	}
	if operandStart == len(expression) {
		return nil, nil, false, false, errors.New("dangling arithmetic operator")
	}
	operands = append(operands, expression[operandStart:])
	return operands, operators, negate, negate || len(operators) != 0, nil
}

func arithmeticOperand(info value.Info, operand string) (arithmeticNumber, bool, error) {
	if number, ok, err := parseArithmeticNumber(operand); ok || err != nil {
		return number, ok, err
	}
	if _, err := parseTraversal(operand); err != nil {
		return arithmeticNumber{}, false, err
	}
	selected, err := traverse(info, operand)
	if err != nil {
		if errors.Is(err, errTraversalBudget) {
			return arithmeticNumber{}, false, err
		}
		return arithmeticNumber{}, false, nil
	}
	if selected.IsMissing() || selected.IsNull() {
		return arithmeticNumber{}, false, nil
	}
	switch selected.Kind() {
	case value.KindInt:
		integer, _ := selected.Int()
		return arithmeticNumber{integer: true, int64: integer}, true, nil
	case value.KindFloat:
		floating, _ := selected.Float()
		if !isFinite(floating) {
			return arithmeticNumber{}, false, errors.New("non-finite arithmetic operand")
		}
		return arithmeticNumber{float64: floating}, true, nil
	case value.KindString:
		text, _ := selected.StringValue()
		return parseArithmeticNumber(text)
	default:
		return arithmeticNumber{}, false, nil
	}
}

func parseArithmeticNumber(text string) (arithmeticNumber, bool, error) {
	if text == "" {
		return arithmeticNumber{}, false, nil
	}
	integerSyntax, hasDigit := true, false
	for index, character := range text {
		if index == 0 && (character == '+' || character == '-') {
			continue
		}
		if character < '0' || character > '9' {
			integerSyntax = false
			break
		}
		hasDigit = true
	}
	if integerSyntax && hasDigit {
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return arithmeticNumber{}, false, errors.New("integer arithmetic operand overflows int64")
		}
		return arithmeticNumber{integer: true, int64: integer}, true, nil
	}
	floating, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return arithmeticNumber{}, false, nil
	}
	if !isFinite(floating) {
		return arithmeticNumber{}, false, errors.New("non-finite arithmetic operand")
	}
	return arithmeticNumber{float64: floating}, true, nil
}

func applyArithmetic(left, right arithmeticNumber, operator byte) (arithmeticNumber, error) {
	if left.integer && right.integer {
		result, ok := integerArithmetic(left.int64, right.int64, operator)
		if !ok {
			return arithmeticNumber{}, errors.New("integer arithmetic overflow")
		}
		return arithmeticNumber{integer: true, int64: result}, nil
	}
	leftFloat := left.float64
	if left.integer {
		leftFloat = float64(left.int64)
	}
	rightFloat := right.float64
	if right.integer {
		rightFloat = float64(right.int64)
	}
	var result float64
	switch operator {
	case '+':
		result = leftFloat + rightFloat
	case '-':
		result = leftFloat - rightFloat
	case '*':
		result = leftFloat * rightFloat
	default:
		return arithmeticNumber{}, errors.New("unsupported arithmetic operator")
	}
	if !isFinite(result) {
		return arithmeticNumber{}, errors.New("non-finite arithmetic result")
	}
	return arithmeticNumber{float64: result}, nil
}

func integerArithmetic(left, right int64, operator byte) (int64, bool) {
	switch operator {
	case '+':
		if (right > 0 && left > math.MaxInt64-right) || (right < 0 && left < math.MinInt64-right) {
			return 0, false
		}
		return left + right, true
	case '-':
		if (right < 0 && left > math.MaxInt64+right) || (right > 0 && left < math.MinInt64+right) {
			return 0, false
		}
		return left - right, true
	case '*':
		if left == 0 || right == 0 {
			return 0, true
		}
		if (left == math.MinInt64 && right == -1) || (right == math.MinInt64 && left == -1) {
			return 0, false
		}
		result := left * right
		if result/right != left {
			return 0, false
		}
		return result, true
	default:
		return 0, false
	}
}

func isFinite(number float64) bool {
	return !math.IsNaN(number) && !math.IsInf(number, 0)
}

func splitAlternatives(source string) []string {
	alternatives := make([]string, 0, 1)
	start, depth := 0, 0
	for index, character := range source {
		switch character {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				alternatives = append(alternatives, source[start:index])
				start = index + 1
			}
		}
	}
	return append(alternatives, source[start:])
}

func replaceBounded(replacement, raw string) (string, error) {
	if len(raw) > maxScalarBytes {
		return "", errors.New("replacement source exceeds size limit")
	}
	count := strings.Count(replacement, "{}")
	if count == 0 {
		return "", errors.New("replacement must contain {}")
	}
	length := len(replacement)
	if len(raw) > 2 {
		if count > (maxScalarBytes-length)/(len(raw)-2) {
			return "", errors.New("replacement output exceeds size limit")
		}
		length += count * (len(raw) - 2)
	}
	if length > maxScalarBytes {
		return "", errors.New("replacement output exceeds size limit")
	}
	return strings.ReplaceAll(replacement, "{}", raw), nil
}

func traverse(info value.Info, path string) (value.Value, error) {
	steps, err := parseTraversal(path)
	if err != nil {
		return value.Missing(), err
	}
	if len(steps) == 0 {
		return value.Missing(), errors.New("empty traversal path")
	}
	budget := traversalBudget{remaining: maxTraversalItems}
	return traverseSteps(value.ObjectValue(info.Fields()), steps, &budget)
}

type traversalBudget struct {
	remaining int
}

func (budget *traversalBudget) consume(count int) error {
	if count < 0 || count > budget.remaining {
		return errTraversalBudget
	}
	budget.remaining -= count
	return nil
}

func parseTraversal(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	steps := make([]string, 0, 4)
	for index := 0; index < len(path); {
		if len(steps) == 0 && path[index] == '.' && index+1 < len(path) && path[index+1] == '{' {
			index++
		}
		if index >= len(path) || path[index] == '.' {
			return nil, errors.New("empty traversal component")
		}
		start := index
		if path[index] == '{' {
			closeOffset := strings.IndexByte(path[index:], '}')
			if closeOffset < 0 {
				return nil, errors.New("unclosed object projection")
			}
			index += closeOffset + 1
		} else {
			for index < len(path) && path[index] != '.' {
				if path[index] == '{' || path[index] == '}' {
					return nil, errors.New("malformed object projection")
				}
				index++
			}
		}
		step := path[start:index]
		if strings.HasPrefix(step, "{") {
			selectors, err := validateProjectionSelectors(step[1 : len(step)-1])
			if err != nil {
				return nil, err
			}
			for _, selector := range selectors {
				if _, err := parseTraversal(selector); err != nil {
					return nil, err
				}
			}
		}
		if step != ":" && strings.Contains(step, ":") {
			if _, err := sliceIndices(0, step); err != nil {
				return nil, err
			}
		}
		steps = append(steps, step)
		if len(steps) > maxTraversalSteps {
			return nil, errors.New("traversal has too many components")
		}
		if index < len(path) {
			if path[index] != '.' {
				return nil, errors.New("malformed traversal path")
			}
			index++
			if index == len(path) {
				return nil, errors.New("empty traversal component")
			}
		}
	}
	return steps, nil
}

func traverseSteps(current value.Value, steps []string, budget *traversalBudget) (value.Value, error) {
	for position, part := range steps {
		if part == ":" {
			if position+1 == len(steps) {
				switch current.Kind() {
				case value.KindList:
					items, _ := current.ListValue()
					return traverseList(items, part, budget)
				case value.KindString:
					text, _ := current.StringValue()
					return traverseString(text, part, budget)
				case value.KindMissing, value.KindNull:
					return value.Missing(), nil
				default:
					return value.Missing(), fmt.Errorf("cannot slice %s", current.Kind())
				}
			}
			items, ok := current.ListValue()
			if !ok {
				if current.IsMissing() || current.IsNull() {
					return value.Missing(), nil
				}
				return value.Missing(), fmt.Errorf("cannot map through %s", current.Kind())
			}
			if len(items) > maxTraversalItems {
				return value.Missing(), errTraversalBudget
			}
			mapped := make([]value.Value, 0, len(items))
			for _, item := range items {
				selected, err := traverseSteps(item, steps[position+1:], budget)
				if err != nil {
					if errors.Is(err, errTraversalBudget) {
						return value.Missing(), err
					}
					continue
				}
				if !selected.IsMissing() && !selected.IsNull() {
					if err := budget.consume(1); err != nil {
						return value.Missing(), err
					}
					mapped = append(mapped, selected)
				}
			}
			if len(mapped) == 0 {
				return value.Missing(), nil
			}
			return value.List(mapped...), nil
		}
		if strings.HasPrefix(part, "{") || strings.HasSuffix(part, "}") {
			if len(part) < 2 || part[0] != '{' || part[len(part)-1] != '}' {
				return value.Missing(), errors.New("malformed object projection")
			}
			projected, err := project(current, part[1:len(part)-1], budget)
			if err != nil {
				return value.Missing(), err
			}
			current = projected
			continue
		}
		switch current.Kind() {
		case value.KindObject:
			object, _ := current.Object()
			current = object.Lookup(part)
		case value.KindList:
			items, _ := current.ListValue()
			selected, err := traverseList(items, part, budget)
			if err != nil {
				return value.Missing(), err
			}
			current = selected
		case value.KindString:
			text, _ := current.StringValue()
			selected, err := traverseString(text, part, budget)
			if err != nil {
				return value.Missing(), err
			}
			current = selected
		case value.KindMissing, value.KindNull:
			return value.Missing(), nil
		default:
			return value.Missing(), fmt.Errorf("cannot traverse %q through %s", part, current.Kind())
		}
	}
	return current, nil
}

func traverseList(items []value.Value, part string, budget *traversalBudget) (value.Value, error) {
	if strings.Contains(part, ":") {
		indices, err := sliceIndices(len(items), part)
		if err != nil {
			return value.Missing(), err
		}
		if len(indices) > maxTraversalItems {
			return value.Missing(), errTraversalBudget
		}
		if err := budget.consume(len(indices)); err != nil {
			return value.Missing(), err
		}
		selected := make([]value.Value, len(indices))
		for index, source := range indices {
			selected[index] = items[source]
		}
		return value.List(selected...), nil
	}
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
	return items[index], nil
}

func traverseString(text, part string, budget *traversalBudget) (value.Value, error) {
	if len(text) > maxScalarBytes {
		return value.Missing(), errTraversalBudget
	}
	runes := []rune(text)
	if strings.Contains(part, ":") {
		indices, err := sliceIndices(len(runes), part)
		if err != nil {
			return value.Missing(), err
		}
		if len(indices) > maxTraversalItems {
			return value.Missing(), errTraversalBudget
		}
		if err := budget.consume(len(indices)); err != nil {
			return value.Missing(), err
		}
		selected := make([]rune, len(indices))
		for index, source := range indices {
			selected[index] = runes[source]
		}
		return value.String(string(selected)), nil
	}
	index, err := strconv.Atoi(part)
	if err != nil {
		return value.Missing(), fmt.Errorf("string index %q is not an integer", part)
	}
	if index < 0 {
		index += len(runes)
	}
	if index < 0 || index >= len(runes) {
		return value.Missing(), nil
	}
	return value.String(string(runes[index])), nil
}

func sliceIndices(length int, expression string) ([]int, error) {
	parts := strings.Split(expression, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, fmt.Errorf("invalid slice %q", expression)
	}
	values := [3]int{}
	present := [3]bool{}
	for index, part := range parts {
		if part == "" {
			continue
		}
		parsed, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid slice %q", expression)
		}
		values[index], present[index] = parsed, true
	}
	step := 1
	if len(parts) == 3 && present[2] {
		step = values[2]
	}
	if step == 0 {
		return nil, errors.New("slice step must not be zero")
	}
	start, stop := 0, length
	if step < 0 {
		start, stop = length-1, -1
	}
	if present[0] {
		start = values[0]
		if start < 0 {
			start += length
		}
	}
	if present[1] {
		stop = values[1]
		if stop < 0 {
			stop += length
		}
	}
	if step > 0 {
		start = max(0, min(start, length))
		stop = max(0, min(stop, length))
	} else {
		start = max(-1, min(start, length-1))
		stop = max(-1, min(stop, length-1))
	}
	count := 0
	if step > 0 && start < stop {
		count = (stop-start-1)/step + 1
	} else if step < 0 && start > stop {
		distance := uint(start - stop - 1)
		stride := uint(-(step + 1)) + 1
		count = int(distance/stride) + 1
	}
	if count > maxTraversalItems {
		return nil, errTraversalBudget
	}
	indices := make([]int, 0, count)
	for index, position := start, 0; position < count; position++ {
		indices = append(indices, index)
		if position+1 < count {
			index += step
		}
	}
	return indices, nil
}

func project(input value.Value, selectors string, budget *traversalBudget) (value.Value, error) {
	if input.Kind() != value.KindObject {
		return value.Missing(), fmt.Errorf("cannot project through %s", input.Kind())
	}
	fields, err := validateProjectionSelectors(selectors)
	if err != nil {
		return value.Missing(), err
	}
	projected := value.NewObject()
	for _, selector := range fields {
		steps, err := parseTraversal(selector)
		if err != nil {
			return value.Missing(), err
		}
		selected, err := traverseSteps(input, steps, budget)
		if err != nil {
			if errors.Is(err, errTraversalBudget) {
				return value.Missing(), err
			}
			selected = value.Missing()
		}
		if selected.IsNull() {
			selected = value.Missing()
		} else if object, ok := selected.Object(); ok && object.Len() == 0 {
			selected = value.Missing()
		}
		if err := budget.consume(1); err != nil {
			return value.Missing(), err
		}
		projected.Set(selector, selected)
	}
	return value.ObjectValue(projected), nil
}

func validateProjectionSelectors(selectors string) ([]string, error) {
	if selectors == "" {
		return nil, errors.New("empty object projection")
	}
	fields := strings.Split(selectors, ",")
	if len(fields) > maxProjectionFields {
		return nil, errors.New("object projection has too many fields")
	}
	for _, selector := range fields {
		if selector == "" || strings.ContainsAny(selector, "{}|&>") {
			return nil, fmt.Errorf("invalid object projection field %q", selector)
		}
	}
	return fields, nil
}

func formatValue(input value.Value, spec string) (string, error) {
	if !validFormatSpec(spec) {
		return "", fmt.Errorf("format width or precision exceeds limit")
	}
	conversion := spec[len(spec)-1]
	format := "%" + spec
	switch conversion {
	case 's':
		raw, err := scalarString(input)
		if err != nil {
			return "", err
		}
		return boundedFormatted(fmt.Sprintf(format, raw))
	case 'd':
		integer, ok := input.Int()
		if !ok {
			if floating, floatOK := input.Float(); floatOK {
				limit := math.Ldexp(1, 63)
				if !isFinite(floating) || floating < -limit || floating >= limit {
					return "", errors.New("float is outside int64 range")
				}
				integer, ok = int64(floating), true
			}
		}
		if !ok {
			return "", fmt.Errorf("kind %s is not numeric", input.Kind())
		}
		return boundedFormatted(fmt.Sprintf(format, integer))
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
		return boundedFormatted(fmt.Sprintf(format, floating))
	case 'l':
		return formatListConversion(input, spec)
	case 'h':
		raw, err := conversionScalarString(input)
		if err != nil {
			return "", err
		}
		escaped, err := escapeHTMLBounded(raw)
		if err != nil {
			return "", err
		}
		return formatRuneString(escaped, spec)
	case 'U':
		return formatUnicodeConversion(input, spec)
	case 'D':
		return formatDecimalConversion(input, spec)
	case 'j':
		if _, ok := estimateJSON(input, maxRenderedBytes); !ok {
			return "", errors.New("JSON output exceeds size limit")
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return "", fmt.Errorf("encode JSON: %w", err)
		}
		encoded, err = normalizeJSONEncoding(encoded, strings.Contains(spec, "+"))
		if err != nil {
			return "", err
		}
		if strings.Contains(spec, "#") {
			return formatPrettyJSON(encoded)
		}
		return formatCompactJSON(encoded)
	case 'B':
		return formatBytesConversion(input, spec)
	case 'c':
		return formatFirstRuneConversion(input, spec)
	case 'q':
		return formatShellQuoteConversion(input, spec)
	case 'S':
		return formatSanitizeConversion(input, spec)
	case 'r', 'a':
		return formatReprConversion(input, spec, conversion == 'a')
	default:
		return "", fmt.Errorf("unsupported conversion %q", conversion)
	}
}

func validFormatSpec(spec string) bool {
	if !formatSpecPattern.MatchString(spec) {
		return false
	}
	if spec[len(spec)-1] == 'j' {
		return true
	}
	body := spec[:len(spec)-1]
	body = strings.TrimLeft(body, "-+0 #")
	width := body
	precision := ""
	if before, after, found := strings.Cut(body, "."); found {
		width, precision = before, after
	}
	if width != "" {
		parsed, err := strconv.Atoi(width)
		if err != nil || parsed > maxFormatWidth {
			return false
		}
	}
	if precision != "" {
		parsed, err := strconv.Atoi(precision)
		if err != nil || parsed > maxFormatPrecision {
			return false
		}
	}
	return true
}

type formatOptions struct {
	flags        string
	width        int
	precision    int
	hasWidth     bool
	hasPrecision bool
}

func parseFormatOptions(spec string) (formatOptions, error) {
	var options formatOptions
	body := spec[:len(spec)-1]
	index := 0
	for index < len(body) && strings.ContainsRune("-+0 #", rune(body[index])) {
		options.flags += body[index : index+1]
		index++
	}
	widthStart := index
	for index < len(body) && body[index] >= '0' && body[index] <= '9' {
		index++
	}
	if index > widthStart {
		width, err := strconv.Atoi(body[widthStart:index])
		if err != nil || width > maxFormatWidth {
			return formatOptions{}, errors.New("format width exceeds limit")
		}
		options.width, options.hasWidth = width, true
	}
	if index < len(body) {
		if body[index] != '.' {
			return formatOptions{}, errors.New("invalid format options")
		}
		index++
		if index == len(body) {
			return formatOptions{}, errors.New("missing format precision")
		}
		precision, err := strconv.Atoi(body[index:])
		if err != nil || precision > maxFormatPrecision {
			return formatOptions{}, errors.New("format precision exceeds limit")
		}
		options.precision, options.hasPrecision = precision, true
		index = len(body)
	}
	if index != len(body) {
		return formatOptions{}, errors.New("invalid format options")
	}
	return options, nil
}

func formatRuneString(text, spec string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("conversion input is not valid UTF-8")
	}
	options, err := parseFormatOptions(spec)
	if err != nil {
		return "", err
	}
	runes := []rune(text)
	if options.hasPrecision && len(runes) > options.precision {
		runes = runes[:options.precision]
		text = string(runes)
	}
	if options.hasWidth && len(runes) < options.width {
		padding := strings.Repeat(" ", options.width-len(runes))
		if strings.Contains(options.flags, "-") {
			text += padding
		} else {
			text = padding + text
		}
	}
	return boundedFormatted(text)
}

func formatListConversion(input value.Value, spec string) (string, error) {
	items, isList := input.ListValue()
	if !isList {
		items = []value.Value{input}
	}
	if len(items) > maxTraversalItems {
		return "", errors.New("list conversion exceeds item limit")
	}
	delimiter := ", "
	if strings.Contains(spec, "#") {
		delimiter = "\n"
	}
	var output strings.Builder
	for index, item := range items {
		if index != 0 {
			if err := appendBounded(&output, delimiter); err != nil {
				return "", err
			}
		}
		rendered, err := conversionScalarString(item)
		if err != nil {
			return "", fmt.Errorf("list element %d: %w", index, err)
		}
		if err := appendBounded(&output, rendered); err != nil {
			return "", err
		}
	}
	return formatRuneString(output.String(), spec)
}

// conversionScalarString follows Python str for the scalar kinds accepted by
// yt-dlp's l and h conversions without changing the established s conversion.
func conversionScalarString(input value.Value) (string, error) {
	switch input.Kind() {
	case value.KindMissing:
		return "NA", nil
	case value.KindNull:
		return "None", nil
	case value.KindBool:
		boolean, _ := input.Bool()
		if boolean {
			return "True", nil
		}
		return "False", nil
	case value.KindFloat:
		floating, _ := input.Float()
		switch {
		case math.IsNaN(floating):
			return "nan", nil
		case math.IsInf(floating, 1):
			return "inf", nil
		case math.IsInf(floating, -1):
			return "-inf", nil
		}
		rendered := strconv.FormatFloat(floating, 'g', -1, 64)
		if !strings.ContainsAny(rendered, ".eE") {
			rendered += ".0"
		}
		return rendered, nil
	default:
		return scalarString(input)
	}
}

func escapeHTMLBounded(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("HTML conversion input is not valid UTF-8")
	}
	var output strings.Builder
	for _, character := range text {
		replacement := ""
		switch character {
		case '&':
			replacement = "&amp;"
		case '<':
			replacement = "&lt;"
		case '>':
			replacement = "&gt;"
		case '"':
			replacement = "&quot;"
		case '\'':
			replacement = "&#39;"
		default:
			replacement = string(character)
		}
		if err := appendBounded(&output, replacement); err != nil {
			return "", err
		}
	}
	return output.String(), nil
}

func formatUnicodeConversion(input value.Value, spec string) (string, error) {
	text, ok := input.StringValue()
	if !ok {
		return "", fmt.Errorf("Unicode normalization requires string, got %s", input.Kind())
	}
	if len(text) > maxScalarBytes {
		return "", errors.New("Unicode input exceeds size limit")
	}
	if !utf8.ValidString(text) {
		return "", errors.New("Unicode normalization input is not valid UTF-8")
	}
	form := norm.NFC
	compatibility := strings.Contains(spec, "+")
	decomposed := strings.Contains(spec, "#")
	switch {
	case compatibility && decomposed:
		form = norm.NFKD
	case compatibility:
		form = norm.NFKC
	case decomposed:
		form = norm.NFD
	}
	normalized := form.String(text)
	if len(normalized) > maxRenderedBytes {
		return "", errors.New("normalized value exceeds size limit")
	}
	return formatRuneString(normalized, spec)
}

func formatDecimalConversion(input value.Value, spec string) (string, error) {
	number, err := decimalNumber(input)
	if err != nil {
		return "", err
	}
	if number < 0 {
		return "", errors.New("decimal suffix requires a non-negative number")
	}
	options, err := parseFormatOptions(spec)
	if err != nil {
		return "", err
	}
	factor := float64(1000)
	if strings.Contains(options.flags, "#") {
		factor = 1024
	}
	const suffixes = "kMGTPEZY"
	exponent := 0
	scaled := number
	for scaled >= factor && exponent < len(suffixes) {
		scaled /= factor
		exponent++
	}
	suffix := ""
	if exponent != 0 {
		suffix = suffixes[exponent-1 : exponent]
		if factor == 1024 {
			if suffix == "k" {
				suffix = "Ki"
			} else {
				suffix += "i"
			}
		}
	}
	format := "%"
	if strings.Contains(options.flags, "-") {
		format += "-"
	} else if strings.Contains(options.flags, "0") {
		format += "0"
	}
	if strings.Contains(options.flags, "+") {
		format += "+"
	} else if strings.Contains(options.flags, " ") {
		format += " "
	}
	if options.hasWidth {
		format += strconv.Itoa(options.width)
	}
	var numeric string
	if options.hasPrecision {
		format += "." + strconv.Itoa(options.precision) + "f"
		numeric = fmt.Sprintf(format, scaled)
	} else {
		limit := math.Ldexp(1, 63)
		if scaled < -limit || scaled >= limit {
			return "", errors.New("decimal suffix value is outside int64 range")
		}
		format += "d"
		numeric = fmt.Sprintf(format, int64(scaled))
	}
	return boundedFormatted(numeric + suffix)
}

func decimalNumber(input value.Value) (float64, error) {
	var number float64
	switch input.Kind() {
	case value.KindInt:
		integer, _ := input.Int()
		number = float64(integer)
	case value.KindFloat:
		number, _ = input.Float()
	case value.KindString:
		text, _ := input.StringValue()
		if len(text) > maxScalarBytes {
			return 0, errors.New("decimal input exceeds size limit")
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, errors.New("decimal suffix requires a numeric value")
		}
		number = parsed
	default:
		return 0, fmt.Errorf("decimal suffix requires numeric input, got %s", input.Kind())
	}
	if !isFinite(number) {
		return 0, errors.New("decimal suffix requires a finite number")
	}
	return number, nil
}

func normalizeJSONEncoding(encoded []byte, rawUnicode bool) ([]byte, error) {
	var output strings.Builder
	inString := false
	for index := 0; index < len(encoded); index++ {
		character := encoded[index]
		if character == '"' {
			inString = !inString
			if err := appendBounded(&output, `"`); err != nil {
				return nil, err
			}
			continue
		}
		if !inString {
			if err := appendBounded(&output, string(character)); err != nil {
				return nil, err
			}
			continue
		}
		if character == '\\' {
			if index+5 < len(encoded) {
				escape := string(encoded[index : index+6])
				switch escape {
				case `\u003c`:
					if err := appendBounded(&output, "<"); err != nil {
						return nil, err
					}
					index += 5
					continue
				case `\u003e`:
					if err := appendBounded(&output, ">"); err != nil {
						return nil, err
					}
					index += 5
					continue
				case `\u0026`:
					if err := appendBounded(&output, "&"); err != nil {
						return nil, err
					}
					index += 5
					continue
				case `\u2028`, `\u2029`:
					if rawUnicode {
						decoded := '\u2028'
						if escape == `\u2029` {
							decoded = '\u2029'
						}
						if err := appendBounded(&output, string(decoded)); err != nil {
							return nil, err
						}
						index += 5
						continue
					}
				}
			}
			if index+1 < len(encoded) {
				if err := appendBounded(&output, string(encoded[index:index+2])); err != nil {
					return nil, err
				}
				index++
				continue
			}
		}
		if character < utf8.RuneSelf {
			if err := appendBounded(&output, string(character)); err != nil {
				return nil, err
			}
			continue
		}
		decoded, size := utf8.DecodeRune(encoded[index:])
		if decoded == utf8.RuneError && size == 1 {
			return nil, errors.New("invalid UTF-8 in JSON output")
		}
		if rawUnicode {
			if err := appendBounded(&output, string(encoded[index:index+size])); err != nil {
				return nil, err
			}
			index += size - 1
			continue
		}
		if decoded <= 0xffff {
			if err := appendBounded(&output, fmt.Sprintf(`\u%04x`, decoded)); err != nil {
				return nil, err
			}
		} else {
			decoded -= 0x10000
			high := 0xd800 + decoded>>10
			low := 0xdc00 + decoded&0x3ff
			if err := appendBounded(&output, fmt.Sprintf(`\u%04x\u%04x`, high, low)); err != nil {
				return nil, err
			}
		}
		index += size - 1
	}
	return []byte(output.String()), nil
}

func formatCompactJSON(encoded []byte) (string, error) {
	var output strings.Builder
	inString, escaped := false, false
	for index := 0; index < len(encoded); index++ {
		character := encoded[index]
		if err := appendBounded(&output, string(encoded[index:index+1])); err != nil {
			return "", err
		}
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
		} else if character == ',' || character == ':' {
			if err := appendBounded(&output, " "); err != nil {
				return "", err
			}
		}
	}
	return output.String(), nil
}

func formatPrettyJSON(encoded []byte) (string, error) {
	var output strings.Builder
	depth := 0
	inString, escaped := false, false
	indent := func(level int) error {
		return appendBounded(&output, strings.Repeat(" ", level*4))
	}
	for index := 0; index < len(encoded); index++ {
		character := encoded[index]
		if inString {
			if err := appendBounded(&output, string(encoded[index:index+1])); err != nil {
				return "", err
			}
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
			if err := appendBounded(&output, `"`); err != nil {
				return "", err
			}
		case '{', '[':
			depth++
			if depth > maxJSONDepth {
				return "", errors.New("JSON nesting exceeds size limit")
			}
			if err := appendBounded(&output, string(character)); err != nil {
				return "", err
			}
			next := byte('}')
			if character == '[' {
				next = ']'
			}
			if index+1 < len(encoded) && encoded[index+1] != next {
				if err := appendBounded(&output, "\n"); err != nil {
					return "", err
				}
				if err := indent(depth); err != nil {
					return "", err
				}
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return "", errors.New("invalid JSON nesting")
			}
			if index > 0 && encoded[index-1] != '{' && encoded[index-1] != '[' {
				if err := appendBounded(&output, "\n"); err != nil {
					return "", err
				}
				if err := indent(depth); err != nil {
					return "", err
				}
			}
			if err := appendBounded(&output, string(character)); err != nil {
				return "", err
			}
		case ',':
			if err := appendBounded(&output, ",\n"); err != nil {
				return "", err
			}
			if err := indent(depth); err != nil {
				return "", err
			}
		case ':':
			if err := appendBounded(&output, ": "); err != nil {
				return "", err
			}
		default:
			if err := appendBounded(&output, string(character)); err != nil {
				return "", err
			}
		}
	}
	return output.String(), nil
}

// c conversion: a present/truthy value becomes the first Unicode code
// point of its Python-like str(value); false, zero, empty string and
// other falsy values still go through the string-format path. Missing
// or None bypasses the conversion entirely and uses the existing
// default/NA string rendering.
func formatFirstRuneConversion(input value.Value, spec string) (string, error) {
	if input.IsMissing() || input.IsNull() {
		raw, err := scalarString(input)
		if err != nil {
			return "", err
		}
		return formatRuneString(raw, spec)
	}
	// Mirror Python's "if value:" semantics. A present null is already
	// handled above, so a present value here is considered truthy.
	body, err := conversionScalarString(input)
	if err != nil {
		return "", err
	}
	if body == "" {
		// Empty string is falsy; default/replacement are not applied
		// because the value itself was not missing.
		raw, err := scalarString(input)
		if err != nil {
			return "", err
		}
		return formatRuneString(raw, spec)
	}
	// Numeric 0, Bool False, and Float 0.0 are falsy in Python and
	// therefore bypassed to the string-format path. They all serialize
	// to non-empty strings via conversionScalarString, so check the
	// raw numeric truthiness here.
	switch input.Kind() {
	case value.KindBool:
		flag, _ := input.Bool()
		if !flag {
			raw, _ := conversionScalarString(input)
			return formatRuneString(raw, spec)
		}
	case value.KindInt:
		integer, _ := input.Int()
		if integer == 0 {
			raw, _ := conversionScalarString(input)
			return formatRuneString(raw, spec)
		}
	case value.KindFloat:
		floating, _ := input.Float()
		if floating == 0 {
			raw, _ := conversionScalarString(input)
			return formatRuneString(raw, spec)
		}
	}
	if !utf8.ValidString(body) {
		return "", errors.New("c conversion input is not valid UTF-8")
	}
	decoded, _ := utf8.DecodeRuneInString(body)
	return formatRuneString(string(decoded), spec)
}

// B conversion: Python-style %s on the UTF-8 byte representation of
// str(value), with width/precision measured in bytes. Mirrors the
// pinned yt-dlp path:
//
//	f"%{str_fmt}".encode() % str(value).encode()
//	value.decode("utf-8", "ignore")
//
// Precision truncates bytes first; width padding is byte-based and may
// split a multibyte rune; the final decode with "ignore" discards any
// invalid trailing fragments.
func formatBytesConversion(input value.Value, spec string) (string, error) {
	if input.IsMissing() || input.IsNull() {
		raw, err := scalarString(input)
		if err != nil {
			return "", err
		}
		return boundedFormatted(raw)
	}
	body, err := conversionScalarString(input)
	if err != nil {
		return "", err
	}
	if len(body) > maxScalarBytes {
		return "", errors.New("bytes conversion input exceeds size limit")
	}
	encoded := []byte(body)
	options, err := parseFormatOptions(spec)
	if err != nil {
		return "", err
	}
	if options.hasPrecision && options.precision < len(encoded) {
		encoded = encoded[:options.precision]
	}
	if options.hasWidth && len(encoded) < options.width {
		// For the byte %s format only the "-" flag has any effect;
		// "0", " ", "+", "#" are silently ignored, matching
		// b"%-Ns" / b"%Ns" in CPython. The padding is always ASCII
		// space (0x20) and may split a multibyte rune; the trailing
		// decode-ignore pass will discard any resulting fragment.
		padding := options.width - len(encoded)
		pad := bytes.Repeat([]byte{' '}, padding)
		if strings.Contains(options.flags, "-") {
			encoded = append(encoded, pad...)
		} else {
			encoded = append(pad, encoded...)
		}
	}
	if len(encoded) > maxRenderedBytes {
		return "", errors.New("bytes conversion output exceeds size limit")
	}
	return boundedFormatted(decodeBytesIgnoreInvalid(encoded))
}

// decodeBytesIgnoreInvalid mirrors Python's bytes.decode("utf-8", "ignore"):
// invalid bytes are skipped one at a time and decoding continues, so a
// stray invalid byte embedded between valid runes does not abort the
// rest of the buffer (e.g. bytes([97,255,98]) decodes to "ab"). An
// incomplete leading byte at the end of the buffer is also skipped.
func decodeBytesIgnoreInvalid(encoded []byte) string {
	var output strings.Builder
	for index := 0; index < len(encoded); {
		if encoded[index] < utf8.RuneSelf {
			output.WriteByte(encoded[index])
			index++
			continue
		}
		decoded, size := utf8.DecodeRune(encoded[index:])
		if decoded == utf8.RuneError && size == 1 {
			// Skip the offending byte (a stray invalid byte, a
			// continuation byte at the start, or a leading byte whose
			// continuation chain does not validate) and continue.
			index++
			continue
		}
		output.WriteString(string(decoded))
		index += size
	}
	return output.String()
}

func formatShellQuoteConversion(input value.Value, spec string) (string, error) {
	var arguments []string
	if strings.Contains(spec, "#") {
		if items, ok := input.ListValue(); ok {
			if len(items) > maxTraversalItems {
				return "", errors.New("shell quote list exceeds item limit")
			}
			arguments = make([]string, 0, len(items))
			for _, item := range items {
				rendered, err := pythonString(item)
				if err != nil {
					return "", err
				}
				arguments = append(arguments, rendered)
			}
		}
	}
	if arguments == nil {
		rendered, err := pythonString(input)
		if err != nil {
			return "", err
		}
		arguments = []string{rendered}
	}
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		if runtime.GOOS == "windows" {
			quoted[index] = quoteWindowsShell(argument)
		} else {
			quoted[index] = quotePOSIXShell(argument)
		}
	}
	return formatRuneString(strings.Join(quoted, " "), spec)
}

var (
	posixShellSafe   = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
	windowsShellSafe = regexp.MustCompile(`^[A-Za-z0-9_#$*+\-./:?@\\]+$`)
)

func quotePOSIXShell(text string) string {
	if text != "" && posixShellSafe.MatchString(text) {
		return text
	}
	return "'" + strings.ReplaceAll(text, "'", `'"'"'`) + "'"
}

func quoteWindowsShell(text string) string {
	if windowsShellSafe.MatchString(text) {
		return text
	}
	var escaped strings.Builder
	for index := 0; index < len(text); {
		if text[index] == '\\' {
			start := index
			for index < len(text) && text[index] == '\\' {
				index++
			}
			count := index - start
			escaped.WriteString(strings.Repeat(`\`, count))
			if index == len(text) || text[index] == '"' {
				escaped.WriteString(strings.Repeat(`\`, count))
			}
			continue
		}
		switch text[index] {
		case '"':
			escaped.WriteString(`""`)
		case '\r', '\n':
			escaped.WriteString("%=%")
		case '%':
			escaped.WriteString("%%cd:~,%")
		default:
			escaped.WriteByte(text[index])
		}
		index++
	}
	return `"` + escaped.String() + `"`
}

func formatSanitizeConversion(input value.Value, spec string) (string, error) {
	rendered, err := pythonString(input)
	if err != nil {
		return "", err
	}
	sanitized, err := sanitizeFilename(rendered, strings.Contains(spec, "#"))
	if err != nil {
		return "", err
	}
	return formatRuneString(sanitized, spec)
}

func sanitizeFilename(text string, restricted bool) (string, error) {
	if len(text) > maxScalarBytes {
		return "", errors.New("filename sanitization input exceeds size limit")
	}
	if !utf8.ValidString(text) {
		return "", errors.New("filename sanitization input is not valid UTF-8")
	}
	if text == "" {
		return "", nil
	}
	if restricted {
		text = norm.NFKC.String(text)
	}
	text = replaceTimestampColons(text)
	var output strings.Builder
	for _, character := range text {
		replacement := string(character)
		if restricted {
			if accent, ok := restrictedAccent(character); ok {
				replacement = accent
			} else if strings.ContainsRune(`!&'()[]{}$;`+"`^,#", character) ||
				unicode.IsSpace(character) || character > 127 {
				if unicode.IsMark(character) {
					replacement = ""
				} else {
					replacement = "\x00_"
				}
			} else {
				replacement = sanitizeFilenameCharacter(character, true)
			}
		} else {
			replacement = sanitizeFilenameCharacter(character, false)
		}
		if err := appendBounded(&output, replacement); err != nil {
			return "", err
		}
	}
	result := collapseSanitizeSubstitutes(output.String())
	result = strings.ReplaceAll(result, "\x00", "")
	if result == "" {
		result = "_"
	}
	for strings.Contains(result, "__") {
		result = strings.ReplaceAll(result, "__", "_")
	}
	result = strings.Trim(result, "_")
	if restricted && strings.HasPrefix(result, "-_") {
		result = result[2:]
	}
	if strings.HasPrefix(result, "-") {
		result = "_" + result[1:]
	}
	result = strings.TrimLeft(result, ".")
	if result == "" {
		result = "_"
	}
	return result, nil
}

func sanitizeFilenameCharacter(character rune, restricted bool) string {
	switch {
	case !restricted && character == '\n':
		return "\x00 "
	case !restricted && strings.ContainsRune(`"*:<>?|/\`, character):
		switch character {
		case '/':
			return "⧸"
		case '\\':
			return "⧹"
		default:
			return string(character + 0xfee0)
		}
	case character == '?' || character < 32 || character == 127:
		return ""
	case character == '"':
		if restricted {
			return ""
		}
		return "'"
	case character == ':':
		if restricted {
			return "\x00_\x00-"
		}
		return "\x00 \x00-"
	case strings.ContainsRune(`\/|*<>`, character):
		return "\x00_"
	default:
		return string(character)
	}
}

func replaceTimestampColons(text string) string {
	timestamp := regexp.MustCompile(`[0-9]+(?::[0-9]+)+`)
	return timestamp.ReplaceAllStringFunc(text, func(match string) string {
		return strings.ReplaceAll(match, ":", "_")
	})
}

func collapseSanitizeSubstitutes(text string) string {
	for index := 0; index+3 < len(text); {
		if text[index] == 0 && text[index+2] == 0 && text[index+1] == text[index+3] {
			text = text[:index+2] + text[index+4:]
			continue
		}
		index++
	}
	if strings.HasPrefix(text, "\x00") && len(text) >= 2 {
		text = text[2:]
		for len(text) > 0 {
			if strings.ContainsRune(" _-", rune(text[0])) {
				text = text[1:]
			} else if text[0] == 0 && len(text) >= 2 {
				text = text[2:]
			} else {
				break
			}
		}
	}
	if len(text) >= 2 && text[len(text)-2] == 0 {
		text = text[:len(text)-2]
		for len(text) > 0 {
			if strings.ContainsRune(" _-", rune(text[len(text)-1])) {
				text = text[:len(text)-1]
			} else if len(text) >= 2 && text[len(text)-2] == 0 {
				text = text[:len(text)-2]
			} else {
				break
			}
		}
	}
	return text
}

func restrictedAccent(character rune) (string, bool) {
	const accented = "ÂÃÄÀÁÅÆÇÈÉÊËÌÍÎÏÐÑÒÓÔÕÖŐØŒÙÚÛÜŰÝÞßàáâãäåæçèéêëìíîïðñòóôõöőøœùúûüűýþÿ"
	replacements := []string{"A", "A", "A", "A", "A", "A", "AE", "C", "E", "E", "E", "E", "I", "I", "I", "I", "D", "N", "O", "O", "O", "O", "O", "O", "O", "O", "OE", "U", "U", "U", "U", "U", "Y", "TH", "ss", "a", "a", "a", "a", "a", "a", "ae", "c", "e", "e", "e", "e", "i", "i", "i", "i", "o", "n", "o", "o", "o", "o", "o", "o", "o", "o", "oe", "u", "u", "u", "u", "u", "y", "th", "y"}
	for index, accent := range []rune(accented) {
		if accent == character {
			return replacements[index], true
		}
	}
	return "", false
}

func formatReprConversion(input value.Value, spec string, asciiOnly bool) (string, error) {
	rendered, err := pythonRepr(input, asciiOnly, 0)
	if err != nil {
		return "", err
	}
	return formatRuneString(rendered, spec)
}

func pythonString(input value.Value) (string, error) {
	switch input.Kind() {
	case value.KindList, value.KindObject, value.KindBytes:
		return pythonRepr(input, false, 0)
	default:
		return conversionScalarString(input)
	}
}

func pythonRepr(input value.Value, asciiOnly bool, depth int) (string, error) {
	if depth > maxJSONDepth {
		return "", errors.New("repr nesting exceeds depth limit")
	}
	switch input.Kind() {
	case value.KindMissing:
		return "'NA'", nil
	case value.KindNull:
		return "None", nil
	case value.KindBool:
		return conversionScalarString(input)
	case value.KindInt, value.KindFloat:
		return conversionScalarString(input)
	case value.KindString:
		text, _ := input.StringValue()
		return quotePythonString(text, asciiOnly)
	case value.KindBytes:
		data, _ := input.BytesValue()
		return quotePythonBytes(data), nil
	case value.KindList:
		items, _ := input.ListValue()
		if len(items) > maxTraversalItems {
			return "", errors.New("repr list exceeds item limit")
		}
		parts := make([]string, len(items))
		for index, item := range items {
			part, err := pythonRepr(item, asciiOnly, depth+1)
			if err != nil {
				return "", err
			}
			parts[index] = part
		}
		return boundedFormatted("[" + strings.Join(parts, ", ") + "]")
	case value.KindObject:
		object, _ := input.Object()
		if object.Len() > maxTraversalItems {
			return "", errors.New("repr object exceeds item limit")
		}
		parts := make([]string, 0, object.Len())
		for _, field := range object.Fields() {
			if field.Value.IsMissing() {
				continue
			}
			key, err := quotePythonString(field.Key, asciiOnly)
			if err != nil {
				return "", err
			}
			item, err := pythonRepr(field.Value, asciiOnly, depth+1)
			if err != nil {
				return "", err
			}
			parts = append(parts, key+": "+item)
		}
		return boundedFormatted("{" + strings.Join(parts, ", ") + "}")
	default:
		return "", fmt.Errorf("unsupported repr kind %s", input.Kind())
	}
}

func quotePythonString(text string, asciiOnly bool) (string, error) {
	if len(text) > maxScalarBytes || !utf8.ValidString(text) {
		return "", errors.New("repr string is invalid or exceeds size limit")
	}
	var output strings.Builder
	output.WriteByte('\'')
	for _, character := range text {
		switch character {
		case '\\':
			output.WriteString(`\\`)
		case '\'':
			output.WriteString(`\'`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character < 32 || character == 127 || (asciiOnly && character > 127) {
				writePythonEscape(&output, character)
			} else {
				output.WriteRune(character)
			}
		}
		if output.Len() > maxRenderedBytes {
			return "", errors.New("repr output exceeds size limit")
		}
	}
	output.WriteByte('\'')
	return output.String(), nil
}

func writePythonEscape(output *strings.Builder, character rune) {
	switch {
	case character <= 0xff:
		fmt.Fprintf(output, `\x%02x`, character)
	case character <= 0xffff:
		fmt.Fprintf(output, `\u%04x`, character)
	default:
		fmt.Fprintf(output, `\U%08x`, character)
	}
}

func quotePythonBytes(data []byte) string {
	var output strings.Builder
	output.WriteString("b'")
	for _, character := range data {
		switch character {
		case '\\', '\'':
			output.WriteByte('\\')
			output.WriteByte(character)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character >= 32 && character < 127 {
				output.WriteByte(character)
			} else {
				fmt.Fprintf(&output, `\x%02x`, character)
			}
		}
	}
	output.WriteByte('\'')
	return output.String()
}

func boundedFormatted(text string) (string, error) {
	if len(text) > maxRenderedBytes {
		return "", errors.New("formatted value exceeds size limit")
	}
	return text, nil
}

func estimateJSON(input value.Value, limit int) (int, bool) {
	if limit < 0 {
		return 0, false
	}
	add := func(total, amount int) (int, bool) {
		if amount > limit-total {
			return 0, false
		}
		return total + amount, true
	}
	switch input.Kind() {
	case value.KindMissing, value.KindNull:
		return 4, limit >= 4
	case value.KindBool:
		return 5, limit >= 5
	case value.KindInt, value.KindFloat:
		return 32, limit >= 32
	case value.KindString:
		text, _ := input.StringValue()
		if len(text) > (limit-2)/6 {
			return 0, false
		}
		return 2 + len(text)*6, true
	case value.KindBytes:
		bytes, _ := input.BytesValue()
		if len(bytes) > (limit-2)/2 {
			return 0, false
		}
		return 2 + len(bytes)*2, true
	case value.KindList:
		items, _ := input.ListValue()
		total := 2
		for _, item := range items {
			if total == limit {
				return 0, false
			}
			if total > 1 {
				total++
			}
			size, ok := estimateJSON(item, limit-total)
			if !ok {
				return 0, false
			}
			total, ok = add(total, size)
			if !ok {
				return 0, false
			}
		}
		return total, true
	case value.KindObject:
		object, _ := input.Object()
		total := 2
		for _, field := range object.Fields() {
			if total > 1 {
				total++
			}
			if len(field.Key) > (limit-total-3)/6 {
				return 0, false
			}
			total += 2 + len(field.Key)*6 + 1
			size, ok := estimateJSON(field.Value, limit-total)
			if !ok {
				return 0, false
			}
			total, ok = add(total, size)
			if !ok {
				return 0, false
			}
		}
		return total, true
	default:
		return 0, false
	}
}

func scalarString(input value.Value) (string, error) {
	switch input.Kind() {
	case value.KindMissing, value.KindNull:
		return "NA", nil
	case value.KindString:
		result, _ := input.StringValue()
		if len(result) > maxScalarBytes {
			return "", errors.New("string value exceeds size limit")
		}
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
