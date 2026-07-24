package format

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrInvalidSelector = errors.New("invalid format selector")
	ErrNoMatch         = errors.New("no format matches selector")
)

const (
	maxSelectorBytes   = 16 << 10
	maxAlternatives    = 64
	maxMergeTerms      = 16
	maxTermFilters     = 32
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

// Atom is the typed best/worst selector form. The zero value is not an atom and
// keeps existing Term{Name: ...} literals safe: selection resolves known legacy
// names from Name when OK is false.
type Atom struct {
	OK    bool
	Best  bool
	Media AtomMedia
	Star  bool
	Index int // one-based; Canonical omits ".1"
}

type Selector struct {
	Alternatives []Choice
}

type Choice struct {
	Terms []Term
}

type Term struct {
	Name    string
	Atom    Atom
	Filters []Filter
}

type Filter struct {
	Field    string
	Operator string
	Value    string
}

// SyntaxError identifies the exact byte range rejected by the pilot parser.
type SyntaxError struct {
	Start   int
	End     int
	Message string
}

func (err *SyntaxError) Error() string {
	return fmt.Sprintf("%v at bytes %d:%d: %s", ErrInvalidSelector, err.Start, err.End, err.Message)
}

func (err *SyntaxError) Unwrap() error { return ErrInvalidSelector }

type selectorSegment struct {
	text       string
	start, end int
}

func ParseSelector(input string) (Selector, error) {
	if len(input) > maxSelectorBytes {
		return Selector{}, selectorSyntax(0, len(input), "selector exceeds size limit")
	}
	root := trimSelectorSegment(selectorSegment{text: input, start: 0, end: len(input)})
	if root.text == "" {
		return Selector{}, selectorSyntax(root.start, root.end, "selector is empty")
	}
	alternatives, err := splitTopLevel(root, '/')
	if err != nil {
		return Selector{}, err
	}
	if len(alternatives) > maxAlternatives {
		return Selector{}, selectorSyntax(root.start, root.end, "too many fallback alternatives")
	}
	selector := Selector{}
	for _, alternative := range alternatives {
		parts, err := splitTopLevel(alternative, '+')
		if err != nil {
			return Selector{}, err
		}
		if len(parts) > maxMergeTerms {
			return Selector{}, selectorSyntax(alternative.start, alternative.end, "too many merge terms")
		}
		choice := Choice{}
		for _, part := range parts {
			term, err := parseTerm(trimSelectorSegment(part))
			if err != nil {
				return Selector{}, err
			}
			choice.Terms = append(choice.Terms, term)
		}
		selector.Alternatives = append(selector.Alternatives, choice)
	}
	return selector, nil
}

func Select(info value.Info, selector Selector) ([]Selection, error) {
	return SelectWithOptions(info, selector, Options{})
}

// SelectWithOptions applies an explicit deterministic preference policy before
// evaluating a selector. It never mutates extractor metadata.
func SelectWithOptions(info value.Info, selector Selector, options Options) ([]Selection, error) {
	formats, ok := info.Formats()
	if !ok {
		return nil, ErrNoFormats
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	objects := make([]*value.Object, 0, len(formats))
	for _, item := range formats {
		if object, ok := item.Object(); ok {
			if !options.AllowDRM && isDRM(object) {
				continue
			}
			objects = append(objects, object)
		}
	}
	objects = orderFormats(objects, options)
	// incompleteFormats mirrors yt-dlp _select_formats: computed once from the
	// post-DRM available universe. Atom filters replace only the candidate list.
	incomplete := incompleteFormats(objects)
	for _, alternative := range selector.Alternatives {
		var selected []Selection
		matched := true
		for _, term := range alternative.Terms {
			if term.Name == "all" {
				count := 0
				for _, candidate := range objects {
					if matchesFilters(candidate, term.Filters) {
						selected = append(selected, objectSelection(candidate))
						count++
					}
				}
				if count == 0 {
					matched = false
				}
				continue
			}
			selection, ok := selectTermWithOptions(objects, term, options, incomplete)
			if !ok {
				matched = false
				break
			}
			selected = append(selected, selection)
		}
		if matched {
			if err := attachHeaders(info, selected, objects); err != nil {
				return nil, err
			}
			return selected, nil
		}
	}
	return nil, ErrNoMatch
}

// attachHeaders gives selector results the same download-header semantics as
// Best: video-level headers are inherited and per-format values override them.
// Selections carry their own header maps so callers may safely mutate one.
func attachHeaders(info value.Info, selections []Selection, formats []*value.Object) error {
	for index := range selections {
		object := selectedObject(selections[index], formats)
		if object == nil {
			return fmt.Errorf("%w: selected format metadata is unavailable", ErrNoFormats)
		}
		headers, err := mergeHeaders(info.Lookup("http_headers"), object.Lookup("http_headers"))
		if err != nil {
			return err
		}
		selections[index].Headers = headers
	}
	return nil
}

func selectedObject(selection Selection, formats []*value.Object) *value.Object {
	for _, candidate := range formats {
		id, _ := candidate.Lookup("format_id").StringValue()
		if id != selection.ID {
			continue
		}
		if selection.YouTubeSABR {
			sabr, _ := candidate.Lookup("_youtube_sabr").Bool()
			if sabr {
				return candidate
			}
			continue
		}
		url, _ := candidate.Lookup("url").StringValue()
		if url == selection.URL {
			return candidate
		}
	}
	return nil
}

func parseTerm(segment selectorSegment) (Term, error) {
	if segment.text == "" {
		return Term{}, selectorSyntax(segment.start, segment.end, "empty term")
	}
	open := strings.IndexByte(segment.text, '[')
	name := segment.text
	remaining := ""
	remainingStart := segment.end
	if open >= 0 {
		name, remaining = segment.text[:open], segment.text[open:]
		remainingStart = segment.start + open
	}
	term, err := parseTermName(name, segment.start)
	if err != nil {
		return Term{}, err
	}
	for remaining != "" {
		if remaining[0] != '[' {
			return Term{}, selectorSyntax(remainingStart, segment.end, fmt.Sprintf("unexpected text %q", remaining))
		}
		close := strings.IndexByte(remaining, ']')
		if close < 0 {
			return Term{}, selectorSyntax(remainingStart, segment.end, "unclosed filter")
		}
		filter, err := parseFilter(remaining[1:close], remainingStart+1)
		if err != nil {
			return Term{}, err
		}
		term.Filters = append(term.Filters, filter)
		if len(term.Filters) > maxTermFilters {
			return Term{}, selectorSyntax(segment.start, segment.end, "too many filters")
		}
		remaining = remaining[close+1:]
		remainingStart += close + 1
	}
	return term, nil
}

func parseTermName(name string, absStart int) (Term, error) {
	if name == "all" {
		return Term{Name: "all"}, nil
	}
	if atom, err := parseAtomToken(name, absStart); err != nil {
		return Term{}, err
	} else if atom.OK {
		return Term{Name: atom.Canonical(), Atom: atom}, nil
	}
	if !formatIDPattern.MatchString(name) {
		return Term{}, selectorSyntax(absStart, absStart+len(name), fmt.Sprintf("unknown term %q", name))
	}
	return Term{Name: name}, nil
}

// Canonical returns the long-form atom text. Index 1 is omitted so N=1 equals
// the unindexed atom string.
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
			// Reserved star modifier with junk (bv*x) is a malformed atom.
			return Atom{}, selectorSyntax(absStart+stemEnd, absStart+len(name), fmt.Sprintf("unexpected atom suffix %q", rest))
		}
		// Ordinary IDs that merely begin with best/worst/b/w (wav, bestx,
		// bestvideox) stay direct format IDs unless they use * or .N.
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
		// Dotted tokens that are not numeric indexes stay direct format IDs
		// (for example "18.1" or "best.mp4").
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

func (term Term) resolveAtom() (Atom, bool) {
	if term.Atom.OK {
		atom := term.Atom
		if atom.Index < 1 {
			atom.Index = 1
		}
		return atom, true
	}
	if term.Name == "" || term.Name == "all" {
		return Atom{}, false
	}
	atom, err := parseAtomToken(term.Name, 0)
	if err != nil || !atom.OK {
		// Legacy literals use long names without Atom set.
		switch term.Name {
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
			return Atom{}, false
		}
	}
	if atom.Index < 1 {
		atom.Index = 1
	}
	return atom, true
}

var (
	fieldPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	formatIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

func parseFilter(input string, start int) (Filter, error) {
	for _, operator := range []string{"!=", ">=", "<=", "^=", "$=", "*=", "~=", "=", ">", "<"} {
		if index := strings.Index(input, operator); index > 0 {
			field := strings.TrimSpace(input[:index])
			filterValue := strings.TrimSpace(input[index+len(operator):])
			if !fieldPattern.MatchString(field) || filterValue == "" {
				return Filter{}, selectorSyntax(start, start+len(input), fmt.Sprintf("malformed filter %q", input))
			}
			if len(filterValue) >= 2 && ((filterValue[0] == '"' && filterValue[len(filterValue)-1] == '"') || (filterValue[0] == '\'' && filterValue[len(filterValue)-1] == '\'')) {
				filterValue = filterValue[1 : len(filterValue)-1]
			}
			if len(filterValue) > maxSelectorBytes/2 {
				return Filter{}, selectorSyntax(start+index+len(operator), start+len(input), "filter value exceeds size limit")
			}
			if operator == "~=" {
				if len(filterValue) > maxRegexBytes {
					return Filter{}, selectorSyntax(start+index+len(operator), start+len(input), "regular expression exceeds size limit")
				}
				if _, err := regexp.Compile(filterValue); err != nil {
					return Filter{}, selectorSyntax(start+index+len(operator), start+len(input), "invalid regular expression")
				}
			}
			return Filter{Field: field, Operator: operator, Value: filterValue}, nil
		}
	}
	return Filter{}, selectorSyntax(start, start+len(input), fmt.Sprintf("filter %q has no operator", input))
}

func splitTopLevel(input selectorSegment, separator byte) ([]selectorSegment, error) {
	depth := 0
	start := 0
	lastOpen := -1
	var result []selectorSegment
	for index := 0; index < len(input.text); index++ {
		switch input.text[index] {
		case '[':
			depth++
			lastOpen = index
		case ']':
			depth--
			if depth < 0 {
				return nil, selectorSyntax(input.start+index, input.start+index+1, "unexpected ]")
			}
		default:
			if input.text[index] == separator && depth == 0 {
				result = append(result, selectorSegment{text: input.text[start:index], start: input.start + start, end: input.start + index})
				start = index + 1
			}
		}
	}
	if depth != 0 {
		return nil, selectorSyntax(input.start+lastOpen, input.end, "unclosed filter")
	}
	result = append(result, selectorSegment{text: input.text[start:], start: input.start + start, end: input.end})
	return result, nil
}

func trimSelectorSegment(segment selectorSegment) selectorSegment {
	left := len(segment.text) - len(strings.TrimLeft(segment.text, " \t\r\n"))
	rightText := strings.TrimRight(segment.text[left:], " \t\r\n")
	return selectorSegment{text: rightText, start: segment.start + left, end: segment.start + left + len(rightText)}
}

func selectorSyntax(start, end int, message string) error {
	if end < start {
		end = start
	}
	return &SyntaxError{Start: start, End: end, Message: message}
}

func selectTerm(formats []*value.Object, term Term) (Selection, bool) {
	return selectTermWithOptions(formats, term, Options{}, incompleteFormats(formats))
}

func selectTermWithOptions(formats []*value.Object, term Term, options Options, incomplete bool) (Selection, bool) {
	atom, isAtom := term.resolveAtom()
	if !isAtom {
		for _, candidate := range formats {
			id, _ := candidate.Lookup("format_id").StringValue()
			if id == term.Name && matchesFilters(candidate, term.Filters) {
				return objectSelection(candidate), true
			}
		}
		return Selection{}, false
	}
	filtered := make([]*value.Object, 0, len(formats))
	for _, candidate := range formats {
		if matchesFilters(candidate, term.Filters) {
			filtered = append(filtered, candidate)
		}
	}
	matches := collectAtomMatches(filtered, atom)
	if len(matches) == 0 && atomAllowsIncompleteFallback(atom) && incomplete {
		matches = collectPlayable(filtered)
	}
	if len(matches) == 0 {
		return Selection{}, false
	}
	ordered := orderAtomMatches(matches, atom, options)
	index := atom.Index
	if index < 1 {
		index = 1
	}
	if index > len(ordered) {
		return Selection{}, false
	}
	if atom.Best {
		return objectSelection(ordered[index-1]), true
	}
	return objectSelection(ordered[len(ordered)-index]), true
}

func atomAllowsIncompleteFallback(atom Atom) bool {
	return atom.Media == AtomMediaCombined && !atom.Star
}

// incompleteFormats mirrors yt-dlp's incomplete_formats predicate: every
// available format is explicitly video-less or every available format is
// explicitly audio-less. Callers compute it once from the post-DRM universe.
func incompleteFormats(formats []*value.Object) bool {
	if len(formats) == 0 {
		return false
	}
	allVideoNone := true
	allAudioNone := true
	for _, candidate := range formats {
		if !codecExplicitlyNone(candidate, "vcodec") {
			allVideoNone = false
		}
		if !codecExplicitlyNone(candidate, "acodec") {
			allAudioNone = false
		}
		if !allVideoNone && !allAudioNone {
			return false
		}
	}
	return allVideoNone || allAudioNone
}

func collectAtomMatches(formats []*value.Object, atom Atom) []*value.Object {
	matches := make([]*value.Object, 0, len(formats))
	for _, candidate := range formats {
		if atomMatches(candidate, atom) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func collectPlayable(formats []*value.Object) []*value.Object {
	matches := make([]*value.Object, 0, len(formats))
	for _, candidate := range formats {
		if codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec") {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func atomMatches(candidate *value.Object, atom Atom) bool {
	switch {
	case atom.Media == AtomMediaCombined && !atom.Star:
		// Plain best/worst: combined A+V. Missing codec keys are not "none",
		// matching yt-dlp's != 'none' checks so codec-less progressive formats
		// remain selectable.
		return codecNotNone(candidate, "vcodec") && codecNotNone(candidate, "acodec")
	case atom.Media == AtomMediaCombined && atom.Star:
		return codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec")
	case atom.Media == AtomMediaVideo && atom.Star:
		hasVideo, _ := candidateMediaKinds(candidate)
		return hasVideo && (codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec"))
	case atom.Media == AtomMediaAudio && atom.Star:
		_, hasAudio := candidateMediaKinds(candidate)
		return hasAudio && (codecNotNone(candidate, "vcodec") || codecNotNone(candidate, "acodec"))
	case atom.Media == AtomMediaVideo:
		return candidateMatchesKind(candidate, true, false, nil)
	case atom.Media == AtomMediaAudio:
		return candidateMatchesKind(candidate, false, true, nil)
	default:
		return false
	}
}

func codecNotNone(object *value.Object, key string) bool {
	text, ok := object.Lookup(key).StringValue()
	return !ok || text != "none"
}

func codecExplicitlyNone(object *value.Object, key string) bool {
	text, ok := object.Lookup(key).StringValue()
	return ok && text == "none"
}

func orderAtomMatches(matches []*value.Object, atom Atom, options Options) []*value.Object {
	ordered := append([]*value.Object(nil), matches...)
	if len(options.Sort) > 0 {
		// matches already appear in orderFormats preference order; keep that
		// relative order and do not re-sort (stable source-order ties).
		return ordered
	}
	wantVideo := atom.Media == AtomMediaVideo
	wantAudio := atom.Media == AtomMediaAudio
	sort.SliceStable(ordered, func(left, right int) bool {
		l, r := ordered[left], ordered[right]
		if lp, rp := extractorPreference(l), extractorPreference(r); lp != rp {
			return lp > rp
		}
		ls, rs := formatScore(l, wantVideo, wantAudio), formatScore(r, wantVideo, wantAudio)
		if ls != rs {
			return ls > rs
		}
		if lr, rr := preferenceRank(l, options), preferenceRank(r, options); lr != rr {
			return lr > rr
		}
		return false
	})
	return ordered
}

func preferenceRank(object *value.Object, options Options) int {
	rank := extensionRank(object, options.PreferExtensions) * 2
	if options.PreferFreeFormats {
		rank += freeRank(object)
	}
	return rank
}

func candidateMatchesKind(candidate *value.Object, wantVideo, wantAudio bool, filters []Filter) bool {
	hasVideo, hasAudio := candidateMediaKinds(candidate)
	return (!wantVideo || hasVideo && !hasAudio) && (!wantAudio || hasAudio && !hasVideo) && matchesFilters(candidate, filters)
}

// An explicit absent side is enough to classify a track even when an
// extractor cannot name the present codec. This matches yt-dlp's bestvideo and
// bestaudio treatment of acodec=none and vcodec=none respectively.
func candidateMediaKinds(candidate *value.Object) (hasVideo, hasAudio bool) {
	vcodec, _ := candidate.Lookup("vcodec").StringValue()
	acodec, _ := candidate.Lookup("acodec").StringValue()
	hasVideo = vcodec != "none" && (vcodec != "" || acodec == "none")
	hasAudio = acodec != "none" && (acodec != "" || vcodec == "none")
	return hasVideo, hasAudio
}

func matchesFilters(object *value.Object, filters []Filter) bool {
	for _, filter := range filters {
		input := object.Lookup(filter.Field)
		stringValue, stringOK := input.StringValue()
		numericValue, numericOK := numeric(input)
		filterNumber, numberErr := parseBoundedNumber(filter.Value)
		var matched bool
		switch filter.Operator {
		case "=":
			matched = (stringOK && stringValue == filter.Value) || (numericOK && numberErr == nil && numericValue == filterNumber)
		case "!=":
			if numericOK && numberErr == nil {
				matched = numericValue != filterNumber
			} else {
				matched = !stringOK || stringValue != filter.Value
			}
		case "^=":
			matched = stringOK && strings.HasPrefix(stringValue, filter.Value)
		case "$=":
			matched = stringOK && strings.HasSuffix(stringValue, filter.Value)
		case "*=":
			matched = stringOK && strings.Contains(stringValue, filter.Value)
		case "~=":
			if len(filter.Value) > maxRegexBytes {
				return false
			}
			expression, err := regexp.Compile(filter.Value)
			matched = err == nil && stringOK && expression.MatchString(stringValue)
		case ">", ">=", "<", "<=":
			if numericOK && numberErr == nil {
				switch filter.Operator {
				case ">":
					matched = numericValue > filterNumber
				case ">=":
					matched = numericValue >= filterNumber
				case "<":
					matched = numericValue < filterNumber
				case "<=":
					matched = numericValue <= filterNumber
				}
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func formatScore(object *value.Object, video, audio bool) float64 {
	height, _ := numeric(object.Lookup("height"))
	tbr, _ := numeric(object.Lookup("tbr"))
	abr, _ := numeric(object.Lookup("abr"))
	filesize, _ := numeric(object.Lookup("filesize"))
	if audio && !video {
		return abr*1e9 + tbr*1e6 + filesize
	}
	return height*1e12 + tbr*1e6 + filesize
}

func objectSelection(object *value.Object) Selection {
	selection := Selection{}
	selection.ID, _ = object.Lookup("format_id").StringValue()
	selection.URL, _ = object.Lookup("url").StringValue()
	selection.Ext, _ = object.Lookup("ext").StringValue()
	selection.Filesize, _ = object.Lookup("filesize").Int()
	selection.Protocol, _ = object.Lookup("protocol").StringValue()
	selection.VCodec, _ = object.Lookup("vcodec").StringValue()
	selection.ACodec, _ = object.Lookup("acodec").StringValue()
	selection.Height, _ = object.Lookup("height").Int()
	selection.TBR, _ = numeric(object.Lookup("tbr"))
	selection.YouTubePostLive, _ = object.Lookup("_youtube_post_live").Bool()
	selection.YouTubeLiveFromStart, _ = object.Lookup("_youtube_live_from_start").Bool()
	selection.YouTubeItag, _ = object.Lookup("_youtube_itag").Int()
	selection.YouTubeClient, _ = object.Lookup("_youtube_client").StringValue()
	selection.YouTubeSourceURL, _ = object.Lookup("_youtube_source_url").StringValue()
	selection.TargetDuration, _ = numeric(object.Lookup("target_duration"))
	selection.LiveStartTimestamp, _ = object.Lookup("live_start_timestamp").Int()
	selection.YouTubeSABR, _ = object.Lookup("_youtube_sabr").Bool()
	selection.YouTubeSABRTrack, _ = object.Lookup("_youtube_sabr_track").StringValue()
	selection.YouTubeSABRItag, _ = object.Lookup("_youtube_sabr_itag").Int()
	selection.YouTubeSABRLastModified, _ = object.Lookup("_youtube_sabr_last_modified").Int()
	selection.YouTubeSABRXTags, _ = object.Lookup("_youtube_sabr_xtags").StringValue()
	selection.YouTubeSABRServerURL, _ = object.Lookup("_youtube_sabr_server_url").StringValue()
	selection.YouTubeSABRUstreamerConfig, _ = object.Lookup("_youtube_sabr_ustreamer_config").StringValue()
	selection.YouTubeSABRClientID, _ = object.Lookup("_youtube_sabr_client_id").Int()
	selection.YouTubeSABRClientVersion, _ = object.Lookup("_youtube_sabr_client_version").StringValue()
	selection.YouTubeSABRUserAgent, _ = object.Lookup("_youtube_sabr_user_agent").StringValue()
	selection.YouTubeSABRVisitorData, _ = object.Lookup("_youtube_sabr_visitor_data").StringValue()
	selection.YouTubeSABRDurationSec, _ = object.Lookup("_youtube_sabr_duration_sec").Int()
	selection.YouTubeSABRVideoID, _ = object.Lookup("_youtube_sabr_video_id").StringValue()
	selection.YouTubeSABRClientName, _ = object.Lookup("_youtube_client").StringValue()
	selection.YouTubeSABRDrc, _ = object.Lookup("_youtube_sabr_drc").Bool()
	selection.YouTubeSABRAudioTrackID, _ = object.Lookup("_youtube_sabr_audio_track_id").StringValue()
	if selection.YouTubeSABR {
		selection.Protocol = "youtube_sabr_ump"
	}
	return selection
}
