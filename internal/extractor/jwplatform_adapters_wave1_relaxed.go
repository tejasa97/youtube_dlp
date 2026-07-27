package extractor

import (
	"bytes"
	"fmt"
	"regexp"
	"unicode"
)

// extractJSObjectAfter locates a balanced {...} literal after marker. Unlike
// extractJSONObjectAfter it understands single-quoted strings and JS comments.
func extractJSObjectAfter(page []byte, marker *regexp.Regexp) ([]byte, error) {
	location := marker.FindIndex(page)
	if location == nil {
		return nil, fmt.Errorf("marker absent")
	}
	start := location[1]
	for start < len(page) && isJSWhitespace(page[start]) {
		start++
	}
	return extractBalancedJSObject(page, start)
}

func extractBalancedJSObject(page []byte, start int) ([]byte, error) {
	if start >= len(page) || page[start] != '{' {
		return nil, fmt.Errorf("object absent")
	}
	depth := 0
	var quote byte
	escaped, lineComment, blockComment := false, false, false
	for index := start; index < len(page) && int64(index-start) <= maxExtractorJSONBytes; index++ {
		character := page[index]
		switch {
		case lineComment:
			if character == '\n' {
				lineComment = false
			}
		case blockComment:
			if character == '*' && index+1 < len(page) && page[index+1] == '/' {
				blockComment = false
				index++
			}
		case quote != 0:
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
		case character == '"' || character == '\'':
			quote = character
		case character == '/' && index+1 < len(page):
			switch page[index+1] {
			case '/':
				lineComment = true
				index++
			case '*':
				blockComment = true
				index++
			default:
				if character == '{' {
					depth++
				}
				if character == '}' {
					depth--
					if depth == 0 {
						return append([]byte(nil), page[start:index+1]...), nil
					}
				}
			}
		default:
			if character == '{' {
				depth++
			}
			if character == '}' {
				depth--
				if depth == 0 {
					return append([]byte(nil), page[start:index+1]...), nil
				}
			}
		}
	}
	return nil, fmt.Errorf("unterminated object")
}

// jwWave1BoundedWriter is the single source of output-side capacity checks
// for the relaxed JS-to-JSON converter. Every byte, run, and string write
// routes through one of the methods on this type, each of which refuses to
// allocate output that would push the buffer past maxExtractorJSONBytes.
// Returning ErrInvalidMetadata before writing guarantees no oversized output
// is ever returned to the caller.
type jwWave1BoundedWriter struct {
	buf bytes.Buffer
}

func newJWWave1BoundedWriter(initial int) *jwWave1BoundedWriter {
	writer := &jwWave1BoundedWriter{}
	if initial > 0 {
		// bytes.Buffer.Grow preallocates the full requested slice; clamp the
		// hint so an at-cap source does not allocate beyond the output cap
		// before any bounded write has a chance to refuse.
		if int64(initial) > maxExtractorJSONBytes {
			initial = int(maxExtractorJSONBytes)
		}
		writer.buf.Grow(initial)
	}
	return writer
}

func (w *jwWave1BoundedWriter) reserve(additional int) error {
	if int64(w.buf.Len()+additional) > maxExtractorJSONBytes {
		return fmt.Errorf("%w: js json output too large", ErrInvalidMetadata)
	}
	return nil
}

func (w *jwWave1BoundedWriter) writeByte(character byte) error {
	if err := w.reserve(1); err != nil {
		return err
	}
	w.buf.WriteByte(character)
	return nil
}

func (w *jwWave1BoundedWriter) writeBytes(data []byte) error {
	if err := w.reserve(len(data)); err != nil {
		return err
	}
	w.buf.Write(data)
	return nil
}

func (w *jwWave1BoundedWriter) writeString(data string) error {
	if err := w.reserve(len(data)); err != nil {
		return err
	}
	w.buf.WriteString(data)
	return nil
}

func (w *jwWave1BoundedWriter) writeEscaped(character byte) error {
	switch character {
	case '"', '\\':
		if err := w.writeByte('\\'); err != nil {
			return err
		}
		return w.writeByte(character)
	case '\n':
		return w.writeString(`\n`)
	case '\r':
		return w.writeString(`\r`)
	case '\t':
		return w.writeString(`\t`)
	default:
		return w.writeByte(character)
	}
}

func (w *jwWave1BoundedWriter) bytes() []byte { return w.buf.Bytes() }

// jwWave1JSToJSON converts a bounded JS object literal subset used by Iltalehti
// app state into JSON without executing page JavaScript. Supported syntax
// includes unquoted keys, single/double-quoted strings, trailing commas
// (including trailing commas separated from `}`/`]` by line or block
// comments), undefined/void 0 → null, and pinned process_escape string
// handling. Template literals, variable substitution, new Map/Array, and other
// full js_to_json transforms are intentionally unsupported. Every output byte
// flows through jwWave1BoundedWriter so an oversize output returns
// ErrInvalidMetadata without producing an oversized buffer.
func jwWave1JSToJSON(src []byte) ([]byte, error) {
	if int64(len(src)) > maxExtractorJSONBytes {
		return nil, fmt.Errorf("%w: js object too large", ErrInvalidMetadata)
	}
	out := newJWWave1BoundedWriter(len(src) + 16)
	expectKey := false
	i := 0
	for i < len(src) {
		if skipped, err := skipJSNoise(src, &i); err != nil {
			return nil, err
		} else if skipped {
			continue
		}
		if i >= len(src) {
			break
		}
		character := src[i]
		switch character {
		case '{':
			if err := out.writeByte('{'); err != nil {
				return nil, err
			}
			expectKey = true
			i++
		case '[':
			if err := out.writeByte('['); err != nil {
				return nil, err
			}
			expectKey = false
			i++
		case '}':
			if err := out.writeByte('}'); err != nil {
				return nil, err
			}
			expectKey = false
			i++
		case ']':
			if err := out.writeByte(']'); err != nil {
				return nil, err
			}
			expectKey = false
			i++
		case ':':
			if err := out.writeByte(':'); err != nil {
				return nil, err
			}
			expectKey = false
			i++
		case ',':
			j := i + 1
			// Skip whitespace and comments as a single bounded step. A trailing
			// comma is recognized when the comment/whitespace sequence advances
			// directly into a closing delimiter.
			for {
				more, err := skipJSNoise(src, &j)
				if err != nil {
					return nil, err
				}
				if !more {
					break
				}
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				i = j
				continue
			}
			if err := out.writeByte(','); err != nil {
				return nil, err
			}
			expectKey = true
			i++
		case '"', '\'':
			if err := writeJSStringLiteral(src, &i, character, out); err != nil {
				return nil, err
			}
		default:
			start := i
			for i < len(src) && !isJSTokenDelimiter(src[i]) {
				i++
			}
			token := bytes.TrimSpace(src[start:i])
			if len(token) == 0 {
				continue
			}
			if bytes.Equal(token, []byte("undefined")) {
				if err := out.writeString("null"); err != nil {
					return nil, err
				}
				continue
			}
			if bytes.Equal(token, []byte("void")) {
				j := i
				for j < len(src) && isJSWhitespace(src[j]) {
					j++
				}
				if j < len(src) && src[j] == ':' {
					if err := writeJSONStringToken(out, token); err != nil {
						return nil, err
					}
					continue
				}
				if j < len(src) && src[j] == '0' {
					i = j + 1
				}
				if err := out.writeString("null"); err != nil {
					return nil, err
				}
				continue
			}
			if bytes.Equal(token, []byte("true")) || bytes.Equal(token, []byte("false")) || bytes.Equal(token, []byte("null")) {
				if err := out.writeBytes(token); err != nil {
					return nil, err
				}
				continue
			}
			if isJSNumberToken(token) {
				if err := out.writeBytes(token); err != nil {
					return nil, err
				}
				continue
			}
			if expectKey {
				if err := writeJSONStringToken(out, token); err != nil {
					return nil, err
				}
				expectKey = false
				continue
			}
			j := i
			for j < len(src) && isJSWhitespace(src[j]) {
				j++
			}
			if j < len(src) && src[j] == ':' {
				if err := writeJSONStringToken(out, token); err != nil {
					return nil, err
				}
				continue
			}
			if err := writeJSONStringToken(out, token); err != nil {
				return nil, err
			}
		}
	}
	return out.bytes(), nil
}

func skipJSNoise(src []byte, index *int) (bool, error) {
	for *index < len(src) && isJSWhitespace(src[*index]) {
		*index++
	}
	if *index+1 < len(src) && src[*index] == '/' && src[*index+1] == '/' {
		*index += 2
		for *index < len(src) && src[*index] != '\n' {
			*index++
		}
		return true, nil
	}
	if *index+1 < len(src) && src[*index] == '/' && src[*index+1] == '*' {
		*index += 2
		for *index+1 < len(src) {
			if src[*index] == '*' && src[*index+1] == '/' {
				*index += 2
				return true, nil
			}
			*index++
		}
		return false, fmt.Errorf("%w: unterminated js block comment", ErrInvalidMetadata)
	}
	return false, nil
}

func writeJSStringLiteral(src []byte, index *int, quote byte, out *jwWave1BoundedWriter) error {
	if err := out.writeByte('"'); err != nil {
		return err
	}
	(*index)++
	for *index < len(src) {
		character := src[*index]
		if character == '\\' {
			(*index)++
			if err := writeJSEscapeSequence(src, index, out); err != nil {
				return err
			}
			continue
		}
		if character == quote {
			if err := out.writeByte('"'); err != nil {
				return err
			}
			(*index)++
			return nil
		}
		if err := out.writeEscaped(character); err != nil {
			return err
		}
		(*index)++
	}
	return fmt.Errorf("%w: unterminated js string", ErrInvalidMetadata)
}

// writeJSEscapeSequence mirrors the pinned js_to_json process_escape subset.
func writeJSEscapeSequence(src []byte, index *int, out *jwWave1BoundedWriter) error {
	if *index >= len(src) {
		return fmt.Errorf("%w: malformed js escape", ErrInvalidMetadata)
	}
	escape := src[*index]
	(*index)++
	switch escape {
	case '"', '\\', 'b', 'f', 'n', 'r', 't', 'u':
		if err := out.writeByte('\\'); err != nil {
			return err
		}
		if err := out.writeByte(escape); err != nil {
			return err
		}
	case 'x':
		if err := out.writeString(`\u00`); err != nil {
			return err
		}
	case '\n':
		// Line continuation removes the escaped newline.
	default:
		if err := out.writeByte(escape); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONStringToken(out *jwWave1BoundedWriter, token []byte) error {
	if err := out.writeByte('"'); err != nil {
		return err
	}
	for _, character := range token {
		if err := out.writeEscaped(character); err != nil {
			return err
		}
	}
	return out.writeByte('"')
}

func isJSWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func isJSTokenDelimiter(ch byte) bool {
	switch ch {
	case '{', '}', '[', ']', ':', ',', '"', '\'', '/':
		return true
	default:
		return isJSWhitespace(ch)
	}
}

func isJSNumberToken(token []byte) bool {
	if len(token) == 0 {
		return false
	}
	for index, character := range token {
		if character == '-' && index == 0 {
			continue
		}
		if character == '.' {
			continue
		}
		if !unicode.IsDigit(rune(character)) {
			return false
		}
	}
	return true
}
