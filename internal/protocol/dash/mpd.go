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
	ErrInvalidMPD          = errors.New("invalid DASH MPD")
	ErrUnsupportedTimeline = errors.New("unsupported DASH segment timeline")
)

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
}

type mpdXML struct {
	Type                  string      `xml:"type,attr"`
	MinimumUpdatePeriod   string      `xml:"minimumUpdatePeriod,attr"`
	MediaPresentationTime string      `xml:"mediaPresentationDuration,attr"`
	BaseURL               string      `xml:"BaseURL"`
	Periods               []periodXML `xml:"Period"`
}

type periodXML struct {
	BaseURL        string             `xml:"BaseURL"`
	AdaptationSets []adaptationSetXML `xml:"AdaptationSet"`
}

type adaptationSetXML struct {
	ContentType     string              `xml:"contentType,attr"`
	MimeType        string              `xml:"mimeType,attr"`
	Codecs          string              `xml:"codecs,attr"`
	BaseURL         string              `xml:"BaseURL"`
	SegmentTemplate *segmentTemplateXML `xml:"SegmentTemplate"`
	SegmentList     *segmentListXML     `xml:"SegmentList"`
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
}

type segmentTemplateXML struct {
	Media          string             `xml:"media,attr"`
	Initialization string             `xml:"initialization,attr"`
	Timescale      int64              `xml:"timescale,attr"`
	Duration       int64              `xml:"duration,attr"`
	StartNumber    int64              `xml:"startNumber,attr"`
	Timeline       segmentTimelineXML `xml:"SegmentTimeline"`
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
	Initialization *initializationXML `xml:"Initialization"`
	Segments       []segmentURLXML    `xml:"SegmentURL"`
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
	mpdBase, err := resolveBase(base, document.BaseURL)
	if err != nil {
		return MPD{}, err
	}
	for _, period := range document.Periods {
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
				template := representation.SegmentTemplate
				if template == nil {
					template = adaptation.SegmentTemplate
				}
				list := representation.SegmentList
				if list == nil {
					list = adaptation.SegmentList
				}
				switch {
				case template != nil:
					normalized.Segments, err = templateSegments(representationBase, normalized, template, presentationDuration)
				case list != nil:
					normalized.Segments, err = listSegments(representationBase, list)
				case representation.BaseURL != "":
					normalized.Segments = []Segment{{URL: representationBase.String()}}
				default:
					err = errors.New("representation has no segment source")
				}
				if err != nil {
					return MPD{}, fmt.Errorf("%w: representation %q: %v", ErrInvalidMPD, representation.ID, err)
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
	}
	number := startNumber
	currentTime := int64(0)
	if len(template.Timeline.Entries) > 0 {
		for _, entry := range template.Timeline.Entries {
			if entry.Duration <= 0 || entry.Repeat < 0 {
				return nil, ErrUnsupportedTimeline
			}
			if entry.Time != nil {
				currentTime = *entry.Time
			}
			for repeat := int64(0); repeat <= entry.Repeat; repeat++ {
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
