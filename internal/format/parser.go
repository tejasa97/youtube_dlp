package format

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	maxASTNodes            = 256
	maxNestingDepth        = 32
	maxCommaOutputs        = 64
	maxCartesianCandidates = 256
)

type selectorParser struct {
	input      string
	pos        int
	nodeCount  int
	depth      int
	mergeTerms int
}

func parseSelectorAST(input string) (*astNode, error) {
	if len(input) > maxSelectorBytes {
		return nil, selectorSyntax(0, len(input), "selector exceeds size limit")
	}
	parser := &selectorParser{input: strings.TrimSpace(input)}
	if parser.input == "" {
		return nil, selectorSyntax(0, len(input), "selector is empty")
	}
	root, err := parser.parseSelection(false, false, false)
	if err != nil {
		return nil, err
	}
	parser.skipSpace()
	if parser.pos < len(parser.input) {
		return nil, selectorSyntax(parser.pos, len(parser.input), fmt.Sprintf("unexpected text %q", parser.input[parser.pos:]))
	}
	if root == nil {
		return nil, selectorSyntax(0, len(parser.input), "selector is empty")
	}
	return root, nil
}

func (parser *selectorParser) parseSelection(insideMerge, insideChoice, insideGroup bool) (*astNode, error) {
	var outputs []astNode
	var current *astNode
	var err error
	for {
		parser.skipSpace()
		if parser.pos >= len(parser.input) {
			break
		}
		character := parser.input[parser.pos]
		if character == ')' {
			if insideGroup || insideMerge || insideChoice {
				break
			}
			return nil, selectorSyntax(parser.pos, parser.pos+1, "unexpected )")
		}
		if insideMerge && (character == '/' || character == ',') {
			break
		}
		if insideChoice && character == ',' {
			break
		}
		switch character {
		case ',':
			if current == nil {
				return nil, selectorSyntax(parser.pos, parser.pos+1, `"," must follow a format selector`)
			}
			outputs = append(outputs, *current)
			if len(outputs) > maxCommaOutputs {
				return nil, selectorSyntax(0, len(parser.input), "too many comma-separated outputs")
			}
			current = nil
			parser.pos++
			continue
		case '/':
			if current == nil {
				return nil, selectorSyntax(parser.pos, parser.pos+1, `"/" must follow a format selector`)
			}
			parser.pos++
			right, err := parser.parseSelection(false, true, false)
			if err != nil {
				return nil, err
			}
			if right == nil {
				return nil, selectorSyntax(parser.pos, parser.pos, "expected a selector")
			}
			merged, err := parser.mergeNodes(astPickFirst, *current, *right)
			if err != nil {
				return nil, err
			}
			current = merged
		case '+':
			if current == nil {
				return nil, selectorSyntax(parser.pos, parser.pos+1, `unexpected "+"`)
			}
			parser.mergeTerms++
			if parser.mergeTerms >= maxMergeTerms {
				return nil, selectorSyntax(parser.pos, parser.pos+1, "too many merge terms")
			}
			parser.pos++
			right, err := parser.parseSelection(true, false, false)
			if err != nil {
				return nil, err
			}
			if right == nil {
				return nil, selectorSyntax(parser.pos, parser.pos, "expected a selector")
			}
			merged, err := parser.mergeNodes(astMerge, *current, *right)
			if err != nil {
				return nil, err
			}
			current = merged
		case '(':
			if current != nil {
				return nil, selectorSyntax(parser.pos, parser.pos+1, `unexpected "("`)
			}
			start := parser.pos
			parser.pos++
			if err := parser.pushDepth(); err != nil {
				return nil, err
			}
			group, err := parser.parseSelection(false, false, true)
			parser.popDepth()
			if err != nil {
				return nil, err
			}
			if group == nil {
				return nil, selectorSyntax(start, start+1, "empty group")
			}
			parser.skipSpace()
			if parser.pos >= len(parser.input) || parser.input[parser.pos] != ')' {
				return nil, selectorSyntax(start, len(parser.input), "unclosed group")
			}
			end := parser.pos + 1
			parser.pos++
			current, err = parser.newGroupNode(start, end, group)
			if err != nil {
				return nil, err
			}
			filters, err := parser.parseAttachedFilters()
			if err != nil {
				return nil, err
			}
			current.filters = append(current.filters, filters...)
			if len(current.filters) > maxTermFilters {
				return nil, selectorSyntax(start, end, "too many filters")
			}
		case '[':
			if current == nil {
				current, err = parser.newFilterOnlyAtom(parser.pos)
				if err != nil {
					return nil, err
				}
			}
			filters, err := parser.parseAttachedFilters()
			if err != nil {
				return nil, err
			}
			current.filters = append(current.filters, filters...)
			if len(current.filters) > maxTermFilters {
				return nil, selectorSyntax(parser.pos, len(parser.input), "too many filters")
			}
		default:
			atom, err := parser.parseAtom()
			if err != nil {
				return nil, err
			}
			current = atom
			filters, err := parser.parseAttachedFilters()
			if err != nil {
				return nil, err
			}
			current.filters = append(current.filters, filters...)
			if len(current.filters) > maxTermFilters {
				return nil, selectorSyntax(atom.span.start, atom.span.end, "too many filters")
			}
		}
	}
	if current != nil {
		outputs = append(outputs, *current)
	}
	if len(outputs) == 0 {
		return nil, nil
	}
	if len(outputs) == 1 {
		return &outputs[0], nil
	}
	if len(outputs) > maxCommaOutputs {
		return nil, selectorSyntax(0, len(parser.input), "too many comma-separated outputs")
	}
	comma, err := parser.newCommaNode(outputs)
	if err != nil {
		return nil, err
	}
	return comma, nil
}

func (parser *selectorParser) parseAtom() (*astNode, error) {
	start := parser.pos
	for parser.pos < len(parser.input) {
		character := parser.input[parser.pos]
		if character == '[' || character == ',' || character == '/' || character == '+' || character == '(' || character == ')' {
			break
		}
		if unicode.IsSpace(rune(character)) {
			break
		}
		parser.pos++
	}
	text := parser.input[start:parser.pos]
	if text == "" {
		return nil, selectorSyntax(start, start+1, "expected atom")
	}
	spec, err := parseAtomSpec(text, start)
	if err != nil {
		return nil, err
	}
	return parser.newAtomNode(spec, start, parser.pos)
}

func (parser *selectorParser) parseAttachedFilters() ([]Filter, error) {
	var filters []Filter
	for {
		parser.skipSpace()
		if parser.pos >= len(parser.input) || parser.input[parser.pos] != '[' {
			return filters, nil
		}
		open := parser.pos
		parser.pos++
		close := strings.IndexByte(parser.input[parser.pos:], ']')
		if close < 0 {
			return nil, selectorSyntax(open, len(parser.input), "unclosed filter")
		}
		filter, err := parseFilter(parser.input[parser.pos:parser.pos+close], open+1)
		if err != nil {
			return nil, err
		}
		filters = append(filters, filter)
		parser.pos += close + 1
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
		return selectorSyntax(parser.pos, len(parser.input), "selector nesting exceeds limit")
	}
	return nil
}

func (parser *selectorParser) popDepth() { parser.depth-- }

func (parser *selectorParser) skipSpace() {
	for parser.pos < len(parser.input) && unicode.IsSpace(rune(parser.input[parser.pos])) {
		parser.pos++
	}
}
