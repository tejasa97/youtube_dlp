package engine

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

func ExtractJSONObject(page []byte, marker string) ([]byte, error) {
	markerIndex := strings.Index(string(page), marker)
	if markerIndex < 0 {
		return nil, fmt.Errorf("marker %q not found", marker)
	}
	raw, _, err := ExtractJSONObjectFrom(page, markerIndex+len(marker), 0)
	return raw, err
}

func ExtractJSONObjectFrom(page []byte, offset, maxStartOffset int) ([]byte, int, error) {
	if offset < 0 || offset > len(page) {
		return nil, 0, errors.New("invalid JSON search offset")
	}
	startOffset := bytes.IndexByte(page[offset:], '{')
	if startOffset < 0 {
		return nil, 0, errors.New("JSON object start not found")
	}
	if maxStartOffset > 0 && startOffset > maxStartOffset {
		return nil, 0, errors.New("JSON object start is too far from marker")
	}
	start := offset + startOffset
	depth := 0
	inString, escaped := false, false
	for index := start; index < len(page); index++ {
		character := page[index]
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
		switch character {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return page[start : index+1], index + 1, nil
			}
		}
	}
	return nil, 0, errors.New("JSON object is not closed")
}
