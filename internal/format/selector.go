package format

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrInvalidSelector = errors.New("invalid format selector")
	ErrNoMatch         = errors.New("no format matches selector")
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
	root := trimSelectorSegment(selectorSegment{text: input, start: 0, end: len(input)})
	if root.text == "" {
		return Selector{}, selectorSyntax(root.start, root.end, "selector is empty")
	}
	alternatives, err := splitTopLevel(root, '/')
	if err != nil {
		return Selector{}, err
	}
	selector := Selector{}
	for _, alternative := range alternatives {
		parts, err := splitTopLevel(alternative, '+')
		if err != nil {
			return Selector{}, err
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
	formats, ok := info.Formats()
	if !ok {
		return nil, ErrNoFormats
	}
	objects := make([]*value.Object, 0, len(formats))
	for _, item := range formats {
		if object, ok := item.Object(); ok {
			objects = append(objects, object)
		}
	}
	for _, alternative := range selector.Alternatives {
		var selected []Selection
		matched := true
		for _, term := range alternative.Terms {
			selection, ok := selectTerm(objects, term)
			if !ok {
				matched = false
				break
			}
			selected = append(selected, selection)
		}
		if matched {
			return selected, nil
		}
	}
	return nil, ErrNoMatch
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
	case "best", "worst", "bestvideo", "worstvideo", "bestaudio", "worstaudio":
	default:
		return Term{}, selectorSyntax(segment.start, segment.start+len(name), fmt.Sprintf("unknown term %q", name))
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
		remaining = remaining[close+1:]
		remainingStart += close + 1
	}
	return term, nil
}

var fieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
	wantBest := strings.HasPrefix(term.Name, "best")
	wantVideo := strings.HasSuffix(term.Name, "video")
	wantAudio := strings.HasSuffix(term.Name, "audio")
	var selected *value.Object
	var selectedScore float64
	for _, candidate := range formats {
		vcodec, _ := candidate.Lookup("vcodec").StringValue()
		acodec, _ := candidate.Lookup("acodec").StringValue()
		hasVideo := vcodec != "" && vcodec != "none"
		hasAudio := acodec != "" && acodec != "none"
		if wantVideo && !hasVideo || wantAudio && !hasAudio {
			continue
		}
		if !matchesFilters(candidate, term.Filters) {
			continue
		}
		score := formatScore(candidate, wantVideo, wantAudio)
		if selected == nil || wantBest && score > selectedScore || !wantBest && score < selectedScore {
			selected, selectedScore = candidate, score
		}
	}
	if selected == nil {
		return Selection{}, false
	}
	return objectSelection(selected), true
}

func matchesFilters(object *value.Object, filters []Filter) bool {
	for _, filter := range filters {
		input := object.Lookup(filter.Field)
		stringValue, stringOK := input.StringValue()
		numericValue, numericOK := numeric(input)
		filterNumber, numberErr := strconv.ParseFloat(filter.Value, 64)
		var matched bool
		switch filter.Operator {
		case "=":
			matched = stringOK && stringValue == filter.Value
		case "!=":
			matched = !stringOK || stringValue != filter.Value
		case "^=":
			matched = stringOK && strings.HasPrefix(stringValue, filter.Value)
		case "$=":
			matched = stringOK && strings.HasSuffix(stringValue, filter.Value)
		case "*=":
			matched = stringOK && strings.Contains(stringValue, filter.Value)
		case "~=":
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
	return selection
}
