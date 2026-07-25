package format

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	maxAtomIndex       = 1000
	maxAtomIndexDigits = 4
)

// AtomMedia selects which media-bearing formats an atom may match.
type AtomMedia uint8

const (
	// AtomMediaCombined is best/worst without a video/audio type.
	AtomMediaCombined AtomMedia = iota
	AtomMediaVideo
	AtomMediaAudio
)

// Atom is the typed best/worst selector form.
type Atom struct {
	OK    bool
	Best  bool
	Media AtomMedia
	Star  bool
	Index int // one-based; Canonical omits ".1"
}

// Canonical returns the long-form atom text. Index 1 is omitted.
func (atom Atom) Canonical() string {
	if !atom.OK {
		return ""
	}
	var builder strings.Builder
	if atom.Best {
		builder.WriteString("best")
	} else {
		builder.WriteString("worst")
	}
	switch atom.Media {
	case AtomMediaVideo:
		builder.WriteString("video")
	case AtomMediaAudio:
		builder.WriteString("audio")
	}
	if atom.Star {
		builder.WriteByte('*')
	}
	if atom.Index > 1 {
		builder.WriteByte('.')
		builder.WriteString(strconv.Itoa(atom.Index))
	}
	return builder.String()
}

func parseAtomToken(name string, absStart int) (Atom, error) {
	atom, rest, stemEnd, ok := cutAtomStem(name)
	if !ok {
		return Atom{}, nil
	}
	if rest == "" {
		atom.OK = true
		if atom.Index == 0 {
			atom.Index = 1
		}
		return atom, nil
	}
	if rest[0] != '.' {
		if atom.Star {
			return Atom{}, selectorSyntax(absStart+stemEnd, absStart+len(name), fmt.Sprintf("unexpected atom suffix %q", rest))
		}
		return Atom{}, nil
	}
	indexStart := stemEnd
	if len(rest) == 1 {
		return Atom{}, selectorSyntax(absStart+indexStart, absStart+len(name), "missing atom index")
	}
	switch rest[1] {
	case '+', '-':
		return Atom{}, selectorSyntax(absStart+indexStart, absStart+len(name), "atom index must not have a sign")
	case '0':
		return Atom{}, selectorSyntax(absStart+indexStart, absStart+len(name), "atom index must be a positive integer without leading zeros")
	}
	if rest[1] < '1' || rest[1] > '9' {
		return Atom{}, nil
	}
	index, consumed, err := parseAtomIndex(rest[1:])
	if err != nil {
		return Atom{}, selectorSyntax(absStart+indexStart, absStart+len(name), err.Error())
	}
	if 1+consumed != len(rest) {
		return Atom{}, selectorSyntax(absStart+indexStart+1+consumed, absStart+len(name), fmt.Sprintf("unexpected atom suffix %q", rest[1+consumed:]))
	}
	atom.OK = true
	atom.Index = index
	return atom, nil
}

func cutAtomStem(name string) (atom Atom, rest string, stemEnd int, ok bool) {
	best, prefixLen, ok := cutQualityPrefix(name)
	if !ok {
		return Atom{}, name, 0, false
	}
	atom.Best = best
	atom.Index = 1
	rest = name[prefixLen:]
	stemEnd = prefixLen
	if media, mediaLen, mediaOK := cutMediaPrefix(rest); mediaOK {
		atom.Media = media
		rest = rest[mediaLen:]
		stemEnd += mediaLen
	}
	if strings.HasPrefix(rest, "*") {
		atom.Star = true
		rest = rest[1:]
		stemEnd++
	}
	return atom, rest, stemEnd, true
}

func cutQualityPrefix(name string) (best bool, n int, ok bool) {
	switch {
	case strings.HasPrefix(name, "best"):
		return true, 4, true
	case strings.HasPrefix(name, "worst"):
		return false, 5, true
	case strings.HasPrefix(name, "b"):
		return true, 1, true
	case strings.HasPrefix(name, "w"):
		return false, 1, true
	default:
		return false, 0, false
	}
}

func cutMediaPrefix(name string) (AtomMedia, int, bool) {
	switch {
	case strings.HasPrefix(name, "video"):
		return AtomMediaVideo, 5, true
	case strings.HasPrefix(name, "audio"):
		return AtomMediaAudio, 5, true
	case strings.HasPrefix(name, "v"):
		return AtomMediaVideo, 1, true
	case strings.HasPrefix(name, "a"):
		return AtomMediaAudio, 1, true
	default:
		return AtomMediaCombined, 0, false
	}
}

func parseAtomIndex(digits string) (int, int, error) {
	if digits == "" {
		return 0, 0, errors.New("missing atom index")
	}
	switch digits[0] {
	case '+', '-':
		return 0, 0, errors.New("atom index must not have a sign")
	case '0':
		return 0, 0, errors.New("atom index must be a positive integer without leading zeros")
	}
	if digits[0] < '1' || digits[0] > '9' {
		return 0, 0, errors.New("atom index must be a positive integer")
	}
	value := 0
	consumed := 0
	for consumed < len(digits) {
		digit := digits[consumed]
		if digit < '0' || digit > '9' {
			break
		}
		consumed++
		if consumed > maxAtomIndexDigits {
			return 0, 0, errors.New("atom index has too many digits")
		}
		value = value*10 + int(digit-'0')
		if value > maxAtomIndex {
			return 0, 0, fmt.Errorf("atom index exceeds maximum %d", maxAtomIndex)
		}
	}
	if consumed == 0 {
		return 0, 0, errors.New("atom index must be a positive integer")
	}
	return value, consumed, nil
}

func resolveLegacyAtomName(name string) (Atom, bool) {
	switch name {
	case "best":
		return Atom{OK: true, Best: true, Index: 1}, true
	case "worst":
		return Atom{OK: true, Best: false, Index: 1}, true
	case "bestvideo":
		return Atom{OK: true, Best: true, Media: AtomMediaVideo, Index: 1}, true
	case "worstvideo":
		return Atom{OK: true, Best: false, Media: AtomMediaVideo, Index: 1}, true
	case "bestaudio":
		return Atom{OK: true, Best: true, Media: AtomMediaAudio, Index: 1}, true
	case "worstaudio":
		return Atom{OK: true, Best: false, Media: AtomMediaAudio, Index: 1}, true
	default:
		atom, err := parseAtomToken(name, 0)
		if err != nil || !atom.OK {
			return Atom{}, false
		}
		if atom.Index < 1 {
			atom.Index = 1
		}
		return atom, true
	}
}

func parseAtomSpec(text string, absStart int) (atomSpec, error) {
	if text == "" {
		return atomSpec{}, selectorSyntax(absStart, absStart, "empty atom")
	}
	if kind, ok := classifyExtensionToken(text); ok {
		return atomSpec{kind: kind, text: text}, nil
	}
	if atom, err := parseAtomToken(text, absStart); err != nil {
		return atomSpec{}, err
	} else if atom.OK {
		return atomSpec{kind: atomQuality, quality: atom}, nil
	}
	if !formatIDPattern.MatchString(text) {
		return atomSpec{}, selectorSyntax(absStart, absStart+len(text), fmt.Sprintf("unknown term %q", text))
	}
	return atomSpec{kind: atomDirectID, text: text}, nil
}
