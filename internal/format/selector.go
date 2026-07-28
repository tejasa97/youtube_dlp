package format

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrInvalidSelector  = errors.New("invalid format selector")
	ErrNoMatch          = errors.New("no format matches selector")
	ErrFilterEvaluation = errors.New("format filter evaluation failed")
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
// Raw syntax ownership lives in unexported fields so PR 3 can compile the
// original bracket body (including ?, quoting, and escapes) while retaining
// the exported legacy Field/Operator/Value surface for in-repo constructors.
type Filter struct {
	Field    string
	Operator string
	Value    string

	raw       string
	span      span
	predicate *compiledFilter
}

// SyntaxError identifies the exact half-open byte range [Start, End) rejected by
// the parser in the original, untrimmed selector string.
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
	root, err := legacyAlternativesToAST(selector.Alternatives)
	if err != nil {
		return nil, err
	}
	if err := compileNodeFilters(root); err != nil {
		return nil, err
	}
	if err := enforceRegexPredicateLimit(root); err != nil {
		return nil, err
	}
	return root, nil
}

// ParseSelector parses a bounded yt-dlp format selector expression.
func ParseSelector(input string) (Selector, error) {
	root, err := parseSelectorAST(input)
	if err != nil {
		return Selector{}, err
	}
	if err := compileNodeFilters(root); err != nil {
		return Selector{}, err
	}
	if err := enforceRegexPredicateLimit(root); err != nil {
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
	if !validDirectIDToken(name) {
		return Term{}, selectorSyntax(absStart, absStart+len(name), fmt.Sprintf("unknown term %q", name))
	}
	return Term{Name: name}, nil
}

func parseFilter(input string, start int) (Filter, error) {
	// Syntax-only extraction for legacy exported fields. Semantic validation and
	// Python-regex compilation happen in compileFilter; do not use Go RE2 here.
	for _, operator := range []string{"!^=", "!$=", "!*=", "!~=", "!=", ">=", "<=", "^=", "$=", "*=", "~=", "=", ">", "<"} {
		if index := strings.Index(input, operator); index > 0 {
			field := strings.TrimSpace(input[:index])
			filterValue := strings.TrimSpace(input[index+len(operator):])
			if field == "" || filterValue == "" {
				return Filter{}, selectorSyntax(start, start+len(input), fmt.Sprintf("malformed filter %q", input))
			}
			legacyValue := filterValue
			if len(legacyValue) >= 2 && ((legacyValue[0] == '"' && legacyValue[len(legacyValue)-1] == '"') || (legacyValue[0] == '\'' && legacyValue[len(legacyValue)-1] == '\'')) {
				legacyValue = legacyValue[1 : len(legacyValue)-1]
			}
			if len(legacyValue) > maxSelectorBytes/2 {
				return Filter{}, selectorSyntax(start+index+len(operator), start+len(input), "filter value exceeds size limit")
			}
			return Filter{
				Field:    field,
				Operator: operator,
				Value:    legacyValue,
				raw:      input,
				span:     span{start: start, end: start + len(input)},
			}, nil
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
	matched, err := matchesFilters(candidate, filters, nil)
	return err == nil && (!wantVideo || hasVideo && !hasAudio) && (!wantAudio || hasAudio && !hasVideo) && matched
}

func candidateMediaKinds(candidate *value.Object) (hasVideo, hasAudio bool) {
	vcodec, _ := candidate.Lookup("vcodec").StringValue()
	acodec, _ := candidate.Lookup("acodec").StringValue()
	hasVideo = vcodec != "none" && (vcodec != "" || acodec == "none")
	hasAudio = acodec != "none" && (acodec != "" || vcodec == "none")
	return hasVideo, hasAudio
}

func matchesFilters(object *value.Object, filters []Filter, budget *regexEvalBudget) (bool, error) {
	for index := range filters {
		matched, err := filters[index].match(object, budget)
		if err != nil {
			return false, err
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
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

func preferenceRank(object *value.Object, options Options) int {
	rank := extensionRank(object, options.PreferExtensions) * 2
	if options.PreferFreeFormats {
		rank += freeRank(object)
	}
	return rank
}

// Ensure compile-time interface sanity for error categories.
var _ = errors.New
