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

// Selector holds a parsed format selector AST. Legacy Alternatives may still be
// set by in-repo constructors; evaluation always normalizes to the AST.
type Selector struct {
	root         *astNode
	Alternatives []Choice
}

// Choice is the legacy slash-alternative merge group representation.
type Choice struct {
	Terms []Term
}

// Term is the legacy merge-term representation.
type Term struct {
	Name    string
	Atom    Atom
	Filters []Filter
}

// Filter is a bounded [field op value] selector predicate.
type Filter struct {
	Field    string
	Operator string
	Value    string
}

// SyntaxError identifies the exact byte range rejected by the parser.
type SyntaxError struct {
	Start   int
	End     int
	Message string
}

func (err *SyntaxError) Error() string {
	return fmt.Sprintf("%v at bytes %d:%d: %s", ErrInvalidSelector, err.Start, err.End, err.Message)
}

func (err *SyntaxError) Unwrap() error { return ErrInvalidSelector }

func (selector Selector) rootNode() (*astNode, error) {
	if selector.root != nil {
		return selector.root, nil
	}
	if len(selector.Alternatives) == 0 {
		return nil, selectorSyntax(0, 0, "selector is empty")
	}
	return legacyAlternativesToAST(selector.Alternatives)
}

// ParseSelector parses a bounded yt-dlp format selector expression.
func ParseSelector(input string) (Selector, error) {
	root, err := parseSelectorAST(input)
	if err != nil {
		return Selector{}, err
	}
	return Selector{root: root}, nil
}

func Select(info value.Info, selector Selector) ([]Selection, error) {
	return SelectWithOptions(info, selector, Options{})
}

// SelectWithOptions evaluates a selector into flat tracks for one output plan.
// It returns ErrMultiOutput when the selector requests multiple independent
// comma/all outputs that cannot be represented as a single merge.
func SelectWithOptions(info value.Info, selector Selector, options Options) ([]Selection, error) {
	plans, err := PlanSelectWithOptions(info, selector, options)
	if err != nil {
		return nil, err
	}
	if len(plans) != 1 {
		return nil, ErrMultiOutput
	}
	return plans[0].Tracks, nil
}

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
	term, err := parseLegacyTermName(name, segment.start)
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

func parseLegacyTermName(name string, absStart int) (Term, error) {
	if name == "all" || name == "mergeall" {
		return Term{Name: name}, nil
	}
	if atom, err := parseAtomToken(name, absStart); err != nil {
		return Term{}, err
	} else if atom.OK {
		return Term{Name: atom.Canonical(), Atom: atom}, nil
	}
	if kind, ok := classifyExtensionToken(name); ok && kind != atomDirectID {
		return Term{Name: name}, nil
	}
	if !formatIDPattern.MatchString(name) {
		return Term{}, selectorSyntax(absStart, absStart+len(name), fmt.Sprintf("unknown term %q", name))
	}
	return Term{Name: name}, nil
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

type selectorSegment struct {
	text       string
	start, end int
}

func splitTopLevel(input selectorSegment, separator byte) ([]selectorSegment, error) {
	depth := 0
	parenDepth := 0
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
		case '(':
			parenDepth++
		case ')':
			parenDepth--
			if parenDepth < 0 {
				return nil, selectorSyntax(input.start+index, input.start+index+1, "unexpected )")
			}
		default:
			if input.text[index] == separator && depth == 0 && parenDepth == 0 {
				result = append(result, selectorSegment{text: input.text[start:index], start: input.start + start, end: input.start + index})
				start = index + 1
			}
		}
	}
	if depth != 0 {
		return nil, selectorSyntax(input.start+lastOpen, input.end, "unclosed filter")
	}
	if parenDepth != 0 {
		return nil, selectorSyntax(input.start, input.end, "unclosed group")
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

func candidateMatchesKind(candidate *value.Object, wantVideo, wantAudio bool, filters []Filter) bool {
	hasVideo, hasAudio := candidateMediaKinds(candidate)
	return (!wantVideo || hasVideo && !hasAudio) && (!wantAudio || hasAudio && !hasVideo) && matchesFilters(candidate, filters)
}

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

// LegacyParseSelector parses slash/plus-only selectors for transitional callers.
func LegacyParseSelector(input string) (Selector, error) {
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

// IsLegacyOnly reports whether the selector uses only slash/plus syntax without
// comma, grouping, or advanced atoms beyond the legacy corpus.
func (selector Selector) IsLegacyOnly() bool { return selector.root == nil }

func preferenceRank(object *value.Object, options Options) int {
	rank := extensionRank(object, options.PreferExtensions) * 2
	if options.PreferFreeFormats {
		rank += freeRank(object)
	}
	return rank
}

// Ensure compile-time interface sanity for error categories.
var _ = errors.New
