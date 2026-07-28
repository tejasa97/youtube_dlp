package format

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	pythonWordClassBody  = `\p{L}\p{Nd}\p{Nl}\p{No}_`
	pythonSpaceClassBody = `\t\n\r\f\v\x{1c}-\x{1f}\x{85}\p{Zs}\p{Zl}\p{Zp}`
	pythonASCIIWordBody  = `A-Za-z0-9_`
	pythonASCIISpaceBody = `\t\n\r\f\v`
	pythonASCIIDigitBody = `0-9`
)

type regexTranslator struct {
	src                  string
	pos                  int
	out                  strings.Builder
	groupCount           int
	nesting              int
	maxNesting           int
	names                map[string]string // python name -> regexp2 name
	definedAt            map[string]int    // python name -> group number
	groupWidth           []regexWidth      // index by group number (1-based)
	ascii                bool
	ignoreCase           bool
	multiline            bool
	dotAll               bool
	verbose              bool
	typeFlag             byte // explicit global 'a' or 'u'
	emitEngineIgnoreCase bool
	sawConsuming         bool // any consuming atom; used for global-flag placement
	groupStack           []regexGroupFrame
}

type regexGroupFrame struct {
	ascii                bool
	ignoreCase           bool
	multiline            bool
	dotAll               bool
	verbose              bool
	emitEngineIgnoreCase bool
	capturing            bool
	groupNumber          int
	sourceStart          int // content start in tr.src
}

type regexWidth struct {
	min, max int
	ok       bool // false => unknown/variable
}

func translatePythonRegex(pattern string) (string, error) {
	pattern, err := rewritePythonPossessiveQuantifiers(pattern)
	if err != nil {
		return "", err
	}
	tr := &regexTranslator{
		src:        pattern,
		names:      make(map[string]string),
		definedAt:  make(map[string]int),
		groupWidth: []regexWidth{{}}, // 1-based
	}
	if err := tr.translate(); err != nil {
		return "", err
	}
	if tr.maxNesting > maxRegexNesting {
		return "", fmt.Errorf("regular expression nesting exceeds limit")
	}
	if tr.groupCount > maxRegexGroups {
		return "", fmt.Errorf("regular expression capture groups exceed limit")
	}
	return tr.out.String(), nil
}

// regexp2 lacks Python 3.11+'s possessive quantifier syntax. Python defines
// x*+, x++ and x{m,n}+ as the corresponding quantifier wrapped atomically.
func rewritePythonPossessiveQuantifiers(pattern string) (string, error) {
	out := make([]byte, 0, len(pattern))
	groupStarts := []int{}
	lastAtomStart := -1
	for pos := 0; pos < len(pattern); {
		start := len(out)
		switch pattern[pos] {
		case '\\':
			end := pos + 2
			if end > len(pattern) {
				return "", fmt.Errorf("trailing backslash")
			}
			if pattern[pos+1] == 'N' && end < len(pattern) && pattern[end] == '{' {
				close := strings.IndexByte(pattern[end+1:], '}')
				if close < 0 {
					return "", fmt.Errorf("missing } in \\N escape")
				}
				end += close + 2
			} else if pattern[pos+1] == 'U' {
				end = min(pos+10, len(pattern))
			} else if pattern[pos+1] == 'u' {
				end = min(pos+6, len(pattern))
			} else if pattern[pos+1] == 'x' {
				end = min(pos+4, len(pattern))
			}
			out = append(out, pattern[pos:end]...)
			pos = end
			lastAtomStart = start
			continue
		case '[':
			end := pos + 1
			first := true
			for end < len(pattern) {
				if pattern[end] == '\\' && end+1 < len(pattern) {
					end += 2
					first = false
					continue
				}
				if pattern[end] == ']' && !first {
					end++
					break
				}
				first = false
				end++
			}
			out = append(out, pattern[pos:end]...)
			pos = end
			lastAtomStart = start
			continue
		case '(':
			groupStarts = append(groupStarts, start)
			out = append(out, '(')
			pos++
			lastAtomStart = -1
			continue
		case ')':
			out = append(out, ')')
			pos++
			if len(groupStarts) > 0 {
				lastAtomStart = groupStarts[len(groupStarts)-1]
				groupStarts = groupStarts[:len(groupStarts)-1]
			} else {
				lastAtomStart = -1
			}
			continue
		case '*', '+', '?':
			end := pos + 1
			if end < len(pattern) && pattern[end] == '+' && lastAtomStart >= 0 {
				atom := append([]byte(nil), out[lastAtomStart:]...)
				out = out[:lastAtomStart]
				out = append(out, "(?>"...)
				out = append(out, atom...)
				out = append(out, pattern[pos], ')')
				pos = end + 1
				continue
			}
			out = append(out, pattern[pos])
			pos++
			continue
		case '{':
			close := strings.IndexByte(pattern[pos:], '}')
			if close > 0 && lastAtomStart >= 0 {
				end := pos + close + 1
				quantifier := pattern[pos:end]
				inner := pattern[pos+1 : end-1]
				if !isPythonRepeatBody(inner) {
					break
				}
				if strings.HasPrefix(inner, ",") {
					quantifier = "{0" + inner + "}"
				}
				if end < len(pattern) && pattern[end] == '+' && lastAtomStart >= 0 {
					atom := append([]byte(nil), out[lastAtomStart:]...)
					out = out[:lastAtomStart]
					out = append(out, "(?>"...)
					out = append(out, atom...)
					out = append(out, quantifier...)
					out = append(out, ')')
					pos = end + 1
					continue
				}
				if quantifier != pattern[pos:end] {
					out = append(out, quantifier...)
					pos = end
					continue
				}
			}
		case '|', '^', '$':
			out = append(out, pattern[pos])
			pos++
			lastAtomStart = -1
			continue
		}
		_, width := utf8.DecodeRuneInString(pattern[pos:])
		out = append(out, pattern[pos:pos+width]...)
		pos += width
		lastAtomStart = start
	}
	return string(out), nil
}

func (tr *regexTranslator) translate() error {
	for tr.pos < len(tr.src) {
		if err := tr.skipVerbose(); err != nil {
			return err
		}
		if tr.pos >= len(tr.src) {
			break
		}
		ch := tr.src[tr.pos]
		switch {
		case ch == '\\':
			if err := tr.writeEscape(false); err != nil {
				return err
			}
			tr.sawConsuming = true
		case ch == '[':
			if err := tr.writeClass(); err != nil {
				return err
			}
			tr.sawConsuming = true
		case ch == '(':
			if err := tr.writeGroup(); err != nil {
				return err
			}
		case ch == ')':
			if err := tr.closeGroup(); err != nil {
				return err
			}
		case ch == '.':
			if tr.dotAll {
				tr.out.WriteString(`[\s\S]`)
			} else {
				tr.out.WriteByte('.')
			}
			tr.pos++
			tr.sawConsuming = true
		case ch == '*' || ch == '+' || ch == '?':
			tr.out.WriteByte(ch)
			tr.pos++
			if tr.pos < len(tr.src) && (tr.src[tr.pos] == '+' || tr.src[tr.pos] == '?') {
				tr.out.WriteByte(tr.src[tr.pos])
				tr.pos++
			}
		case ch == '^' || ch == '$' || ch == '|' || ch == '{' || ch == '}' || ch == ']':
			tr.out.WriteByte(ch)
			tr.pos++
			if ch == '|' || ch == '^' || ch == '$' {
				// zero-width or alternation; still blocks later global flags
				tr.sawConsuming = true
			}
		default:
			r, width := utf8.DecodeRuneInString(tr.src[tr.pos:])
			if r == utf8.RuneError && width == 1 {
				return fmt.Errorf("invalid UTF-8 in regular expression")
			}
			tr.writeLiteral(r)
			tr.pos += width
			tr.sawConsuming = true
		}
	}
	if len(tr.groupStack) != 0 {
		return fmt.Errorf("unbalanced regular expression")
	}
	return nil
}

func (tr *regexTranslator) skipVerbose() error {
	for tr.verbose && tr.pos < len(tr.src) {
		ch := tr.src[tr.pos]
		if ch == '#' {
			for tr.pos < len(tr.src) && tr.src[tr.pos] != '\n' {
				tr.pos++
			}
			continue
		}
		r, width := utf8.DecodeRuneInString(tr.src[tr.pos:])
		if unicode.IsSpace(r) {
			tr.pos += width
			continue
		}
		break
	}
	return nil
}

func (tr *regexTranslator) writeLiteral(r rune) {
	if tr.ignoreCase {
		if tr.ascii && isASCIILetter(r) {
			tr.out.WriteString(`[`)
			tr.out.WriteRune(unicode.ToLower(r))
			tr.out.WriteRune(unicode.ToUpper(r))
			tr.out.WriteByte(']')
			return
		}
		if !tr.ascii && hasUnicodeCaseVariant(r) {
			tr.writeIgnoreCaseLiteral(r)
			return
		}
	}
	tr.out.WriteRune(r)
}

func (tr *regexTranslator) writeEscape(inClass bool) error {
	if tr.pos+1 >= len(tr.src) {
		return fmt.Errorf("trailing backslash")
	}
	esc := tr.src[tr.pos+1]
	switch esc {
	case 'A':
		tr.out.WriteString(`\A`)
		tr.pos += 2
	case 'Z':
		tr.out.WriteString(`\z`)
		tr.pos += 2
	case 'z':
		return fmt.Errorf(`unknown escape \z`)
	case 'w':
		tr.writeShorthand(tr.wordBody(), false, inClass)
		tr.pos += 2
	case 'W':
		tr.writeShorthand(tr.wordBody(), true, inClass)
		tr.pos += 2
	case 's':
		tr.writeShorthand(tr.spaceBody(), false, inClass)
		tr.pos += 2
	case 'S':
		tr.writeShorthand(tr.spaceBody(), true, inClass)
		tr.pos += 2
	case 'd':
		tr.writeShorthand(tr.digitBody(), false, inClass)
		tr.pos += 2
	case 'D':
		tr.writeShorthand(tr.digitBody(), true, inClass)
		tr.pos += 2
	case 'b':
		if inClass {
			tr.out.WriteString(`\b`)
			tr.pos += 2
			return nil
		}
		tr.writeBoundary(false)
		tr.pos += 2
	case 'B':
		if inClass {
			tr.out.WriteString(`\B`)
			tr.pos += 2
			return nil
		}
		tr.writeBoundary(true)
		tr.pos += 2
	case 'N':
		return tr.writeUnicodeNameEscape(inClass)
	case 'U':
		return tr.writeLongUnicodeEscape(inClass)
	case 'u':
		return tr.writeShortUnicodeEscape(inClass)
	case 'x':
		return tr.writeHexEscape(inClass)
	case 'p', 'P':
		return fmt.Errorf(`unicode category escapes are unsupported`)
	case 'k':
		return fmt.Errorf(`dotnet named backreferences are unsupported`)
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return tr.writeDigitEscape(inClass)
	default:
		if isASCIILetter(rune(esc)) && !strings.ContainsRune("afnrtv", rune(esc)) {
			return fmt.Errorf("unknown escape \\%c", esc)
		}
		tr.out.WriteByte('\\')
		tr.out.WriteByte(esc)
		tr.pos += 2
	}
	return nil
}

func (tr *regexTranslator) writeShorthand(body string, negated, inClass bool) {
	if inClass {
		// Class rewriting handles shorthands; this path is only for outer escapes.
		if negated {
			tr.out.WriteString(`[^` + body + `]`)
			return
		}
		tr.out.WriteString(body)
		return
	}
	if negated {
		tr.out.WriteString(`[^` + body + `]`)
		return
	}
	tr.out.WriteString(`[` + body + `]`)
}

func (tr *regexTranslator) wordBody() string {
	if tr.ascii {
		return pythonASCIIWordBody
	}
	return pythonWordClassBody
}

func (tr *regexTranslator) spaceBody() string {
	if tr.ascii {
		return pythonASCIISpaceBody
	}
	return pythonSpaceClassBody
}

func (tr *regexTranslator) digitBody() string {
	if tr.ascii {
		return pythonASCIIDigitBody
	}
	return `\d`
}

func (tr *regexTranslator) writeBoundary(negated bool) {
	word := tr.wordBody()
	boundary := `(?:(?:(?<=[` + word + `])(?:(?=[^` + word + `])|\z))|(?:(?:\A|(?<=[^` + word + `]))(?=[` + word + `])))`
	if negated {
		// CPython 3.12's \B does not match the sole position in an empty string.
		tr.out.WriteString(`(?!\A\z)(?!` + boundary + `)`)
		return
	}
	tr.out.WriteString(boundary)
}

type classPart struct {
	kind string // "pos", "neg", "text"
	text string
}

type classAtom struct {
	kind    string // "pos", "neg", "literal"
	text    string
	literal rune
}

func (tr *regexTranslator) writeClass() error {
	tr.pos++ // [
	negated := false
	if tr.pos < len(tr.src) && tr.src[tr.pos] == '^' {
		negated = true
		tr.pos++
	}
	var parts []classPart
	first := true
	closed := false
	sawI, sawS, sawMu := false, false, false
	for tr.pos < len(tr.src) {
		if tr.src[tr.pos] == ']' && !first {
			tr.pos++
			closed = true
			break
		}
		first = false
		atom, err := tr.readClassAtom()
		if err != nil {
			return err
		}
		if atom.kind == "literal" && tr.pos+1 < len(tr.src) && tr.src[tr.pos] == '-' && tr.src[tr.pos+1] != ']' {
			tr.pos++
			end, err := tr.readClassAtom()
			if err != nil {
				return err
			}
			if end.kind != "literal" {
				return fmt.Errorf("bad character range")
			}
			text := atom.text + "-" + end.text
			if tr.ignoreCase && tr.ascii {
				text = expandASCIIIgnoreCaseRange(atom.literal, end.literal)
			} else {
				notePythonIgnoreCaseRange(atom.literal, end.literal, &sawI, &sawS, &sawMu)
			}
			parts = append(parts, classPart{kind: "text", text: text})
			continue
		}
		if atom.kind != "literal" && tr.pos+1 < len(tr.src) && tr.src[tr.pos] == '-' && tr.src[tr.pos+1] != ']' {
			return fmt.Errorf("bad character range")
		}
		if atom.kind == "literal" {
			notePythonIgnoreCaseExtras(atom.literal, &sawI, &sawS, &sawMu)
			atom.text = tr.classLiteralText(atom)
		}
		parts = append(parts, classPart{kind: atom.kind, text: atom.text})
	}
	if !closed || len(parts) == 0 {
		return fmt.Errorf("unterminated character class")
	}

	hasNeg := false
	for _, part := range parts {
		if part.kind == "neg" {
			hasNeg = true
			break
		}
	}

	extras := ""
	if tr.ignoreCase && !tr.ascii {
		if sawI {
			extras += `ı`
		}
		if sawS {
			extras += `ſ`
		}
		if sawMu {
			extras += `µμΜ`
		}
	}

	if !hasNeg {
		tr.out.WriteByte('[')
		if negated {
			tr.out.WriteByte('^')
		}
		for _, part := range parts {
			tr.out.WriteString(part.text)
		}
		tr.out.WriteString(extras)
		tr.out.WriteByte(']')
		return nil
	}

	if !negated {
		// Union: positive body ∪ each negated shorthand complement.
		var alts []string
		var positive strings.Builder
		for _, part := range parts {
			switch part.kind {
			case "neg":
				alts = append(alts, `[^`+part.text+`]`)
			default:
				positive.WriteString(part.text)
			}
		}
		if positive.Len() > 0 {
			body := positive.String() + extras
			alts = append([]string{`[` + body + `]`}, alts...)
		}
		if len(alts) == 1 {
			tr.out.WriteString(alts[0])
		} else {
			tr.out.WriteString(`(?:` + strings.Join(alts, `|`) + `)`)
		}
		return nil
	}

	// Negated outer with negated shorthands:
	// ¬(P ∪ ¬S1 ∪ ¬S2 ...) = ¬P ∩ S1 ∩ S2 ...
	var positive strings.Builder
	var need []string
	for _, part := range parts {
		switch part.kind {
		case "neg":
			need = append(need, part.text)
		default:
			positive.WriteString(part.text)
		}
	}
	positive.WriteString(extras)
	tr.out.WriteString(`(?:`)
	if positive.Len() > 0 {
		tr.out.WriteString(`(?![` + positive.String() + `])`)
	}
	for _, body := range need {
		tr.out.WriteString(`(?=[` + body + `])`)
	}
	tr.out.WriteString(`[\s\S])`)
	return nil
}

func (tr *regexTranslator) readClassAtom() (classAtom, error) {
	if tr.pos >= len(tr.src) {
		return classAtom{}, fmt.Errorf("unterminated character class")
	}
	if tr.src[tr.pos] != '\\' {
		r, width := utf8.DecodeRuneInString(tr.src[tr.pos:])
		if r == utf8.RuneError && width == 1 {
			return classAtom{}, fmt.Errorf("invalid UTF-8 in regular expression")
		}
		text := tr.src[tr.pos : tr.pos+width]
		tr.pos += width
		return classAtom{kind: "literal", text: text, literal: r}, nil
	}
	if tr.pos+1 >= len(tr.src) {
		return classAtom{}, fmt.Errorf("trailing backslash")
	}
	esc := tr.src[tr.pos+1]
	switch esc {
	case 'w':
		tr.pos += 2
		return classAtom{kind: "pos", text: tr.wordBody()}, nil
	case 'W':
		tr.pos += 2
		return classAtom{kind: "neg", text: tr.wordBody()}, nil
	case 's':
		tr.pos += 2
		return classAtom{kind: "pos", text: tr.spaceBody()}, nil
	case 'S':
		tr.pos += 2
		return classAtom{kind: "neg", text: tr.spaceBody()}, nil
	case 'd':
		tr.pos += 2
		return classAtom{kind: "pos", text: tr.digitBody()}, nil
	case 'D':
		tr.pos += 2
		return classAtom{kind: "neg", text: tr.digitBody()}, nil
	case 'N':
		if tr.pos+2 >= len(tr.src) || tr.src[tr.pos+2] != '{' {
			return classAtom{}, fmt.Errorf("missing { in \\N escape")
		}
		start := tr.pos + 3
		offset := strings.IndexByte(tr.src[start:], '}')
		if offset < 0 {
			return classAtom{}, fmt.Errorf("missing } in \\N escape")
		}
		r, ok := lookupPythonUnicodeName(tr.src[start : start+offset])
		if !ok {
			return classAtom{}, fmt.Errorf("unknown unicode name")
		}
		tr.pos = start + offset + 1
		return classLiteralAtom(r), nil
	case 'U':
		return tr.readClassHexAtom(8, 'U')
	case 'u':
		return tr.readClassHexAtom(4, 'u')
	case 'x':
		return tr.readClassHexAtom(2, 'x')
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if esc > '7' {
			return classAtom{}, fmt.Errorf("invalid group reference")
		}
		end := tr.pos + 1
		for end < len(tr.src) && end < tr.pos+4 && tr.src[end] >= '0' && tr.src[end] <= '7' {
			end++
		}
		value, err := strconv.ParseUint(tr.src[tr.pos+1:end], 8, 16)
		if err != nil || value > 0o377 {
			return classAtom{}, fmt.Errorf("invalid octal escape")
		}
		tr.pos = end
		return classLiteralAtom(rune(value)), nil
	default:
		if isASCIILetter(rune(esc)) && !strings.ContainsRune("abfnrtv", rune(esc)) {
			return classAtom{}, fmt.Errorf("unknown escape \\%c", esc)
		}
		values := map[byte]rune{'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v'}
		r := rune(esc)
		if value, ok := values[esc]; ok {
			r = value
		}
		text := tr.src[tr.pos : tr.pos+2]
		tr.pos += 2
		return classAtom{kind: "literal", text: text, literal: r}, nil
	}
}

func (tr *regexTranslator) readClassHexAtom(digits int, escape byte) (classAtom, error) {
	end := tr.pos + 2 + digits
	if end > len(tr.src) {
		return classAtom{}, fmt.Errorf("incomplete \\%c escape", escape)
	}
	value, err := strconv.ParseUint(tr.src[tr.pos+2:end], 16, 32)
	if err != nil || escape == 'U' && !utf8.ValidRune(rune(value)) {
		return classAtom{}, fmt.Errorf("invalid \\%c escape", escape)
	}
	tr.pos = end
	return classLiteralAtom(rune(value)), nil
}

func classLiteralAtom(r rune) classAtom {
	return classAtom{kind: "literal", text: fmt.Sprintf(`\x{%X}`, uint32(r)), literal: r}
}

func (tr *regexTranslator) classLiteralText(atom classAtom) string {
	if tr.ignoreCase && tr.ascii && isASCIILetter(atom.literal) {
		return string([]rune{unicode.ToLower(atom.literal), unicode.ToUpper(atom.literal)})
	}
	if tr.ignoreCase && !tr.ascii && hasUnicodeCaseVariant(atom.literal) {
		return pythonIgnoreCaseBody(atom.literal)
	}
	return atom.text
}

func notePythonIgnoreCaseRange(start, end rune, sawI, sawS, sawMu *bool) {
	if start > end {
		start, end = end, start
	}
	for _, r := range []rune{'i', 'I', 's', 'S', 'µ', 'μ', 'Μ'} {
		if r >= start && r <= end {
			notePythonIgnoreCaseExtras(r, sawI, sawS, sawMu)
		}
	}
}

func notePythonIgnoreCaseExtras(r rune, sawI, sawS, sawMu *bool) {
	switch unicode.ToLower(r) {
	case 'i':
		*sawI = true
	case 's':
		*sawS = true
	case 'µ', 'μ':
		*sawMu = true
	}
}

func expandASCIIIgnoreCaseRange(start, end rune) string {
	var body strings.Builder
	lo, hi := start, end
	if lo > hi {
		lo, hi = hi, lo
	}
	for r := lo; r <= hi; r++ {
		if isASCIILetter(r) {
			body.WriteRune(unicode.ToLower(r))
			body.WriteRune(unicode.ToUpper(r))
			continue
		}
		body.WriteRune(r)
	}
	return body.String()
}

func (tr *regexTranslator) writeGroup() error {
	if tr.pos+1 >= len(tr.src) {
		return fmt.Errorf("unterminated group")
	}
	if tr.src[tr.pos+1] != '?' {
		return tr.openCapturingGroup("")
	}
	rest := tr.src[tr.pos+2:]
	switch {
	case strings.HasPrefix(rest, "P<"):
		return tr.writeNamedGroup()
	case strings.HasPrefix(rest, "P="):
		return tr.writeNamedBackref()
	case strings.HasPrefix(rest, "<="):
		return tr.writeLookbehind(false)
	case strings.HasPrefix(rest, "<!"):
		return tr.writeLookbehind(true)
	case strings.HasPrefix(rest, "="):
		tr.pushGroup(false, 0)
		tr.out.WriteString(`(?=`)
		tr.pos += 3
		tr.sawConsuming = true
		return nil
	case strings.HasPrefix(rest, "!"):
		tr.pushGroup(false, 0)
		tr.out.WriteString(`(?!`)
		tr.pos += 3
		tr.sawConsuming = true
		return nil
	case strings.HasPrefix(rest, ">"):
		tr.pushGroup(false, 0)
		tr.out.WriteString(`(?>`)
		tr.pos += 3
		tr.sawConsuming = true
		return nil
	case strings.HasPrefix(rest, "#"):
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return fmt.Errorf("unterminated comment group")
		}
		tr.pos += 2 + end + 1
		return nil
	case strings.HasPrefix(rest, ":"):
		tr.pushGroup(false, 0)
		tr.out.WriteString(`(?:`)
		tr.pos += 3
		tr.sawConsuming = true
		return nil
	case len(rest) > 0 && rest[0] == '(':
		return tr.writeConditional()
	case len(rest) > 0 && (rest[0] == '-' || isFlagChar(rest[0])):
		return tr.writeFlagsGroup()
	case strings.HasPrefix(rest, "<"), strings.HasPrefix(rest, "'"):
		return fmt.Errorf("dotnet named groups are unsupported")
	default:
		return fmt.Errorf("unsupported group extension")
	}
}

func (tr *regexTranslator) openCapturingGroup(pythonName string) error {
	tr.groupCount++
	if tr.groupCount > maxRegexGroups {
		return fmt.Errorf("regular expression capture groups exceed limit")
	}
	for len(tr.groupWidth) <= tr.groupCount {
		tr.groupWidth = append(tr.groupWidth, regexWidth{})
	}
	if pythonName != "" {
		generated := fmt.Sprintf("g%d", tr.groupCount)
		tr.names[pythonName] = generated
		tr.definedAt[pythonName] = tr.groupCount
		tr.out.WriteString(`(?<` + generated + `>`)
	} else {
		tr.out.WriteByte('(')
	}
	tr.pos++ // skip '('
	tr.pushGroup(true, tr.groupCount)
	tr.sawConsuming = true
	return nil
}

func (tr *regexTranslator) writeNamedGroup() error {
	start := tr.pos + 4
	if start > len(tr.src) {
		return fmt.Errorf("unterminated named group")
	}
	end := strings.IndexByte(tr.src[start:], '>')
	if end < 0 {
		return fmt.Errorf("unterminated named group")
	}
	name := tr.src[start : start+end]
	if !isPythonGroupName(name) {
		return fmt.Errorf("invalid group name")
	}
	if _, exists := tr.names[name]; exists {
		return fmt.Errorf("duplicate group name")
	}
	tr.groupCount++
	if tr.groupCount > maxRegexGroups {
		return fmt.Errorf("regular expression capture groups exceed limit")
	}
	for len(tr.groupWidth) <= tr.groupCount {
		tr.groupWidth = append(tr.groupWidth, regexWidth{})
	}
	generated := fmt.Sprintf("g%d", tr.groupCount)
	tr.names[name] = generated
	tr.definedAt[name] = tr.groupCount
	tr.out.WriteString(`(?<` + generated + `>`)
	tr.pos = start + end + 1
	tr.pushGroup(true, tr.groupCount)
	tr.sawConsuming = true
	return nil
}

func (tr *regexTranslator) writeNamedBackref() error {
	start := tr.pos + 4
	end := strings.IndexByte(tr.src[start:], ')')
	if end < 0 {
		return fmt.Errorf("unterminated named backreference")
	}
	name := tr.src[start : start+end]
	generated, ok := tr.names[name]
	groupNumber := tr.definedAt[name]
	if !ok || tr.isGroupOpen(groupNumber) {
		return fmt.Errorf("unknown group name")
	}
	tr.out.WriteString(`\k<` + generated + `>`)
	tr.pos = start + end + 1
	tr.sawConsuming = true
	return nil
}

func (tr *regexTranslator) writeConditional() error {
	// (?(id/name)yes|no)
	if tr.pos+3 >= len(tr.src) || tr.src[tr.pos+2] != '(' {
		return fmt.Errorf("invalid conditional")
	}
	condStart := tr.pos + 3
	if condStart < len(tr.src) && (tr.src[condStart] == '?' || tr.src[condStart] == '<') {
		return fmt.Errorf("assertion conditionals are unsupported")
	}
	condEnd := strings.IndexByte(tr.src[condStart:], ')')
	if condEnd < 0 {
		return fmt.Errorf("unterminated conditional")
	}
	cond := tr.src[condStart : condStart+condEnd]
	if cond == "" {
		return fmt.Errorf("invalid conditional")
	}
	if isAllDigits(cond) {
		n, err := strconv.Atoi(cond)
		if err != nil || n < 1 || n > tr.groupCount || tr.isGroupOpen(n) {
			return fmt.Errorf("invalid group reference")
		}
	} else {
		if !isPythonGroupName(cond) {
			return fmt.Errorf("invalid group name")
		}
		n := tr.definedAt[cond]
		if _, ok := tr.names[cond]; !ok || tr.isGroupOpen(n) {
			return fmt.Errorf("unknown group name")
		}
		cond = tr.names[cond]
	}
	tr.pushGroup(false, 0)
	tr.out.WriteString(`(?(` + cond + `)`)
	tr.pos = condStart + condEnd + 1
	tr.sawConsuming = true
	return nil
}

func (tr *regexTranslator) writeLookbehind(negated bool) error {
	prefix := `(?<=`
	advance := 4
	if negated {
		prefix = `(?<!`
	}
	contentStart := tr.pos + advance
	end, ok := matchingGroupEnd(tr.src, tr.pos)
	if !ok {
		return fmt.Errorf("unterminated lookbehind")
	}
	content := tr.src[contentStart:end]
	width, fixed := tr.fixedWidth(content)
	if !fixed {
		return fmt.Errorf("lookbehind requires fixed-width pattern")
	}
	_ = width
	tr.pushGroup(false, 0)
	tr.out.WriteString(prefix)
	tr.pos = contentStart
	tr.sawConsuming = true
	return nil
}

func (tr *regexTranslator) closeGroup() error {
	if len(tr.groupStack) == 0 {
		return fmt.Errorf("unbalanced group")
	}
	frame := tr.groupStack[len(tr.groupStack)-1]
	tr.groupStack = tr.groupStack[:len(tr.groupStack)-1]
	if frame.capturing && frame.groupNumber > 0 && frame.groupNumber < len(tr.groupWidth) {
		if width, ok := tr.scanWidth(tr.src[frame.sourceStart:tr.pos], 0); ok {
			tr.groupWidth[frame.groupNumber] = width
		} else {
			tr.groupWidth[frame.groupNumber] = regexWidth{}
		}
	}
	tr.ascii = frame.ascii
	tr.ignoreCase = frame.ignoreCase
	tr.multiline = frame.multiline
	tr.dotAll = frame.dotAll
	tr.verbose = frame.verbose
	tr.emitEngineIgnoreCase = frame.emitEngineIgnoreCase
	tr.out.WriteByte(')')
	tr.pos++
	tr.nesting--
	return nil
}

func (tr *regexTranslator) writeFlagsGroup() error {
	pos := tr.pos + 2
	var enable, disable []byte
	section := &enable
	sawDash := false
	for pos < len(tr.src) {
		ch := tr.src[pos]
		if ch == '-' {
			if section == &disable {
				return fmt.Errorf("unknown flag")
			}
			section = &disable
			sawDash = true
			pos++
			continue
		}
		if ch == ':' || ch == ')' {
			break
		}
		if !isFlagChar(ch) {
			return fmt.Errorf("unknown flag")
		}
		switch ch {
		case 'L':
			return fmt.Errorf("locale flag is unsupported")
		case 'n':
			return fmt.Errorf("flag n is unsupported")
		}
		*section = append(*section, ch)
		pos++
	}
	if pos >= len(tr.src) {
		return fmt.Errorf("unterminated flags group")
	}
	scoped := tr.src[pos] == ':'
	if len(enable)+len(disable) == 0 || sawDash && len(disable) == 0 {
		return fmt.Errorf("missing flag")
	}
	if !scoped && len(disable) > 0 {
		return fmt.Errorf("missing :")
	}
	if !scoped && tr.sawConsuming {
		return fmt.Errorf("global flags not at the start of the expression")
	}
	has := func(list []byte, flag byte) bool {
		for _, b := range list {
			if b == flag {
				return true
			}
		}
		return false
	}
	if has(enable, 'a') && has(enable, 'u') {
		return fmt.Errorf("flags a, u and L are incompatible")
	}
	if has(disable, 'a') || has(disable, 'u') || has(disable, 'L') {
		return fmt.Errorf("cannot turn off flags a, u and L")
	}
	for _, flag := range enable {
		if has(disable, flag) {
			return fmt.Errorf("flag turned on and off")
		}
	}

	ascii, ignoreCase, multiline, dotAll, verbose := tr.ascii, tr.ignoreCase, tr.multiline, tr.dotAll, tr.verbose
	for _, flag := range enable {
		switch flag {
		case 'a':
			if has(enable, 'u') {
				return fmt.Errorf("flags a, u and L are incompatible")
			}
			if !scoped && tr.typeFlag == 'u' {
				return fmt.Errorf("ASCII and UNICODE flags are incompatible")
			}
			ascii = true
		case 'u':
			if !scoped && tr.typeFlag == 'a' || has(enable, 'a') {
				return fmt.Errorf("ASCII and UNICODE flags are incompatible")
			}
			// CPython's compile-wide ASCII flag remains effective inside a
			// later scoped Unicode group. A scoped Unicode group can override
			// only an enclosing scoped ASCII group.
			ascii = tr.typeFlag == 'a'
		case 'i':
			ignoreCase = true
		case 'm':
			multiline = true
		case 's':
			dotAll = true
		case 'x':
			verbose = true
		}
	}
	for _, flag := range disable {
		switch flag {
		case 'i':
			ignoreCase = false
		case 'm':
			multiline = false
		case 's':
			dotAll = false
		case 'x':
			verbose = false
		}
	}
	emitEngineIC := ignoreCase && !ascii

	if !scoped {
		if has(enable, 'a') {
			tr.typeFlag = 'a'
		} else if has(enable, 'u') {
			tr.typeFlag = 'u'
		}
		tr.ascii, tr.ignoreCase, tr.multiline, tr.dotAll, tr.verbose = ascii, ignoreCase, multiline, dotAll, verbose
		tr.emitEngineIgnoreCase = emitEngineIC
		var on strings.Builder
		if emitEngineIC {
			on.WriteByte('i')
		}
		if multiline {
			on.WriteByte('m')
		}
		if verbose {
			on.WriteByte('x')
		}
		// Dot-all is implemented via translation of '.'; do not emit engine 's'
		// because regexp2 \s in DOTALL interactions differ. We expand '.' ourselves.
		if on.Len() > 0 {
			tr.out.WriteString(`(?` + on.String() + `)`)
		}
		tr.pos = pos + 1
		return nil
	}

	tr.pushGroup(false, 0)
	var on, off strings.Builder
	if emitEngineIC && !tr.emitEngineIgnoreCase {
		on.WriteByte('i')
	}
	if multiline && !tr.multiline {
		on.WriteByte('m')
	}
	if verbose && !tr.verbose {
		on.WriteByte('x')
	}
	if !emitEngineIC && tr.emitEngineIgnoreCase {
		off.WriteByte('i')
	}
	if !multiline && tr.multiline {
		off.WriteByte('m')
	}
	if !verbose && tr.verbose {
		off.WriteByte('x')
	}
	if on.Len() == 0 && off.Len() == 0 {
		tr.out.WriteString(`(?:`)
	} else {
		tr.out.WriteString(`(?` + on.String())
		if off.Len() > 0 {
			tr.out.WriteByte('-')
			tr.out.WriteString(off.String())
		}
		tr.out.WriteByte(':')
	}
	tr.ascii, tr.ignoreCase, tr.multiline, tr.dotAll, tr.verbose = ascii, ignoreCase, multiline, dotAll, verbose
	tr.emitEngineIgnoreCase = emitEngineIC
	tr.pos = pos + 1
	return nil
}

func (tr *regexTranslator) pushGroup(capturing bool, number int) {
	tr.groupStack = append(tr.groupStack, regexGroupFrame{
		ascii: tr.ascii, ignoreCase: tr.ignoreCase, multiline: tr.multiline,
		dotAll: tr.dotAll, verbose: tr.verbose, emitEngineIgnoreCase: tr.emitEngineIgnoreCase,
		capturing: capturing, groupNumber: number, sourceStart: tr.pos,
	})
	tr.nesting++
	if tr.nesting > tr.maxNesting {
		tr.maxNesting = tr.nesting
	}
}

func (tr *regexTranslator) writeDigitEscape(inClass bool) error {
	start := tr.pos + 1
	end := start
	for end < len(tr.src) && tr.src[end] >= '0' && tr.src[end] <= '9' {
		end++
	}
	digits := tr.src[start:end]
	if digits == "" {
		return fmt.Errorf("trailing backslash")
	}

	// Inside a class, and outside when prefixed by 0 or by three octal
	// digits, Python parses an octal escape of at most three digits.
	threeOctal := len(digits) >= 3 && digits[0] <= '7' && digits[1] <= '7' && digits[2] <= '7'
	if inClass || digits[0] == '0' || threeOctal {
		tr.pos = end
		return tr.emitOctalDigits(digits, inClass)
	}

	// Otherwise Python consumes at most two digits as a decimal group
	// reference and leaves subsequent digits as literals.
	refLen := min(2, len(digits))
	n, err := strconv.Atoi(digits[:refLen])
	if err == nil && n >= 1 && n <= tr.groupCount && !tr.isGroupOpen(n) {
		tr.out.WriteByte('\\')
		tr.out.WriteString(digits[:refLen])
		tr.out.WriteString(digits[refLen:])
		tr.pos = end
		return nil
	}
	tr.pos = end
	return fmt.Errorf("invalid group reference")
}

func (tr *regexTranslator) isGroupOpen(number int) bool {
	for _, frame := range tr.groupStack {
		if frame.capturing && frame.groupNumber == number {
			return true
		}
	}
	return false
}

func (tr *regexTranslator) emitOctalDigits(digits string, inClass bool) error {
	octLen := 0
	for octLen < len(digits) && octLen < 3 && digits[octLen] >= '0' && digits[octLen] <= '7' {
		octLen++
	}
	if octLen == 0 {
		return fmt.Errorf("invalid group reference")
	}
	value, err := strconv.ParseUint(digits[:octLen], 8, 16)
	if err != nil || value > 0o377 {
		return fmt.Errorf("invalid octal escape")
	}
	tr.writeCaseAwareRune(rune(value), inClass)
	for index := octLen; index < len(digits); index++ {
		tr.out.WriteByte(digits[index])
	}
	return nil
}

func (tr *regexTranslator) writeUnicodeNameEscape(inClass bool) error {
	if tr.pos+2 >= len(tr.src) || tr.src[tr.pos+2] != '{' {
		return fmt.Errorf("missing { in \\N escape")
	}
	start := tr.pos + 3
	end := strings.IndexByte(tr.src[start:], '}')
	if end < 0 {
		return fmt.Errorf("missing } in \\N escape")
	}
	name := tr.src[start : start+end]
	r, ok := lookupPythonUnicodeName(name)
	if !ok {
		return fmt.Errorf("unknown unicode name")
	}
	tr.writeCaseAwareRune(r, inClass)
	tr.pos = start + end + 1
	return nil
}

func (tr *regexTranslator) writeLongUnicodeEscape(inClass bool) error {
	if tr.pos+9 >= len(tr.src) {
		return fmt.Errorf("incomplete \\U escape")
	}
	hex := tr.src[tr.pos+2 : tr.pos+10]
	value, err := strconv.ParseUint(hex, 16, 32)
	if err != nil || !utf8.ValidRune(rune(value)) {
		return fmt.Errorf("invalid \\U escape")
	}
	tr.writeCaseAwareRune(rune(value), inClass)
	tr.pos += 10
	return nil
}

func (tr *regexTranslator) writeShortUnicodeEscape(inClass bool) error {
	if tr.pos+5 >= len(tr.src) {
		return fmt.Errorf("incomplete \\u escape")
	}
	hex := tr.src[tr.pos+2 : tr.pos+6]
	value, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return fmt.Errorf("invalid \\u escape")
	}
	tr.writeCaseAwareRune(rune(value), inClass)
	tr.pos += 6
	return nil
}

func (tr *regexTranslator) writeHexEscape(inClass bool) error {
	if tr.pos+3 >= len(tr.src) {
		return fmt.Errorf("incomplete \\x escape")
	}
	hex := tr.src[tr.pos+2 : tr.pos+4]
	value, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return fmt.Errorf("invalid \\x escape")
	}
	tr.writeCaseAwareRune(rune(value), inClass)
	tr.pos += 4
	return nil
}

func (tr *regexTranslator) writeRuneEscape(r rune) {
	tr.out.WriteString(fmt.Sprintf(`\x{%X}`, uint32(r)))
}

func (tr *regexTranslator) writeCaseAwareRune(r rune, inClass bool) {
	if tr.ignoreCase && tr.ascii && isASCIILetter(r) {
		body := string([]rune{unicode.ToLower(r), unicode.ToUpper(r)})
		if inClass {
			tr.out.WriteString(body)
		} else {
			tr.out.WriteString(`[` + body + `]`)
		}
		return
	}
	if tr.ignoreCase && !tr.ascii && hasUnicodeCaseVariant(r) {
		body := pythonIgnoreCaseBody(r)
		if inClass {
			tr.out.WriteString(body)
		} else {
			tr.out.WriteString(`[` + body + `]`)
		}
		return
	}
	tr.writeRuneEscape(r)
}

func (tr *regexTranslator) writeIgnoreCaseLiteral(r rune) {
	tr.out.WriteString(`[` + pythonIgnoreCaseBody(r) + `]`)
}

func pythonIgnoreCaseBody(r rune) string {
	var body strings.Builder
	seen := map[rune]bool{}
	for candidate := r; !seen[candidate]; candidate = unicode.SimpleFold(candidate) {
		seen[candidate] = true
		body.WriteRune(candidate)
	}
	switch unicode.ToLower(r) {
	case 'i':
		body.WriteString(`ıİ`)
	case 's':
		body.WriteRune('ſ')
	case 'k':
		body.WriteRune('K')
	case 'µ', 'μ':
		body.WriteString(`µμΜ`)
	}
	return body.String()
}

func hasUnicodeCaseVariant(r rune) bool {
	return unicode.SimpleFold(r) != r || strings.ContainsRune("iIsSkKµμΜ", r)
}

// fixedWidth reports whether pattern has a single fixed match width in Python's
// lookbehind sense. Backreferences are accepted when the referenced group has a
// known fixed width.
func (tr *regexTranslator) fixedWidth(content string) (int, bool) {
	w, ok := tr.scanWidth(content, 0)
	if !ok || w.min != w.max {
		return 0, false
	}
	return w.min, true
}

func (tr *regexTranslator) scanWidth(content string, depth int) (regexWidth, bool) {
	total := regexWidth{ok: true}
	last := regexWidth{}
	index := 0
	var alt *regexWidth
	for index < len(content) {
		ch := content[index]
		if ch == '\\' {
			if index+1 >= len(content) {
				return regexWidth{}, false
			}
			esc := content[index+1]
			switch esc {
			case '1', '2', '3', '4', '5', '6', '7', '8', '9':
				end := index + 2
				for end < len(content) && content[end] >= '0' && content[end] <= '9' {
					end++
				}
				digits := content[index+1 : end]
				threeOctal := len(digits) >= 3 && digits[0] <= '7' && digits[1] <= '7' && digits[2] <= '7'
				if digits[0] == '0' || threeOctal {
					octLen := min(3, len(digits))
					for octLen > 0 && digits[octLen-1] > '7' {
						octLen--
					}
					last = regexWidth{min: 1, max: 1, ok: true}
					total.min += 1 + len(digits) - octLen
					total.max += 1 + len(digits) - octLen
					index = end
					continue
				}
				refLen := min(2, len(digits))
				n, err := strconv.Atoi(digits[:refLen])
				if err != nil || n < 1 || n >= len(tr.groupWidth) || !tr.groupWidth[n].ok || tr.groupWidth[n].min != tr.groupWidth[n].max {
					return regexWidth{}, false
				}
				last = regexWidth{
					min: tr.groupWidth[n].min + len(digits) - refLen,
					max: tr.groupWidth[n].max + len(digits) - refLen,
					ok:  true,
				}
				total.min += last.min
				total.max += last.max
				index = end
				continue
			case 'b', 'B', 'A', 'Z', 'z':
				last = regexWidth{ok: true}
				index += 2
				continue
			case 'N':
				if index+2 >= len(content) || content[index+2] != '{' {
					return regexWidth{}, false
				}
				close := strings.IndexByte(content[index+3:], '}')
				if close < 0 {
					return regexWidth{}, false
				}
				last = regexWidth{min: 1, max: 1, ok: true}
				total.min++
				total.max++
				index += close + 4
				continue
			case 'U':
				if index+10 > len(content) {
					return regexWidth{}, false
				}
				last = regexWidth{min: 1, max: 1, ok: true}
				total.min++
				total.max++
				index += 10
				continue
			case 'u':
				if index+6 > len(content) {
					return regexWidth{}, false
				}
				last = regexWidth{min: 1, max: 1, ok: true}
				total.min++
				total.max++
				index += 6
				continue
			case 'x':
				if index+4 > len(content) {
					return regexWidth{}, false
				}
				last = regexWidth{min: 1, max: 1, ok: true}
				total.min++
				total.max++
				index += 4
				continue
			default:
				last = regexWidth{min: 1, max: 1, ok: true}
				total.min++
				total.max++
				index += 2
				continue
			}
		}
		if ch == '[' {
			close := index + 1
			if close < len(content) && content[close] == '^' {
				close++
			}
			for close < len(content) {
				if content[close] == '\\' {
					close += 2
					continue
				}
				if content[close] == ']' && close > index+1 && !(close == index+2 && content[index+1] == '^') {
					break
				}
				if content[close] == ']' && close > index+1 {
					break
				}
				close++
			}
			if close >= len(content) {
				return regexWidth{}, false
			}
			total.min++
			total.max++
			last = regexWidth{min: 1, max: 1, ok: true}
			index = close + 1
			continue
		}
		if ch == '(' {
			end, ok := matchingGroupEnd(content, index)
			if !ok {
				return regexWidth{}, false
			}
			inner := content[index+1 : end]
			w, ok := tr.groupInnerWidth(inner)
			if !ok {
				return regexWidth{}, false
			}
			total.min += w.min
			total.max += w.max
			last = w
			index = end + 1
			continue
		}
		if ch == '|' && depth == 0 {
			if alt == nil {
				copyWidth := total
				alt = &copyWidth
			} else if alt.min != total.min || alt.max != total.max || !alt.ok || !total.ok {
				return regexWidth{}, false
			}
			total = regexWidth{ok: true}
			last = regexWidth{}
			index++
			continue
		}
		if ch == '*' || ch == '+' || ch == '?' {
			if !last.ok || last.min != 0 || last.max != 0 {
				return regexWidth{}, false
			}
			index++
			if index < len(content) && (content[index] == '+' || content[index] == '?') {
				index++
			}
			continue
		}
		if ch == '{' {
			close := strings.IndexByte(content[index:], '}')
			if close < 0 {
				return regexWidth{}, false
			}
			inner := content[index+1 : index+close]
			parts := strings.Split(inner, ",")
			var low, high int
			var err error
			if len(parts) == 1 {
				low, err = strconv.Atoi(parts[0])
				if err != nil || low < 0 {
					return regexWidth{}, false
				}
				high = low
			} else if len(parts) == 2 {
				if parts[0] == "" {
					low = 0
				} else {
					low, err = strconv.Atoi(parts[0])
				}
				if err != nil || low < 0 {
					return regexWidth{}, false
				}
				if parts[1] == "" {
					if last.ok && last.min == 0 && last.max == 0 {
						high = low
					} else {
						return regexWidth{}, false
					}
				} else {
					high, err = strconv.Atoi(parts[1])
				}
				if err != nil || low != high && (!last.ok || last.min != 0 || last.max != 0) {
					return regexWidth{}, false
				}
			} else {
				return regexWidth{}, false
			}
			if !last.ok {
				return regexWidth{}, false
			}
			total.min += (low - 1) * last.min
			total.max += (high - 1) * last.max
			last.min *= low
			last.max *= high
			index += close + 1
			if index < len(content) && (content[index] == '+' || content[index] == '?') {
				index++
			}
			continue
		}
		if ch == '^' || ch == '$' {
			last = regexWidth{ok: true}
			index++
			continue
		}
		_, width := utf8.DecodeRuneInString(content[index:])
		total.min++
		total.max++
		last = regexWidth{min: 1, max: 1, ok: true}
		index += width
	}
	if alt != nil {
		if alt.min != total.min || alt.max != total.max {
			return regexWidth{}, false
		}
		return *alt, true
	}
	return total, true
}

func (tr *regexTranslator) groupInnerWidth(inner string) (regexWidth, bool) {
	if !strings.HasPrefix(inner, "?") {
		return tr.scanWidth(inner, 0)
	}
	switch {
	case strings.HasPrefix(inner, "?:"):
		return tr.scanWidth(inner[2:], 0)
	case strings.HasPrefix(inner, "?="), strings.HasPrefix(inner, "?!"):
		// Assertions are zero-width even when their internal expression is variable.
		return regexWidth{ok: true}, true
	case strings.HasPrefix(inner, "?<="), strings.HasPrefix(inner, "?<!"):
		if _, ok := tr.fixedWidth(inner[3:]); !ok {
			return regexWidth{}, false
		}
		return regexWidth{ok: true}, true
	case strings.HasPrefix(inner, "?>"):
		return tr.scanWidth(inner[2:], 0)
	case strings.HasPrefix(inner, "?P<"):
		end := strings.IndexByte(inner, '>')
		if end < 0 {
			return regexWidth{}, false
		}
		return tr.scanWidth(inner[end+1:], 0)
	case strings.HasPrefix(inner, "?P="):
		name := strings.TrimSuffix(inner[3:], ")")
		name = strings.TrimSuffix(name, "")
		// inner is without closing paren; "?P=name"
		name = inner[3:]
		n, ok := tr.definedAt[name]
		if !ok || n >= len(tr.groupWidth) || !tr.groupWidth[n].ok || tr.groupWidth[n].min != tr.groupWidth[n].max {
			return regexWidth{}, false
		}
		return tr.groupWidth[n], true
	case strings.HasPrefix(inner, "?("):
		// Conditional: both branches must have equal fixed width.
		return tr.conditionalWidth(inner[1:])
	default:
		// Flag group (?i:...) or (?i)
		colon := strings.IndexByte(inner, ':')
		if colon < 0 {
			return regexWidth{ok: true}, true
		}
		return tr.scanWidth(inner[colon+1:], 0)
	}
}

func (tr *regexTranslator) conditionalWidth(inner string) (regexWidth, bool) {
	// inner starts with '(' of condition: (id)yes|no
	if !strings.HasPrefix(inner, "(") {
		return regexWidth{}, false
	}
	end := strings.IndexByte(inner, ')')
	if end < 0 {
		return regexWidth{}, false
	}
	body := inner[end+1:]
	yes, no, hasNo := body, "", false
	depth := 0
	inClass := false
	for index := 0; index < len(body); index++ {
		ch := body[index]
		if ch == '\\' {
			index++
			continue
		}
		if inClass {
			if ch == ']' {
				inClass = false
			}
			continue
		}
		switch ch {
		case '[':
			inClass = true
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth == 0 {
				yes = body[:index]
				no = body[index+1:]
				hasNo = true
				index = len(body)
			}
		}
	}
	yw, ok := tr.scanWidth(yes, 0)
	if !ok {
		return regexWidth{}, false
	}
	if !hasNo {
		if yw.min == 0 && yw.max == 0 {
			return yw, true
		}
		return regexWidth{}, false
	}
	nw, ok := tr.scanWidth(no, 0)
	if !ok || yw.min != nw.min || yw.max != nw.max {
		return regexWidth{}, false
	}
	return yw, true
}

func matchingGroupEnd(content string, start int) (int, bool) {
	depth := 0
	inClass := false
	for index := start; index < len(content); index++ {
		ch := content[index]
		if ch == '\\' {
			index++
			continue
		}
		if inClass {
			if ch == ']' {
				inClass = false
			}
			continue
		}
		switch ch {
		case '[':
			inClass = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index, true
			}
		}
	}
	return 0, false
}

func isFlagChar(ch byte) bool {
	switch ch {
	case 'a', 'i', 'L', 'm', 's', 'u', 'x', 'n':
		return true
	default:
		return false
	}
}

func isPythonGroupName(name string) bool {
	if name == "" {
		return false
	}
	for index, r := range name {
		if index == 0 {
			if !isPythonIdentifierStart(r) {
				return false
			}
			continue
		}
		if !isPythonIdentifierContinue(r) {
			return false
		}
	}
	return true
}

func isPythonIdentifierStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.In(r, unicode.Nl) ||
		r == '\u1885' || r == '\u1886' || r == '\u2118' || r == '\u212e' ||
		r == '\u309b' || r == '\u309c'
}

func isPythonIdentifierContinue(r rune) bool {
	return isPythonIdentifierStart(r) || unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc) ||
		r == '\u00b7' || r == '\u0387' || r >= '\u1369' && r <= '\u1371' || r == '\u19da'
}

func isASCIILetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

func isAllDigits(text string) bool {
	if text == "" {
		return false
	}
	for index := 0; index < len(text); index++ {
		if text[index] < '0' || text[index] > '9' {
			return false
		}
	}
	return true
}

func isPythonRepeatBody(text string) bool {
	if isAllDigits(text) {
		return true
	}
	if strings.Count(text, ",") != 1 {
		return false
	}
	parts := strings.SplitN(text, ",", 2)
	return (parts[0] == "" || isAllDigits(parts[0])) &&
		(parts[1] == "" || isAllDigits(parts[1]))
}
