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

func ParseSelector(input string) (Selector, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Selector{}, fmt.Errorf("%w: selector is empty", ErrInvalidSelector)
	}
	alternatives, err := splitTopLevel(input, '/')
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
			term, err := parseTerm(strings.TrimSpace(part))
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

func parseTerm(input string) (Term, error) {
	if input == "" {
		return Term{}, fmt.Errorf("%w: empty term", ErrInvalidSelector)
	}
	open := strings.IndexByte(input, '[')
	name := input
	remaining := ""
	if open >= 0 {
		name, remaining = input[:open], input[open:]
	}
	switch name {
	case "best", "worst", "bestvideo", "worstvideo", "bestaudio", "worstaudio":
	default:
		return Term{}, fmt.Errorf("%w: unknown term %q", ErrInvalidSelector, name)
	}
	term := Term{Name: name}
	for remaining != "" {
		if remaining[0] != '[' {
			return Term{}, fmt.Errorf("%w: unexpected text %q", ErrInvalidSelector, remaining)
		}
		close := strings.IndexByte(remaining, ']')
		if close < 0 {
			return Term{}, fmt.Errorf("%w: unclosed filter", ErrInvalidSelector)
		}
		filter, err := parseFilter(remaining[1:close])
		if err != nil {
			return Term{}, err
		}
		term.Filters = append(term.Filters, filter)
		remaining = remaining[close+1:]
	}
	return term, nil
}

var fieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func parseFilter(input string) (Filter, error) {
	for _, operator := range []string{"!=", ">=", "<=", "^=", "$=", "*=", "~=", "=", ">", "<"} {
		if index := strings.Index(input, operator); index > 0 {
			field := strings.TrimSpace(input[:index])
			filterValue := strings.TrimSpace(input[index+len(operator):])
			if !fieldPattern.MatchString(field) || filterValue == "" {
				return Filter{}, fmt.Errorf("%w: malformed filter %q", ErrInvalidSelector, input)
			}
			if len(filterValue) >= 2 && ((filterValue[0] == '"' && filterValue[len(filterValue)-1] == '"') || (filterValue[0] == '\'' && filterValue[len(filterValue)-1] == '\'')) {
				filterValue = filterValue[1 : len(filterValue)-1]
			}
			return Filter{Field: field, Operator: operator, Value: filterValue}, nil
		}
	}
	return Filter{}, fmt.Errorf("%w: filter %q has no operator", ErrInvalidSelector, input)
}

func splitTopLevel(input string, separator byte) ([]string, error) {
	depth := 0
	start := 0
	var result []string
	for index := 0; index < len(input); index++ {
		switch input[index] {
		case '[':
			depth++
		case ']':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("%w: unexpected ] at byte %d", ErrInvalidSelector, index)
			}
		default:
			if input[index] == separator && depth == 0 {
				result = append(result, input[start:index])
				start = index + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("%w: unclosed filter", ErrInvalidSelector)
	}
	result = append(result, input[start:])
	return result, nil
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
