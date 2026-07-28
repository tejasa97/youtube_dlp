package format

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
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
	OK            bool
	Best          bool
	Media         AtomMedia
	Star          bool
	Index         int // one-based; Canonical omits ".1"
	indexText     string
	indexTooLarge bool
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
	if atom.indexText != "" && atom.indexText != "1" {
		builder.WriteByte('.')
		builder.WriteString(atom.indexText)
	} else if atom.Index > 1 {
		builder.WriteByte('.')
		builder.WriteString(strconv.Itoa(atom.Index))
	}
	return builder.String()
}

// parseAtomToken matches the pinned quality-atom regex exactly:
//
//	(?P<bw>best|worst|b|w)(?P<type>video|audio|v|a)?(?P<mod>\*)?(?:\.(?P<n>[1-9]\d*))?$
//
// Strings that look alias-like but fail that regex return (!OK, nil) so the
// caller can fall back to a direct format ID (best*foo, best.01, best.0, best.).
func parseAtomToken(name string, absStart int) (Atom, error) {
	_ = absStart
	atom, rest, _, ok := cutAtomStem(name)
	if !ok {
		return Atom{}, nil
	}
	if rest == "" {
		atom.OK = true
		return atom, nil
	}
	if rest[0] != '.' || len(rest) == 1 {
		return Atom{}, nil
	}
	digits := rest[1:]
	if digits[0] < '1' || digits[0] > '9' {
		return Atom{}, nil
	}
	for index := range digits {
		if digits[index] < '0' || digits[index] > '9' {
			return Atom{}, nil
		}
	}
	index, tooLarge := parseAtomIndex(digits)
	atom.OK = true
	atom.Index = index
	atom.indexText = digits
	atom.indexTooLarge = tooLarge
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

func parseAtomIndex(digits string) (int, bool) {
	const maxInt = int(^uint(0) >> 1)
	value := 0
	for index := range digits {
		digit := int(digits[index] - '0')
		if value > (maxInt-digit)/10 {
			return maxInt, true
		}
		value = value*10 + digit
	}
	return value, false
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
	if !validDirectIDToken(text) {
		return atomSpec{}, selectorSyntax(absStart, absStart+len(text), fmt.Sprintf("unknown term %q", text))
	}
	return atomSpec{kind: atomDirectID, text: text}, nil
}

// validDirectIDToken mirrors NAME, NUMBER, and non-structural OP tokens retained
// by the pinned Python tokenizer. Error, comment, and string-token punctuation is
// rejected instead of being silently discarded and changing the requested ID.
func validDirectIDToken(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_' {
			continue
		}
		switch r {
		// Includes '/' so joined Python OP tokens such as "//" and "/=" can form
		// direct IDs (best//). '+' appears only via joinable "+=" and is allowed
		// for the same reason. ERRORTOKEN/comment/string punctuation stays out.
		case '-', '.', '*', '/', '+', ':', '%', '&', ';', '<', '=', '>', '@', '^', '|', '~', '{', '}':
			continue
		default:
			return false
		}
	}
	return true
}
