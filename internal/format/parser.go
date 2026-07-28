package format

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxASTNodes            = 256
	maxNestingDepth        = 32
	maxCommaOutputs        = 64
	maxCartesianCandidates = 256
)

type selectorParser struct {
	input     string
	pos       int
	nodeCount int
	depth     int
}

func parseSelectorAST(input string) (*astNode, error) {
	if len(input) > maxSelectorBytes {
		return nil, selectorSyntax(0, len(input), "selector exceeds size limit")
	}
	parser := &selectorParser{input: input}
	parser.skipSpace()
	if parser.pos == len(parser.input) {
		return nil, selectorSyntax(0, len(input), "selector is empty")
	}
	root, err := parser.parseComma(false)
	if err != nil {
		return nil, err
	}
	parser.skipSpace()
	if parser.pos < len(parser.input) {
		end := parser.pos + runeWidth(parser.input[parser.pos:])
		return nil, selectorSyntax(parser.pos, end, fmt.Sprintf("unexpected text %q", parser.input[parser.pos:end]))
	}
	return root, nil
}

// parseComma parses independent outputs. Comma has the lowest precedence.
// A trailing comma is accepted (pinned Python keeps the preceding selector);
// an empty comma branch such as ",best" or "best,," is rejected.
func (parser *selectorParser) parseComma(insideGroup bool) (*astNode, error) {
	first, err := parser.parseChoice(insideGroup)
	if err != nil {
		return nil, err
	}
	outputs := []astNode{*first}
	for {
		parser.skipSpace()
		if parser.atEnd() || insideGroup && parser.peek() == ')' {
			break
		}
		if parser.peek() != ',' {
			break
		}
		operator := parser.pos
		parser.pos++
		parser.skipSpace()
		if parser.atEnd() || insideGroup && parser.peek() == ')' {
			break
		}
		if parser.peek() == ',' {
			return nil, selectorSyntax(operator+1, operator+2, `"," must follow a format selector`)
		}
		right, err := parser.parseChoice(insideGroup)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, *right)
		if len(outputs) > maxCommaOutputs {
			return nil, selectorSyntax(outputs[0].span.start, outputs[len(outputs)-1].span.end, "too many comma-separated outputs")
		}
	}
	if len(outputs) == 1 {
		return &outputs[0], nil
	}
	return parser.newCommaNode(outputs)
}

// parseChoice parses fallback alternatives. Slash binds less tightly than plus.
// A trailing slash is accepted with an empty right-hand side (pinned Python
// builds PICKFIRST(left, [])); consecutive "//" is consumed inside parseAtom as
// a joined direct-ID token instead of reaching this operator path.
func (parser *selectorParser) parseChoice(insideGroup bool) (*astNode, error) {
	current, err := parser.parseMerge(insideGroup)
	if err != nil {
		return nil, err
	}
	alternatives := 1
	for {
		parser.skipSpace()
		if parser.atEnd() || insideGroup && parser.peek() == ')' || parser.peek() != '/' {
			break
		}
		operator := parser.pos
		parser.pos++
		parser.skipSpace()
		if parser.atEnd() || insideGroup && parser.peek() == ')' || parser.peek() == ',' {
			break
		}
		if parser.peek() == '/' {
			return nil, selectorSyntax(operator, operator+1, `"/" must be followed by a format selector`)
		}
		right, err := parser.parseMerge(insideGroup)
		if err != nil {
			return nil, err
		}
		current, err = parser.mergeNodes(astPickFirst, *current, *right)
		if err != nil {
			return nil, err
		}
		alternatives++
		if alternatives > maxAlternatives {
			return nil, selectorSyntax(current.span.start, current.span.end, "too many selector alternatives")
		}
	}
	return current, nil
}

// parseMerge parses tracks that form one output. Plus has the highest binary
// operator precedence.
func (parser *selectorParser) parseMerge(insideGroup bool) (*astNode, error) {
	current, err := parser.parsePrimary(insideGroup)
	if err != nil {
		return nil, err
	}
	terms := 1
	for {
		parser.skipSpace()
		if parser.atEnd() || insideGroup && parser.peek() == ')' || parser.peek() != '+' {
			break
		}
		operator := parser.pos
		parser.pos++
		parser.skipSpace()
		if parser.atEnd() || insideGroup && parser.peek() == ')' || parser.peek() == '+' || parser.peek() == '/' || parser.peek() == ',' {
			return nil, selectorSyntax(operator, operator+1, `"+" must be followed by a format selector`)
		}
		right, err := parser.parsePrimary(insideGroup)
		if err != nil {
			return nil, err
		}
		current, err = parser.mergeNodes(astMerge, *current, *right)
		if err != nil {
			return nil, err
		}
		terms++
		if terms > maxMergeTerms {
			return nil, selectorSyntax(current.span.start, current.span.end, "too many merge terms")
		}
	}
	return current, nil
}

func (parser *selectorParser) parsePrimary(insideGroup bool) (*astNode, error) {
	parser.skipSpace()
	if parser.atEnd() {
		return nil, selectorSyntax(parser.pos, parser.pos, "expected a selector")
	}
	start := parser.pos
	var current *astNode
	var err error
	switch parser.peek() {
	case '(':
		parser.pos++
		if err := parser.pushDepth(); err != nil {
			return nil, err
		}
		parser.skipSpace()
		if !parser.atEnd() && parser.peek() == ')' {
			parser.pos++
			parser.popDepth()
			current, err = parser.newGroupNode(start, parser.pos, nil)
			break
		}
		group, groupErr := parser.parseComma(true)
		parser.popDepth()
		if groupErr != nil {
			return nil, groupErr
		}
		parser.skipSpace()
		if parser.atEnd() {
			return nil, selectorSyntax(start, len(parser.input), "unclosed group")
		}
		if parser.peek() != ')' {
			end := parser.pos + runeWidth(parser.input[parser.pos:])
			return nil, selectorSyntax(parser.pos, end, "expected closing parenthesis")
		}
		parser.pos++
		current, err = parser.newGroupNode(start, parser.pos, group)
	case '[':
		current, err = parser.newFilterOnlyAtom(start)
	case ')':
		if insideGroup {
			return nil, selectorSyntax(start, start+1, "expected a selector")
		}
		return nil, selectorSyntax(start, start+1, "unexpected )")
	case ']', ',':
		return nil, selectorSyntax(start, start+1, fmt.Sprintf("unexpected %q", parser.input[start:start+1]))
	case '/', '+':
		if !parser.hasJoinableOperator() {
			return nil, selectorSyntax(start, start+1, fmt.Sprintf("unexpected %q", parser.input[start:start+1]))
		}
		current, err = parser.parseAtom()
	default:
		current, err = parser.parseAtom()
	}
	if err != nil {
		return nil, err
	}
	filters, err := parser.parseAttachedFilters()
	if err != nil {
		return nil, err
	}
	current.filters = append(current.filters, filters...)
	if len(current.filters) > maxTermFilters {
		return nil, selectorSyntax(current.span.start, parser.pos, "too many filters")
	}
	if len(filters) > 0 {
		current.span.end = parser.pos
	}
	return current, nil
}

// parseAtom accumulates one selector atom while mirroring pinned Python's
// _remove_unused_ops join of NAME, NUMBER, and non-structural OP tokens.
// Whitespace between joined fragments is skipped; structural operators and
// single '/' or '+' remain delimiters. Multi-character Python OP tokens such
// as "//", "/=", and "+=" are retained inside the atom text.
func (parser *selectorParser) parseAtom() (*astNode, error) {
	start := parser.pos
	var text strings.Builder
	end := start
	for parser.pos < len(parser.input) {
		character := parser.input[parser.pos]
		if joined, ok := parser.joinableOperator(); ok {
			text.WriteString(joined)
			end = parser.pos
			continue
		}
		if isSelectorDelimiter(character) {
			break
		}
		r, width := utf8.DecodeRuneInString(parser.input[parser.pos:])
		if r == utf8.RuneError && width == 1 {
			return nil, selectorSyntax(parser.pos, parser.pos+1, "invalid UTF-8 in selector")
		}
		if unicode.IsSpace(r) {
			parser.pos += width
			continue
		}
		text.WriteString(parser.input[parser.pos : parser.pos+width])
		parser.pos += width
		end = parser.pos
	}
	atomText := text.String()
	if atomText == "" {
		return nil, selectorSyntax(start, start+runeWidth(parser.input[start:]), "expected atom")
	}
	spec, err := parseAtomSpec(atomText, start)
	if err != nil {
		return nil, err
	}
	return parser.newAtomNode(spec, start, end)
}

func (parser *selectorParser) hasJoinableOperator() bool {
	if parser.pos+1 >= len(parser.input) {
		return false
	}
	switch parser.input[parser.pos] {
	case '/':
		next := parser.input[parser.pos+1]
		return next == '/' || next == '='
	case '+':
		return parser.input[parser.pos+1] == '='
	default:
		return false
	}
}

// joinableOperator consumes multi-character Python OP tokens that are not
// structural format-selector operators and therefore join into the surrounding
// atom under _remove_unused_ops.
func (parser *selectorParser) joinableOperator() (string, bool) {
	if !parser.hasJoinableOperator() {
		return "", false
	}
	joined := parser.input[parser.pos : parser.pos+2]
	parser.pos += 2
	return joined, true
}

func (parser *selectorParser) parseAttachedFilters() ([]Filter, error) {
	var filters []Filter
	for {
		parser.skipSpace()
		if parser.atEnd() || parser.peek() != '[' {
			return filters, nil
		}
		open := parser.pos
		parser.pos++
		contentStart := parser.pos
		quote := byte(0)
		quoteStart := -1
		escaped := false
		closed := false
		for parser.pos < len(parser.input) {
			character := parser.input[parser.pos]
			if escaped {
				escaped = false
				parser.pos++
				continue
			}
			if character == '\\' {
				escaped = true
				parser.pos++
				continue
			}
			if quote != 0 {
				if character == quote {
					quote = 0
				}
				parser.pos++
				continue
			}
			if character == '\'' || character == '"' {
				quote = character
				quoteStart = parser.pos
				parser.pos++
				continue
			}
			if character == ']' {
				contentEnd := parser.pos
				filter, err := parseFilter(parser.input[contentStart:contentEnd], contentStart)
				if err != nil {
					return nil, err
				}
				filters = append(filters, filter)
				parser.pos++
				closed = true
				break
			}
			parser.pos++
		}
		if !closed {
			if quote != 0 {
				return nil, selectorSyntax(quoteStart, len(parser.input), "unclosed quoted filter value")
			}
			return nil, selectorSyntax(open, len(parser.input), "unclosed filter")
		}
	}
}

func (parser *selectorParser) newFilterOnlyAtom(pos int) (*astNode, error) {
	if err := parser.incNode(); err != nil {
		return nil, err
	}
	return &astNode{
		kind: astAtom,
		atom: atomSpec{kind: atomFilterOnly},
		span: span{start: pos, end: pos},
	}, nil
}

func (parser *selectorParser) newAtomNode(spec atomSpec, start, end int) (*astNode, error) {
	if err := parser.incNode(); err != nil {
		return nil, err
	}
	return &astNode{kind: astAtom, atom: spec, span: span{start: start, end: end}}, nil
}

func (parser *selectorParser) newGroupNode(start, end int, child *astNode) (*astNode, error) {
	if err := parser.incNode(); err != nil {
		return nil, err
	}
	node := &astNode{kind: astGroup, span: span{start: start, end: end}}
	if child != nil {
		node.children = []astNode{*child}
	}
	return node, nil
}

func (parser *selectorParser) mergeNodes(kind astKind, left, right astNode) (*astNode, error) {
	if err := parser.incNode(); err != nil {
		return nil, err
	}
	return &astNode{
		kind:     kind,
		children: []astNode{left, right},
		span:     span{start: left.span.start, end: right.span.end},
	}, nil
}

func (parser *selectorParser) newCommaNode(outputs []astNode) (*astNode, error) {
	if err := parser.incNode(); err != nil {
		return nil, err
	}
	return &astNode{
		kind:     astComma,
		children: outputs,
		span:     span{start: outputs[0].span.start, end: outputs[len(outputs)-1].span.end},
	}, nil
}

func (parser *selectorParser) incNode() error {
	parser.nodeCount++
	if parser.nodeCount > maxASTNodes {
		return selectorSyntax(0, len(parser.input), "selector AST exceeds node limit")
	}
	return nil
}

func (parser *selectorParser) pushDepth() error {
	parser.depth++
	if parser.depth > maxNestingDepth {
		return selectorSyntax(parser.pos-1, parser.pos, "selector nesting exceeds limit")
	}
	return nil
}

func (parser *selectorParser) popDepth() { parser.depth-- }

func (parser *selectorParser) skipSpace() {
	for parser.pos < len(parser.input) {
		r, width := utf8.DecodeRuneInString(parser.input[parser.pos:])
		if !unicode.IsSpace(r) {
			return
		}
		parser.pos += width
	}
}

func (parser *selectorParser) atEnd() bool { return parser.pos >= len(parser.input) }

func (parser *selectorParser) peek() byte { return parser.input[parser.pos] }

func isSelectorDelimiter(character byte) bool {
	switch character {
	case '[', ']', ',', '/', '+', '(', ')':
		return true
	default:
		return false
	}
}

func runeWidth(input string) int {
	_, width := utf8.DecodeRuneInString(input)
	if width == 0 {
		return 1
	}
	return width
}
