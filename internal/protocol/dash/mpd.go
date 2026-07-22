// Package dash parses and downloads the Phase 1 DASH pilot subset.
package dash

import (
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidMPD            = errors.New("invalid DASH MPD")
	ErrUnsupportedTimeline   = errors.New("unsupported DASH segment timeline")
	ErrUnsupportedAddressing = errors.New("unsupported DASH segment addressing")
)

const maxSegmentsPerRepresentation = 100_000

type MPD struct {
	Dynamic             bool
	MinimumUpdatePeriod time.Duration
	Representations     []Representation
}

type Representation struct {
	ID          string
	ContentType string
	MimeType    string
	Codecs      string
	Bandwidth   int64
	Width       int
	Height      int
	Segments    []Segment
}

type Segment struct {
	URL         string
	RangeStart  int64
	RangeLength int64
	Initialize  bool
	// IndexRange is set when this segment requires SIDX expansion. The
	// downloader fetches this byte range from the media resource, parses the
	// SIDX box, and replaces this segment with expanded media byte ranges.
	// Format: "start-end" (inclusive).
	IndexRange string
	// InitRange is the byte range of the initialization segment within the
	// same media resource. Only set alongside IndexRange when the
	// initialization is in-file rather than a separate resource.
	InitRange string
}

type mpdXML struct {
	Type                  string      `xml:"type,attr"`
	MinimumUpdatePeriod   string      `xml:"minimumUpdatePeriod,attr"`
	MediaPresentationTime string      `xml:"mediaPresentationDuration,attr"`
	AvailabilityStartTime string      `xml:"availabilityStartTime,attr"`
	PublishTime           string      `xml:"publishTime,attr"`
	BaseURL               string      `xml:"BaseURL"`
	Periods               []periodXML `xml:"Period"`
}

type periodXML struct {
	Start           string              `xml:"start,attr"`
	Duration        string              `xml:"duration,attr"`
	BaseURL         string              `xml:"BaseURL"`
	SegmentTemplate *segmentTemplateXML `xml:"SegmentTemplate"`
	SegmentList     *segmentListXML     `xml:"SegmentList"`
	SegmentBase     *segmentBaseXML     `xml:"SegmentBase"`
	AdaptationSets  []adaptationSetXML  `xml:"AdaptationSet"`
}

type adaptationSetXML struct {
	ContentType     string              `xml:"contentType,attr"`
	MimeType        string              `xml:"mimeType,attr"`
	Codecs          string              `xml:"codecs,attr"`
	BaseURL         string              `xml:"BaseURL"`
	SegmentTemplate *segmentTemplateXML `xml:"SegmentTemplate"`
	SegmentList     *segmentListXML     `xml:"SegmentList"`
	SegmentBase     *segmentBaseXML     `xml:"SegmentBase"`
	Representations []representationXML `xml:"Representation"`
}

type representationXML struct {
	ID              string              `xml:"id,attr"`
	Bandwidth       int64               `xml:"bandwidth,attr"`
	Width           int                 `xml:"width,attr"`
	Height          int                 `xml:"height,attr"`
	MimeType        string              `xml:"mimeType,attr"`
	Codecs          string              `xml:"codecs,attr"`
	BaseURL         string              `xml:"BaseURL"`
	SegmentTemplate *segmentTemplateXML `xml:"SegmentTemplate"`
	SegmentList     *segmentListXML     `xml:"SegmentList"`
	SegmentBase     *segmentBaseXML     `xml:"SegmentBase"`
}

type segmentTemplateXML struct {
	Media                 string             `xml:"media,attr"`
	Initialization        string             `xml:"initialization,attr"`
	Timescale             int64              `xml:"timescale,attr"`
	Duration              int64              `xml:"duration,attr"`
	StartNumber           int64              `xml:"startNumber,attr"`
	Timeline              segmentTimelineXML `xml:"SegmentTimeline"`
	InitializationElement *initializationXML `xml:"Initialization"`
}

type segmentTimelineXML struct {
	Entries []timelineEntryXML `xml:"S"`
}

type timelineEntryXML struct {
	Time     *int64 `xml:"t,attr"`
	Duration int64  `xml:"d,attr"`
	Repeat   int64  `xml:"r,attr"`
}

type segmentListXML struct {
	Timescale      int64              `xml:"timescale,attr"`
	Duration       int64              `xml:"duration,attr"`
	StartNumber    int64              `xml:"startNumber,attr"`
	Timeline       segmentTimelineXML `xml:"SegmentTimeline"`
	Initialization *initializationXML `xml:"Initialization"`
	Segments       []segmentURLXML    `xml:"SegmentURL"`
}

type segmentBaseXML struct {
	IndexRange     string             `xml:"indexRange,attr"`
	Initialization *initializationXML `xml:"Initialization"`
}

type initializationXML struct {
	SourceURL string `xml:"sourceURL,attr"`
	Range     string `xml:"range,attr"`
}

type segmentURLXML struct {
	Media      string `xml:"media,attr"`
	MediaRange string `xml:"mediaRange,attr"`
}

func Parse(rawURL string, input []byte) (MPD, error) {
	base, err := url.Parse(rawURL)
	if err != nil {
		return MPD{}, fmt.Errorf("%w: base URL: %v", ErrInvalidMPD, err)
	}
	var document mpdXML
	if err := xml.Unmarshal(input, &document); err != nil {
		return MPD{}, fmt.Errorf("%w: XML: %v", ErrInvalidMPD, err)
	}
	minimumUpdate, err := parseISODuration(document.MinimumUpdatePeriod)
	if err != nil {
		return MPD{}, fmt.Errorf("%w: minimumUpdatePeriod: %v", ErrInvalidMPD, err)
	}
	presentationDuration, err := parseISODuration(document.MediaPresentationTime)
	if err != nil {
		return MPD{}, fmt.Errorf("%w: mediaPresentationDuration: %v", ErrInvalidMPD, err)
	}
	result := MPD{Dynamic: document.Type == "dynamic", MinimumUpdatePeriod: minimumUpdate}
	availabilityStart, availabilityErr := parseOptionalTime(document.AvailabilityStartTime)
	if availabilityErr != nil {
		return MPD{}, fmt.Errorf("%w: availabilityStartTime: %v", ErrInvalidMPD, availabilityErr)
	}
	publishTime, publishErr := parseOptionalTime(document.PublishTime)
	if publishErr != nil {
		return MPD{}, fmt.Errorf("%w: publishTime: %v", ErrInvalidMPD, publishErr)
	}
	mpdBase, err := resolveBase(base, document.BaseURL)
	if err != nil {
		return MPD{}, err
	}
	for _, period := range document.Periods {
		periodStart, err := parseISODuration(period.Start)
		if err != nil {
			return MPD{}, fmt.Errorf("%w: period start: %v", ErrInvalidMPD, err)
		}
		periodDuration, err := parseISODuration(period.Duration)
		if err != nil {
			return MPD{}, fmt.Errorf("%w: period duration: %v", ErrInvalidMPD, err)
		}
		if periodDuration <= 0 {
			periodDuration = presentationDuration
		}
		if periodDuration <= 0 && !availabilityStart.IsZero() && !publishTime.IsZero() {
			periodDuration = publishTime.Sub(availabilityStart) - periodStart
		}
		periodBase, err := resolveBase(mpdBase, period.BaseURL)
		if err != nil {
			return MPD{}, err
		}
		for _, adaptation := range period.AdaptationSets {
			adaptationBase, err := resolveBase(periodBase, adaptation.BaseURL)
			if err != nil {
				return MPD{}, err
			}
			for _, representation := range adaptation.Representations {
				representationBase, err := resolveBase(adaptationBase, representation.BaseURL)
				if err != nil {
					return MPD{}, err
				}
				normalized := Representation{
					ID: representation.ID, ContentType: adaptation.ContentType,
					MimeType:  firstNonEmpty(representation.MimeType, adaptation.MimeType),
					Codecs:    firstNonEmpty(representation.Codecs, adaptation.Codecs),
					Bandwidth: representation.Bandwidth, Width: representation.Width, Height: representation.Height,
				}
				template := mergeSegmentTemplates(period.SegmentTemplate, adaptation.SegmentTemplate, representation.SegmentTemplate)
				list := mergeSegmentLists(period.SegmentList, adaptation.SegmentList, representation.SegmentList)
				segmentBase := mergeSegmentBases(period.SegmentBase, adaptation.SegmentBase, representation.SegmentBase)
				switch {
				case template != nil:
					normalized.Segments, err = templateSegments(representationBase, normalized, template, periodDuration)
				case list != nil:
					normalized.Segments, err = listSegments(representationBase, list)
				case segmentBase != nil:
					normalized.Segments, err = baseSegments(representationBase, segmentBase)
				case representation.BaseURL != "":
					normalized.Segments = []Segment{{URL: representationBase.String()}}
				default:
					err = errors.New("representation has no segment source")
				}
				if err != nil {
					return MPD{}, fmt.Errorf("%w: representation %q: %w", ErrInvalidMPD, representation.ID, err)
				}
				result.Representations = append(result.Representations, normalized)
			}
		}
	}
	if len(result.Representations) == 0 {
		return MPD{}, fmt.Errorf("%w: no representations", ErrInvalidMPD)
	}
	return result, nil
}

func templateSegments(base *url.URL, representation Representation, template *segmentTemplateXML, presentationDuration time.Duration) ([]Segment, error) {
	timescale := template.Timescale
	if timescale <= 0 {
		timescale = 1
	}
	startNumber := template.StartNumber
	if startNumber <= 0 {
		startNumber = 1
	}
	var result []Segment
	if template.Initialization != "" {
		resolved, err := templateURL(base, template.Initialization, representation, startNumber, 0)
		if err != nil {
			return nil, err
		}
		result = append(result, Segment{URL: resolved, Initialize: true})
	} else if template.InitializationElement != nil {
		segment, err := rangedSegment(base, template.InitializationElement.SourceURL, template.InitializationElement.Range)
		if err != nil {
			return nil, err
		}
		segment.Initialize = true
		result = append(result, segment)
	}
	number := startNumber
	currentTime := int64(0)
	if len(template.Timeline.Entries) > 0 {
		for entryIndex, entry := range template.Timeline.Entries {
			if entry.Duration <= 0 || entry.Repeat < -1 {
				return nil, ErrUnsupportedTimeline
			}
			if entry.Time != nil {
				currentTime = *entry.Time
			}
			repeatCount := entry.Repeat
			if repeatCount == -1 {
				var endTime int64
				if entryIndex+1 < len(template.Timeline.Entries) && template.Timeline.Entries[entryIndex+1].Time != nil {
					endTime = *template.Timeline.Entries[entryIndex+1].Time
				} else if presentationDuration > 0 {
					endTime = int64(math.Ceil(presentationDuration.Seconds() * float64(timescale)))
				} else {
					return nil, fmt.Errorf("%w: open-ended negative repeat", ErrUnsupportedTimeline)
				}
				if endTime <= currentTime {
					return nil, fmt.Errorf("%w: invalid repeat boundary", ErrUnsupportedTimeline)
				}
				repeatCount = (endTime - currentTime - 1) / entry.Duration
			}
			if int64(len(result))+repeatCount+1 > maxSegmentsPerRepresentation {
				return nil, fmt.Errorf("%w: segment count exceeds %d", ErrUnsupportedTimeline, maxSegmentsPerRepresentation)
			}
			for repeat := int64(0); repeat <= repeatCount; repeat++ {
				resolved, err := templateURL(base, template.Media, representation, number, currentTime)
				if err != nil {
					return nil, err
				}
				result = append(result, Segment{URL: resolved})
				number++
				currentTime += entry.Duration
			}
		}
		return result, nil
	}
	if template.Duration <= 0 || presentationDuration <= 0 {
		return nil, errors.New("duration template needs MPD presentation duration")
	}
	count := int64(math.Ceil(presentationDuration.Seconds() * float64(timescale) / float64(template.Duration)))
	if count > maxSegmentsPerRepresentation {
		return nil, fmt.Errorf("%w: segment count exceeds %d", ErrUnsupportedTimeline, maxSegmentsPerRepresentation)
	}
	for index := int64(0); index < count; index++ {
		resolved, err := templateURL(base, template.Media, representation, number, index*template.Duration)
		if err != nil {
			return nil, err
		}
		result = append(result, Segment{URL: resolved})
		number++
	}
	return result, nil
}

func listSegments(base *url.URL, list *segmentListXML) ([]Segment, error) {
	if len(list.Segments) > maxSegmentsPerRepresentation {
		return nil, fmt.Errorf("%w: segment count exceeds %d", ErrUnsupportedAddressing, maxSegmentsPerRepresentation)
	}
	var result []Segment
	if list.Initialization != nil {
		segment, err := rangedSegment(base, list.Initialization.SourceURL, list.Initialization.Range)
		if err != nil {
			return nil, err
		}
		segment.Initialize = true
		result = append(result, segment)
	}
	for _, item := range list.Segments {
		segment, err := rangedSegment(base, item.Media, item.MediaRange)
		if err != nil {
			return nil, err
		}
		result = append(result, segment)
	}
	return result, nil
}

func baseSegments(base *url.URL, segmentBase *segmentBaseXML) ([]Segment, error) {
	if segmentBase.IndexRange != "" {
		return indexRangeSegments(base, segmentBase)
	}
	// A SegmentBase without an index is a single-file representation. When the
	// initialization source is a different resource it must precede that file;
	// an in-file initialization range is already contained in the full resource.
	result := make([]Segment, 0, 2)
	if initialization := segmentBase.Initialization; initialization != nil && initialization.SourceURL != "" {
		resolved, err := resolveBase(base, initialization.SourceURL)
		if err != nil {
			return nil, err
		}
		if resolved.String() != base.String() {
			segment, err := rangedSegment(base, initialization.SourceURL, initialization.Range)
			if err != nil {
				return nil, err
			}
			segment.Initialize = true
			result = append(result, segment)
		}
	}
	return append(result, Segment{URL: base.String()}), nil
}

// indexRangeSegments builds the segment plan for a SegmentBase with indexRange.
// It validates the range and returns a marker segment that the downloader will
// expand via SIDX fetch and parse.
func indexRangeSegments(base *url.URL, segmentBase *segmentBaseXML) ([]Segment, error) {
	start, end, err := parseByteRange(segmentBase.IndexRange)
	if err != nil {
		return nil, fmt.Errorf("%w: indexRange: %v", ErrUnsupportedAddressing, err)
	}
	_ = start
	_ = end

	segment := Segment{
		URL:        base.String(),
		IndexRange: segmentBase.IndexRange,
	}

	// Handle initialization.
	if initialization := segmentBase.Initialization; initialization != nil {
		if initialization.SourceURL != "" {
			// Separate initialization resource.
			resolved, err := resolveBase(base, initialization.SourceURL)
			if err != nil {
				return nil, err
			}
			initSegment, err := rangedSegment(base, initialization.SourceURL, initialization.Range)
			if err != nil {
				return nil, err
			}
			initSegment.Initialize = true
			// If the init resource is the same as the media resource, record
			// the range on the marker segment so the downloader can prepend it.
			if resolved.String() == base.String() {
				segment.InitRange = initialization.Range
				return []Segment{segment}, nil
			}
			return []Segment{initSegment, segment}, nil
		}
		if initialization.Range != "" {
			// Same-resource initialization range.
			if _, _, err := parseByteRange(initialization.Range); err != nil {
				return nil, fmt.Errorf("%w: initialization range: %v", ErrUnsupportedAddressing, err)
			}
			segment.InitRange = initialization.Range
		}
	}
	return []Segment{segment}, nil
}

// parseByteRange parses a "start-end" inclusive byte range string and validates
// that it is well-formed: non-negative, non-reversed, and non-overflowing.
func parseByteRange(raw string) (int64, int64, error) {
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid byte range %q", raw)
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid byte range start %q", parts[0])
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < 0 {
		return 0, 0, fmt.Errorf("invalid byte range end %q", parts[1])
	}
	if end < start {
		return 0, 0, fmt.Errorf("reversed byte range %d-%d", start, end)
	}
	if end-start+1 <= 0 {
		return 0, 0, fmt.Errorf("overflowing byte range %d-%d", start, end)
	}
	return start, end, nil
}

func mergeSegmentTemplates(values ...*segmentTemplateXML) *segmentTemplateXML {
	var result *segmentTemplateXML
	for _, value := range values {
		if value == nil {
			continue
		}
		if result == nil {
			result = &segmentTemplateXML{}
		}
		if value.Media != "" {
			result.Media = value.Media
		}
		if value.Initialization != "" {
			result.Initialization = value.Initialization
		}
		if value.InitializationElement != nil {
			result.InitializationElement = value.InitializationElement
		}
		if value.Timescale != 0 {
			result.Timescale = value.Timescale
		}
		if value.Duration != 0 {
			result.Duration = value.Duration
		}
		if value.StartNumber != 0 {
			result.StartNumber = value.StartNumber
		}
		if len(value.Timeline.Entries) > 0 {
			result.Timeline = value.Timeline
		}
	}
	return result
}

func mergeSegmentLists(values ...*segmentListXML) *segmentListXML {
	var result *segmentListXML
	for _, value := range values {
		if value == nil {
			continue
		}
		if result == nil {
			result = &segmentListXML{}
		}
		if value.Timescale != 0 {
			result.Timescale = value.Timescale
		}
		if value.Duration != 0 {
			result.Duration = value.Duration
		}
		if value.StartNumber != 0 {
			result.StartNumber = value.StartNumber
		}
		if len(value.Timeline.Entries) > 0 {
			result.Timeline = value.Timeline
		}
		if value.Initialization != nil {
			result.Initialization = value.Initialization
		}
		if len(value.Segments) > 0 {
			result.Segments = value.Segments
		}
	}
	return result
}

// mergeSegmentBases merges SegmentBase fields across hierarchy levels
// (Period → AdaptationSet → Representation). More specific levels override
// less specific ones, field by field, matching the DASH inheritance model.
// Initialization is treated as an overriding element: a more-specific
// Initialization replaces the parent element wholesale (shallow inheritance),
// matching DASH-IF dash.js behavior (SegmentValuesMap.js, objectiron.js).
func mergeSegmentBases(values ...*segmentBaseXML) *segmentBaseXML {
	var result *segmentBaseXML
	for _, value := range values {
		if value == nil {
			continue
		}
		if result == nil {
			result = &segmentBaseXML{}
		}
		if value.IndexRange != "" {
			result.IndexRange = value.IndexRange
		}
		if value.Initialization != nil {
			result.Initialization = value.Initialization
		}
	}
	return result
}

func rangedSegment(base *url.URL, rawURL, rawRange string) (Segment, error) {
	resolved, err := resolveBase(base, rawURL)
	if err != nil {
		return Segment{}, err
	}
	segment := Segment{URL: resolved.String()}
	if rawRange == "" {
		return segment, nil
	}
	parts := strings.SplitN(rawRange, "-", 2)
	if len(parts) != 2 {
		return Segment{}, errors.New("invalid byte range")
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Segment{}, err
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return Segment{}, errors.New("invalid byte range")
	}
	segment.RangeStart = start
	segment.RangeLength = end - start + 1
	return segment, nil
}

var templatePattern = regexp.MustCompile(`\$(RepresentationID|Bandwidth|Number|Time)(%0([0-9]+)d)?\$`)

func templateURL(base *url.URL, pattern string, representation Representation, number, segmentTime int64) (string, error) {
	if pattern == "" {
		return "", errors.New("segment template media is empty")
	}
	replaced := strings.ReplaceAll(pattern, "$$", "\x00")
	replaced = templatePattern.ReplaceAllStringFunc(replaced, func(match string) string {
		parts := templatePattern.FindStringSubmatch(match)
		var value string
		switch parts[1] {
		case "RepresentationID":
			value = representation.ID
		case "Bandwidth":
			value = strconv.FormatInt(representation.Bandwidth, 10)
		case "Number":
			value = strconv.FormatInt(number, 10)
		case "Time":
			value = strconv.FormatInt(segmentTime, 10)
		}
		if parts[3] != "" && parts[1] != "RepresentationID" {
			width, _ := strconv.Atoi(parts[3])
			value = strings.Repeat("0", max(0, width-len(value))) + value
		}
		return value
	})
	replaced = strings.ReplaceAll(replaced, "\x00", "$")
	if strings.Contains(replaced, "$") {
		return "", fmt.Errorf("unsupported template token in %q", pattern)
	}
	resolved, err := resolveBase(base, replaced)
	if err != nil {
		return "", err
	}
	return resolved.String(), nil
}

func resolveBase(base *url.URL, raw string) (*url.URL, error) {
	if raw == "" {
		copy := *base
		return &copy, nil
	}
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	return base.ResolveReference(reference), nil
}

func parseISODuration(input string) (time.Duration, error) {
	if input == "" {
		return 0, nil
	}
	if !strings.HasPrefix(input, "PT") {
		return 0, fmt.Errorf("unsupported duration %q", input)
	}
	remaining := strings.TrimPrefix(input, "PT")
	var total time.Duration
	for len(remaining) > 0 {
		index := strings.IndexAny(remaining, "HMS")
		if index <= 0 {
			return 0, fmt.Errorf("invalid duration %q", input)
		}
		value, err := strconv.ParseFloat(remaining[:index], 64)
		if err != nil {
			return 0, err
		}
		switch remaining[index] {
		case 'H':
			total += time.Duration(value * float64(time.Hour))
		case 'M':
			total += time.Duration(value * float64(time.Minute))
		case 'S':
			total += time.Duration(value * float64(time.Second))
		}
		remaining = remaining[index+1:]
	}
	return total, nil
}

func parseOptionalTime(input string) (time.Time, error) {
	if input == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, input)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
