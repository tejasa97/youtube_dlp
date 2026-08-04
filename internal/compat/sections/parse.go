// Package sections parses and normalizes yt-dlp-compatible --download-sections
// specifications into a bounded, ordered list of section ranges. It is the
// generic planner shared by the CLI and the product download path; it carries
// no I/O, no ffmpeg, and no extractor knowledge.
package sections

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ErrInvalidSpecification marks an unsupported or malformed range expression.
// It wraps ErrLimit for budget violations so callers can distinguish a
// user-input error from a resource-limit error.
var (
	ErrInvalidSpecification = errors.New("invalid section specification")
	ErrLimit                = errors.New("section limit")
)

// Limits bound the planner's resource use. They mirror the chapter-removal
// planner's approach: a fixed maximum number of specifications, a byte cap,
// and a maximum number of ranges after expansion.
const (
	MaxSpecifications     = 64
	MaxSpecificationBytes = 4096
	MaxTotalBytes         = 1 << 20
	MaxRanges             = 256
	MaxTimestampBytes     = 64
	MaxRangeCount         = 16
)

// Section is one normalized, bounded download range. Start is always set;
// End is nil for an open-ended ("...-inf") range. Open composes with an
// extractor-provided base offset at execution time, not here.
type Section struct {
	Start float64
	End   *float64
}

// Program is the parsed, normalized set of section ranges and the source URL
// marker. It is immutable after Parse returns.
type Program struct {
	Sections []Section
	// FromURL reports whether any *from-url specification was present. When
	// present, the execution layer consumes the extractor's start_time and
	// end_time fields as the section bounds.
	FromURL bool
}

// rangePattern matches the two supported literal forms: *START-END and
// *START-inf. Group 1 is start, group 2 is end (or "inf").
var rangePattern = regexp.MustCompile(`^\s*(\S+?)\-\s*(\S*)\s*$`)

// Parse compiles repeatable --download-sections values. Only the three
// supported forms are accepted:
//
//	*START-END     absolute bounded range
//	*START-inf     absolute open-ended range
//	*from-url      consume the extractor's start_time/end_time bounds
//
// Any other value (including a bare timestamp, a non-star value, or an
// unsupported range like *START+END) is rejected explicitly; the planner
// never silently ignores an unsupported expression.
func Parse(specifications []string) (Program, error) {
	if len(specifications) > MaxSpecifications {
		return Program{}, fmt.Errorf("%w: too many specifications", ErrLimit)
	}
	program := Program{}
	total := 0
	for index, specification := range specifications {
		total += len(specification)
		if len(specification) > MaxSpecificationBytes || total > MaxTotalBytes {
			return Program{}, fmt.Errorf("%w: specification %d", ErrLimit, index)
		}
		if specification == "*from-url" {
			program.FromURL = true
			continue
		}
		if !strings.HasPrefix(specification, "*") {
			return Program{}, fmt.Errorf("%w: unsupported value %q (expected *START-END, *START-inf, or *from-url)", ErrInvalidSpecification, specification)
		}
		startRaw, endRaw, ok := splitRange(specification)
		if !ok {
			return Program{}, fmt.Errorf("%w: range %q", ErrInvalidSpecification, specification)
		}
		if len(program.Sections) >= MaxRangeCount {
			return Program{}, fmt.Errorf("%w: too many ranges", ErrLimit)
		}
		start, err := parseTimestamp(startRaw, false)
		if err != nil {
			return Program{}, fmt.Errorf("%w: start of %q", err, specification)
		}
		var end *float64
		if strings.EqualFold(endRaw, "inf") || strings.EqualFold(endRaw, "infinite") {
			end = nil
		} else {
			parsed, parseErr := parseTimestamp(endRaw, false)
			if parseErr != nil {
				return Program{}, fmt.Errorf("%w: end of %q", parseErr, specification)
			}
			end = &parsed
		}
		if end != nil && *end <= start {
			return Program{}, fmt.Errorf("%w: non-positive range %q", ErrInvalidSpecification, specification)
		}
		program.Sections = append(program.Sections, Section{Start: start, End: end})
	}
	return program, nil
}

// splitRange splits a * spec into its start and end raw strings. It rejects
// payloads that do not contain a "-" separator or that are empty either side.
func splitRange(specification string) (start, end string, ok bool) {
	body := strings.TrimPrefix(specification, "*")
	match := rangePattern.FindStringSubmatch(body)
	if match == nil {
		return "", "", false
	}
	start, end = match[1], match[2]
	if start == "" {
		return "", "", false
	}
	return start, end, true
}

// parseTimestamp parses a bounded timestamp into seconds. It accepts the
// colon (MM:SS / HH:MM:SS / D:HH:MM:SS) and unit (1h30m / 90s / 1:30) forms,
// and rejects NaN, infinity, negatives, and overlong payloads.
func parseTimestamp(input string, allowInfinity bool) (float64, error) {
	text := strings.TrimSpace(input)
	if text == "" || len(text) > MaxTimestampBytes {
		return 0, fmt.Errorf("%w: timestamp", ErrInvalidSpecification)
	}
	if allowInfinity && (text == "inf" || text == "infinite") {
		return math.Inf(1), nil
	}
	if value, ok := parseColonDuration(text); ok {
		return value, nil
	}
	if value, ok := parseUnitDuration(text); ok {
		return value, nil
	}
	return 0, fmt.Errorf("%w: timestamp %q", ErrInvalidSpecification, input)
}

// parseColonDuration parses MM:SS, HH:MM:SS, and D:HH:MM:SS into seconds.
func parseColonDuration(text string) (float64, bool) {
	parts := strings.Split(text, ":")
	if len(parts) > 4 {
		return 0, false
	}
	values := make([]float64, len(parts))
	for index, part := range parts {
		if part == "" {
			return 0, false
		}
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, false
		}
		values[index] = value
	}
	multipliers := []float64{1}
	switch len(values) {
	case 1:
		multipliers = []float64{1}
	case 2:
		multipliers = []float64{60, 1}
	case 3:
		multipliers = []float64{3600, 60, 1}
	case 4:
		multipliers = []float64{86400, 3600, 60, 1}
	}
	total := 0.0
	for index, value := range values {
		total += value * multipliers[index]
	}
	return total, true
}

// parseUnitDuration parses "1h30m", "90s", "1:30", and similar unit forms.
// It is intentionally narrow: hours, minutes, seconds, and days with optional
// spaces and a trailing Z.
var unitPattern = regexp.MustCompile(`(?i)^\s*((?:[0-9]+(?:\.[0-9]+)?)\s*h(?:ours?)?)?\s*((?:[0-9]+(?:\.[0-9]+)?)\s*m(?:in(?:ute)?s?)?)?\s*((?:[0-9]+(?:\.[0-9]+)?)\s*s(?:ec(?:ond)?s?)?)?\s*(?:Z)?\s*$`)

var numberPattern = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)`)

func parseUnitDuration(text string) (float64, bool) {
	match := unitPattern.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	multipliers := []float64{3600, 60, 1}
	total := 0.0
	for index, raw := range match[1:] {
		if raw == "" {
			continue
		}
		if len(raw) > MaxTimestampBytes {
			return 0, false
		}
		num := numberPattern.FindString(raw)
		if num == "" {
			return 0, false
		}
		value, err := strconv.ParseFloat(num, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, false
		}
		total += value * multipliers[index]
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}
