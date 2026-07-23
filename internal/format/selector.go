package format

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrInvalidSelector = errors.New("invalid format selector")
	ErrNoMatch         = errors.New("no format matches selector")
)

const (
	maxSelectorBytes = 16 << 10
	maxAlternatives  = 64
	maxMergeTerms    = 16
	maxTermFilters   = 32
)

type Selector struct {
	Alternatives []Choice
}

type Choice struct {
	Terms []Term
}

type Term struct {
	Name    string
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
			selection, ok := selectTermWithOptions(objects, term, options)
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
		url, _ := candidate.Lookup("url").StringValue()
		if id == selection.ID && url == selection.URL {
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
	switch name {
	case "best", "worst", "bestvideo", "worstvideo", "bestaudio", "worstaudio", "all":
	default:
		if !formatIDPattern.MatchString(name) {
			return Term{}, selectorSyntax(segment.start, segment.start+len(name), fmt.Sprintf("unknown term %q", name))
		}
	}
	term := Term{Name: name}
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
	if term.Name != "best" && term.Name != "worst" && !strings.HasPrefix(term.Name, "best") && !strings.HasPrefix(term.Name, "worst") {
		for _, candidate := range formats {
			id, _ := candidate.Lookup("format_id").StringValue()
			if id == term.Name && matchesFilters(candidate, term.Filters) {
				return objectSelection(candidate), true
			}
		}
		return Selection{}, false
	}
	wantBest := strings.HasPrefix(term.Name, "best")
	wantVideo := strings.HasSuffix(term.Name, "video")
	wantAudio := strings.HasSuffix(term.Name, "audio")
	var selected *value.Object
	var selectedScore float64
	var selectedPreference float64
	for _, candidate := range formats {
		hasVideo, hasAudio := candidateMediaKinds(candidate)
		if wantVideo && (!hasVideo || hasAudio) || wantAudio && (!hasAudio || hasVideo) {
			continue
		}
		if !matchesFilters(candidate, term.Filters) {
			continue
		}
		score := formatScore(candidate, wantVideo, wantAudio)
		preference := extractorPreference(candidate)
		if selected == nil ||
			wantBest && (preference > selectedPreference || preference == selectedPreference && score > selectedScore) ||
			!wantBest && (preference < selectedPreference || preference == selectedPreference && score < selectedScore) {
			selected, selectedScore, selectedPreference = candidate, score, preference
		}
	}
	if selected == nil {
		return Selection{}, false
	}
	return objectSelection(selected), true
}

func selectTermWithOptions(formats []*value.Object, term Term, options Options) (Selection, bool) {
	if len(options.Sort) == 0 && len(options.PreferExtensions) == 0 && !options.PreferFreeFormats {
		return selectTerm(formats, term)
	}
	if term.Name != "best" && term.Name != "worst" && !strings.HasPrefix(term.Name, "best") && !strings.HasPrefix(term.Name, "worst") {
		return selectTerm(formats, term)
	}
	wantVideo := strings.HasSuffix(term.Name, "video")
	wantAudio := strings.HasSuffix(term.Name, "audio")
	wantWorst := strings.HasPrefix(term.Name, "worst")
	if len(options.Sort) == 0 {
		return selectTermWithPreferenceTiebreak(formats, term, options, wantVideo, wantAudio, wantWorst)
	}
	if wantWorst {
		for index := len(formats) - 1; index >= 0; index-- {
			if candidateMatchesKind(formats[index], wantVideo, wantAudio, term.Filters) {
				return objectSelection(formats[index]), true
			}
		}
		return Selection{}, false
	}
	for _, candidate := range formats {
		if candidateMatchesKind(candidate, wantVideo, wantAudio, term.Filters) {
			return objectSelection(candidate), true
		}
	}
	return Selection{}, false
}

// User extension/free preferences only break equal quality scores. This keeps
// yt-dlp's quality-first selection semantics while making the preference
// policy visible and deterministic when formats are otherwise equivalent.
func selectTermWithPreferenceTiebreak(formats []*value.Object, term Term, options Options, wantVideo, wantAudio, wantWorst bool) (Selection, bool) {
	var selected *value.Object
	selectedScore := float64(0)
	selectedPreference := float64(0)
	for _, candidate := range formats {
		if !candidateMatchesKind(candidate, wantVideo, wantAudio, term.Filters) {
			continue
		}
		score := formatScore(candidate, wantVideo, wantAudio)
		preference := extractorPreference(candidate)
		samePreference := preference == selectedPreference
		if selected == nil ||
			wantWorst && (preference < selectedPreference || samePreference && score < selectedScore) ||
			!wantWorst && (preference > selectedPreference || samePreference && score > selectedScore) ||
			samePreference && score == selectedScore && preferenceRank(candidate, options) > preferenceRank(selected, options) {
			selected, selectedScore, selectedPreference = candidate, score, preference
		}
	}
	if selected == nil {
		return Selection{}, false
	}
	return objectSelection(selected), true
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
	selection.TargetDuration, _ = numeric(object.Lookup("target_duration"))
	selection.LiveStartTimestamp, _ = object.Lookup("live_start_timestamp").Int()
	return selection
}
