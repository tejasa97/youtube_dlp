// Package chapterremove parses and evaluates yt-dlp-compatible chapter
// removal specifications without depending on Python.
package chapterremove

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	MaxSpecifications     = 64
	MaxSpecificationBytes = 4096
	MaxTotalBytes         = 64 << 10
	MaxRanges             = 256
)

var (
	ErrInvalidSpecification = errors.New("invalid chapter removal specification")
	ErrLimit                = errors.New("chapter removal limit exceeded")
)

// Range is a parsed manual removal range. A nil End means the end of the
// media. Start defaults to zero for an open start.
type Range struct {
	Start float64
	End   *float64
}

// Program is an immutable, concurrency-safe chapter removal program.
type Program struct {
	patterns []*regexp.Regexp
	ranges   []Range
}

// Parse compiles repeatable --remove-chapters values. Values beginning with
// "*" are comma-separated time ranges; every other value is a title regular
// expression using search semantics.
func Parse(specifications []string) (Program, error) {
	if len(specifications) > MaxSpecifications {
		return Program{}, fmt.Errorf("%w: too many specifications", ErrLimit)
	}
	total := 0
	program := Program{}
	for index, specification := range specifications {
		total += len(specification)
		if len(specification) > MaxSpecificationBytes || total > MaxTotalBytes {
			return Program{}, fmt.Errorf("%w: specification %d", ErrLimit, index)
		}
		if !strings.HasPrefix(specification, "*") {
			expression, err := regexp.Compile(specification)
			if err != nil {
				return Program{}, fmt.Errorf("%w: regex %d", ErrInvalidSpecification, index)
			}
			program.patterns = append(program.patterns, expression)
			continue
		}
		parsed, err := parseRanges(specification)
		if err != nil {
			return Program{}, fmt.Errorf("%w: range %d", err, index)
		}
		if len(program.ranges)+len(parsed) > MaxRanges {
			return Program{}, fmt.Errorf("%w: too many ranges", ErrLimit)
		}
		program.ranges = append(program.ranges, parsed...)
	}
	return program, nil
}

// Empty reports whether the program requests no title or range removal.
func (program Program) Empty() bool {
	return len(program.patterns) == 0 && len(program.ranges) == 0
}

// HasPatterns reports whether title matching was requested.
func (program Program) HasPatterns() bool {
	return len(program.patterns) != 0
}

// HasRanges reports whether manual time ranges were requested.
func (program Program) HasRanges() bool {
	return len(program.ranges) != 0
}

// MatchTitle reports whether any configured expression occurs in title.
func (program Program) MatchTitle(ctx context.Context, title string) (bool, error) {
	for _, expression := range program.patterns {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if expression.MatchString(title) {
			return true, nil
		}
	}
	return false, nil
}

// ResolveRanges maps open range ends to duration and drops intervals wholly
// outside the media. Returned ranges are clamped but intentionally not merged;
// the shared cut planner performs stable overlap/adjacency merging.
func (program Program) ResolveRanges(duration float64) ([]Range, error) {
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return nil, fmt.Errorf("%w: duration", ErrInvalidSpecification)
	}
	out := make([]Range, 0, len(program.ranges))
	for _, item := range program.ranges {
		start := item.Start
		end := duration
		if item.End != nil {
			end = *item.End
		}
		if start >= duration || end <= 0 || end <= start {
			continue
		}
		if start < 0 {
			start = 0
		}
		if end > duration {
			end = duration
		}
		endCopy := end
		out = append(out, Range{Start: start, End: &endCopy})
	}
	return out, nil
}

var rangePattern = regexp.MustCompile(`^(?:(-?)([^-]+))?\s*-\s*(?:(-?)([^-]+))?$`)

func parseRanges(specification string) ([]Range, error) {
	body := strings.TrimPrefix(specification, "*")
	parts := strings.Split(body, ",")
	out := make([]Range, 0, len(parts))
	for _, raw := range parts {
		text := strings.TrimSpace(raw)
		match := rangePattern.FindStringSubmatch(text)
		if text == "" || text == "-" || match == nil {
			return nil, fmt.Errorf("%w: expected *start-end", ErrInvalidSpecification)
		}
		if match[1] != "" || match[3] != "" {
			return nil, fmt.Errorf("%w: negative timestamp", ErrInvalidSpecification)
		}
		start := 0.0
		if strings.TrimSpace(match[2]) != "" {
			parsed, ok := parseTimestamp(match[2], false)
			if !ok {
				return nil, fmt.Errorf("%w: start timestamp", ErrInvalidSpecification)
			}
			start = parsed
		}
		var end *float64
		if strings.TrimSpace(match[4]) != "" {
			parsed, ok := parseTimestamp(match[4], true)
			if !ok {
				return nil, fmt.Errorf("%w: end timestamp", ErrInvalidSpecification)
			}
			if math.IsInf(parsed, 1) {
				end = nil
			} else {
				end = &parsed
			}
		}
		if end != nil && *end <= start {
			return nil, fmt.Errorf("%w: non-positive range", ErrInvalidSpecification)
		}
		out = append(out, Range{Start: start, End: end})
	}
	return out, nil
}

func parseTimestamp(input string, allowInfinity bool) (float64, bool) {
	text := strings.TrimSpace(input)
	if allowInfinity && (text == "inf" || text == "infinite") {
		return math.Inf(1), true
	}
	if text == "" {
		return 0, false
	}
	if value, ok := parseColonDuration(text); ok {
		return value, true
	}
	if value, ok := parseUnitDuration(text); ok {
		return value, true
	}
	return 0, false
}

func parseColonDuration(text string) (float64, bool) {
	trimmed := strings.TrimSuffix(strings.TrimSpace(text), "Z")
	parts := strings.Split(trimmed, ":")
	if len(parts) > 4 {
		return 0, false
	}
	fraction := 0.0
	if len(parts) > 1 {
		last := parts[len(parts)-1]
		if len(last) > 2 && !strings.Contains(last, ".") {
			digits, err := strconv.ParseUint(last, 10, 64)
			if err != nil {
				return 0, false
			}
			fraction = float64(digits) / math.Pow10(len(last))
			parts = parts[:len(parts)-1]
		}
	}
	values := make([]float64, len(parts))
	for index, part := range parts {
		if part == "" {
			return 0, false
		}
		if index < len(parts)-1 {
			if strings.Contains(part, ".") {
				return 0, false
			}
			value, err := strconv.ParseUint(part, 10, 64)
			if err != nil {
				return 0, false
			}
			values[index] = float64(value)
			continue
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
	total := fraction
	for index, value := range values {
		total += value * multipliers[index]
	}
	return total, true
}

var unitDurationPattern = regexp.MustCompile(`(?i)^\s*(?:P?(?:[0-9]+\s*y(?:ears?)?,?\s*)?(?:[0-9]+\s*m(?:onths?)?,?\s*)?(?:[0-9]+\s*w(?:eeks?)?,?\s*)?(?:([0-9]+)\s*d(?:ays?)?,?\s*)?T)?(?:([0-9]+(?:\.[0-9]+)?)\s*h(?:(?:ou)?rs?)?,?\s*)?(?:([0-9]+(?:\.[0-9]+)?)\s*m(?:in(?:ute)?s?)?\.?,?\s*)?(?:([0-9]+(?:\.[0-9]+)?)\s*s(?:ec(?:ond)?s?)?\s*)?Z?\s*$`)

func parseUnitDuration(text string) (float64, bool) {
	match := unitDurationPattern.FindStringSubmatch(text)
	if match == nil || (match[1] == "" && match[2] == "" && match[3] == "" && match[4] == "") {
		return 0, false
	}
	multipliers := []float64{86400, 3600, 60, 1}
	total := 0.0
	for index, raw := range match[1:] {
		if raw == "" {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return 0, false
		}
		total += value * multipliers[index]
	}
	return total, true
}
