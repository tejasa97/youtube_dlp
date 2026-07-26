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

// jwWave1JSToJSON converts a bounded JS object literal subset used by Iltalehti
// app state into JSON without executing page JavaScript. Supported syntax
// includes unquoted keys, single/double-quoted strings, trailing commas, line
// and block comments, undefined/void 0 → null, and pinned process_escape
// string handling. Template literals, variable substitution, new Map/Array, and
// other full js_to_json transforms are intentionally unsupported.
func jwWave1JSToJSON(src []byte) ([]byte, error) {
	if int64(len(src)) > maxExtractorJSONBytes {
		return nil, fmt.Errorf("%w: js object too large", ErrInvalidMetadata)
	}
	var out bytes.Buffer
	out.Grow(len(src) + 16)
	expectKey := false
	i := 0
	for i < len(src) {
		if int64(out.Len()) > maxExtractorJSONBytes {
			return nil, fmt.Errorf("%w: js json output too large", ErrInvalidMetadata)
		}
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
			out.WriteByte('{')
			expectKey = true
			i++
		case '[':
			out.WriteByte('[')
			expectKey = false
			i++
		case '}':
			out.WriteByte('}')
			expectKey = false
			i++
		case ']':
			out.WriteByte(']')
			expectKey = false
			i++
		case ':':
			out.WriteByte(':')
			expectKey = false
			i++
		case ',':
			j := i + 1
			for j < len(src) && isJSWhitespace(src[j]) {
				j++
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				i++
				continue
			}
			out.WriteByte(',')
			expectKey = true
			i++
		case '"', '\'':
			if err := writeJSStringLiteral(src, &i, character, &out); err != nil {
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
				out.WriteString("null")
				continue
			}
			if bytes.Equal(token, []byte("void")) {
				j := i
				for j < len(src) && isJSWhitespace(src[j]) {
					j++
				}
				if j < len(src) && src[j] == ':' {
					writeJSONStringToken(&out, token)
					continue
				}
				if j < len(src) && src[j] == '0' {
					i = j + 1
				}
				out.WriteString("null")
				continue
			}
			if bytes.Equal(token, []byte("true")) || bytes.Equal(token, []byte("false")) || bytes.Equal(token, []byte("null")) {
				out.Write(token)
				continue
			}
			if isJSNumberToken(token) {
				out.Write(token)
				continue
			}
			if expectKey {
				writeJSONStringToken(&out, token)
				expectKey = false
				continue
			}
			j := i
			for j < len(src) && isJSWhitespace(src[j]) {
				j++
			}
			if j < len(src) && src[j] == ':' {
				writeJSONStringToken(&out, token)
				continue
			}
			writeJSONStringToken(&out, token)
		}
	}
	return out.Bytes(), nil
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

func writeJSStringLiteral(src []byte, index *int, quote byte, out *bytes.Buffer) error {
	out.WriteByte('"')
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
			out.WriteByte('"')
			(*index)++
			return nil
		}
		writeJSONEscaped(out, character)
		(*index)++
	}
	return fmt.Errorf("%w: unterminated js string", ErrInvalidMetadata)
}

// writeJSEscapeSequence mirrors the pinned js_to_json process_escape subset.
func writeJSEscapeSequence(src []byte, index *int, out *bytes.Buffer) error {
	if *index >= len(src) {
		return fmt.Errorf("%w: malformed js escape", ErrInvalidMetadata)
	}
	escape := src[*index]
	(*index)++
	switch escape {
	case '"', '\\', 'b', 'f', 'n', 'r', 't', 'u':
		out.WriteByte('\\')
		out.WriteByte(escape)
	case 'x':
		out.WriteString(`\u00`)
	case '\n':
		// Line continuation removes the escaped newline.
	default:
		out.WriteByte(escape)
	}
	return nil
}

func writeJSONStringToken(out *bytes.Buffer, token []byte) {
	out.WriteByte('"')
	for _, character := range token {
		writeJSONEscaped(out, character)
	}
	out.WriteByte('"')
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

func writeJSONEscaped(out *bytes.Buffer, character byte) {
	switch character {
	case '"', '\\':
		out.WriteByte('\\')
		out.WriteByte(character)
	case '\n':
		out.WriteString(`\n`)
	case '\r':
		out.WriteString(`\r`)
	case '\t':
		out.WriteString(`\t`)
	default:
		out.WriteByte(character)
	}
}
