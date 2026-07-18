package config

import (
	"bytes"
	"fmt"
	"strings"
	stdunicode "unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Decode converts a supported yt-dlp configuration encoding to UTF-8.
// BOMs take priority over a first-512-byte coding declaration.
func Decode(data []byte, source string) (string, error) {
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		data = data[3:]
		if !utf8.Valid(data) {
			return "", configError(ErrorEncoding, "decode", source, "invalid byte sequence for UTF-8 BOM", nil)
		}
		return string(data), nil
	}
	for _, candidate := range []struct {
		bom    []byte
		width  int
		little bool
	}{
		{[]byte{0x00, 0x00, 0xfe, 0xff}, 4, false},
		{[]byte{0xff, 0xfe, 0x00, 0x00}, 4, true},
		{[]byte{0xff, 0xfe}, 2, true},
		{[]byte{0xfe, 0xff}, 2, false},
	} {
		if !bytes.HasPrefix(data, candidate.bom) {
			continue
		}
		decoded, err := decodeUnicode(data[len(candidate.bom):], candidate.width, candidate.little)
		if err != nil {
			return "", configError(ErrorEncoding, "decode", source, "invalid byte sequence for declared BOM", err)
		}
		return decoded, nil
	}

	name := codingDeclaration(data)
	switch name {
	case "", "utf-8", "utf8":
		if !utf8.Valid(data) {
			return "", configError(ErrorEncoding, "decode", source, "input is not valid UTF-8", nil)
		}
		return string(data), nil
	case "ascii", "us-ascii":
		for _, value := range data {
			if value >= utf8.RuneSelf {
				return "", configError(ErrorEncoding, "decode", source, "input is not valid ASCII", nil)
			}
		}
		return string(data), nil
	case "latin-1", "latin1", "iso-8859-1":
		runes := make([]rune, len(data))
		for index, value := range data {
			runes[index] = rune(value)
		}
		return string(runes), nil
	case "windows-1252", "cp1252":
		runes := make([]rune, 0, len(data))
		for _, value := range data {
			decoded, ok := decodeWindows1252(value)
			if !ok {
				return "", configError(ErrorEncoding, "decode", source, "invalid Windows-1252 input", nil)
			}
			runes = append(runes, decoded)
		}
		return string(runes), nil
	default:
		return "", configError(ErrorEncoding, "decode", source, "unsupported coding declaration", nil)
	}
}

func decodeUnicode(data []byte, width int, little bool) (string, error) {
	if len(data)%width != 0 {
		return "", fmt.Errorf("truncated code unit")
	}
	if width == 4 {
		runes := make([]rune, 0, len(data)/4)
		for index := 0; index < len(data); index += 4 {
			var value uint32
			if little {
				value = uint32(data[index]) | uint32(data[index+1])<<8 | uint32(data[index+2])<<16 | uint32(data[index+3])<<24
			} else {
				value = uint32(data[index])<<24 | uint32(data[index+1])<<16 | uint32(data[index+2])<<8 | uint32(data[index+3])
			}
			if value > utf8.MaxRune || value >= 0xd800 && value <= 0xdfff {
				return "", fmt.Errorf("invalid UTF-32 code point")
			}
			runes = append(runes, rune(value))
		}
		return string(runes), nil
	}
	units := make([]uint16, 0, len(data)/2)
	for index := 0; index < len(data); index += 2 {
		value := uint16(data[index])<<8 | uint16(data[index+1])
		if little {
			value = uint16(data[index]) | uint16(data[index+1])<<8
		}
		units = append(units, value)
	}
	for index := 0; index < len(units); index++ {
		if units[index] >= 0xd800 && units[index] <= 0xdbff {
			if index+1 >= len(units) || units[index+1] < 0xdc00 || units[index+1] > 0xdfff {
				return "", fmt.Errorf("invalid UTF-16 surrogate pair")
			}
			index++
		} else if units[index] >= 0xdc00 && units[index] <= 0xdfff {
			return "", fmt.Errorf("unexpected UTF-16 low surrogate")
		}
	}
	return string(utf16.Decode(units)), nil
}

func decodeWindows1252(value byte) (rune, bool) {
	if value < 0x80 || value >= 0xa0 {
		return rune(value), true
	}
	table := [...]rune{
		'€', 0, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
		0, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
	}
	decoded := table[value-0x80]
	return decoded, decoded != 0
}

func codingDeclaration(data []byte) string {
	if len(data) > 512 {
		data = data[:512]
	}
	data = bytes.ReplaceAll(data, []byte{0}, nil)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if !bytes.HasPrefix(line, []byte{'#'}) {
			continue
		}
		line = bytes.TrimSpace(line[1:])
		parts := bytes.SplitN(line, []byte{':'}, 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(string(parts[0])), "coding") {
			fields := strings.Fields(string(parts[1]))
			if len(fields) == 1 {
				return strings.ToLower(fields[0])
			}
		}
	}
	return ""
}

// Tokenize applies POSIX shlex-compatible comments, quoting, and escaping.
func Tokenize(text, source string, limits Limits) ([]Token, error) {
	limits = normalizeLimits(limits)
	var tokens []Token
	var word strings.Builder
	line, column := 1, 1
	startLine, startColumn := 0, 0
	inWord := false
	quote := rune(0)
	escaped := false
	runes := []rune(text)

	emit := func() error {
		if !inWord {
			return nil
		}
		value := word.String()
		if len(value) > limits.MaxTokenBytes {
			return tokenError(ErrorResource, "tokenize", Token{Source: source, Line: startLine, Column: startColumn}, "token exceeds byte limit")
		}
		tokens = append(tokens, Token{Value: value, Source: source, Line: startLine, Column: startColumn})
		if len(tokens) > limits.MaxTokens {
			return configError(ErrorResource, "tokenize", source, "token count exceeds limit", nil)
		}
		word.Reset()
		inWord = false
		return nil
	}
	begin := func() {
		if !inWord {
			inWord, startLine, startColumn = true, line, column
		}
	}

	for index := 0; index < len(runes); index++ {
		current := runes[index]
		if escaped {
			if current != '\n' {
				word.WriteRune(current)
			}
			escaped = false
		} else if quote == '\'' {
			if current == '\'' {
				quote = 0
			} else {
				word.WriteRune(current)
			}
		} else if quote == '"' {
			switch current {
			case '"':
				quote = 0
			case '\\':
				if index+1 < len(runes) {
					next := runes[index+1]
					if next == '\\' || next == '"' || next == '$' || next == '`' || next == '\n' {
						escaped = true
					} else {
						word.WriteRune(current)
					}
				} else {
					word.WriteRune(current)
				}
			default:
				word.WriteRune(current)
			}
		} else {
			switch {
			case current == '#':
				if err := emit(); err != nil {
					return nil, err
				}
				for index+1 < len(runes) && runes[index+1] != '\n' {
					index++
					column++
				}
			case stdunicode.IsSpace(current):
				if err := emit(); err != nil {
					return nil, err
				}
			case current == '\\':
				begin()
				escaped = true
			case current == '\'' || current == '"':
				begin()
				quote = current
			default:
				begin()
				word.WriteRune(current)
			}
		}

		if current == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	if escaped {
		return nil, &Error{Category: ErrorSyntax, Op: "tokenize", Source: source, Line: line, Column: column, Message: "trailing escape"}
	}
	if quote != 0 {
		return nil, &Error{Category: ErrorSyntax, Op: "tokenize", Source: source, Line: startLine, Column: startColumn, Message: "unterminated quote"}
	}
	if err := emit(); err != nil {
		return nil, err
	}
	return tokens, nil
}

func normalizeLimits(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaults.MaxFiles
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxTokens <= 0 {
		limits.MaxTokens = defaults.MaxTokens
	}
	if limits.MaxTokenBytes <= 0 {
		limits.MaxTokenBytes = defaults.MaxTokenBytes
	}
	if limits.MaxPathBytes <= 0 {
		limits.MaxPathBytes = defaults.MaxPathBytes
	}
	if limits.MaxAliasTriggers <= 0 {
		limits.MaxAliasTriggers = defaults.MaxAliasTriggers
	}
	return limits
}
