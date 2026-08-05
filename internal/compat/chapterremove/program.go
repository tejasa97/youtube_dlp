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
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
	"github.com/tejasa97/youtube_dlp/internal/compat/pyregex"
)

const (
	MaxSpecifications     = 64
	MaxSpecificationBytes = 4096
	MaxTotalBytes         = 64 << 10
	MaxRanges             = 256
	// The chapter title language is a bounded Python regular-expression
	// search, matching the pinned ModifyChaptersPP use of regex.search().
	// Keep these limits local to chapter removal: this package shares syntax
	// translation with match filters and metadata transforms, not their
	// execution policy.
	MaxRegexSourceBytes      = 4096
	MaxRegexTranslatedBytes  = 16 << 10
	MaxRegexInputBytes       = 64 << 10
	MaxRegexAttempts         = 256
	MaxRegexInspectedBytes   = 4 << 20
	RegexMatchTimeout        = 25 * time.Millisecond
	RegexAggregateWallBudget = 250 * time.Millisecond
)

var (
	ErrInvalidSpecification = errors.New("invalid chapter removal specification")
	ErrLimit                = errors.New("chapter removal limit exceeded")
	errRegexTimeout         = errors.New("regular expression match timed out")
	errRegexBudget          = errors.New("regular expression budget exhausted")
)

// Range is a parsed manual removal range. A nil End means the end of the
// media. Start defaults to zero for an open start.
type Range struct {
	Start float64
	End   *float64
}

// Program is an immutable, concurrency-safe chapter removal program.
type Program struct {
	patterns []pattern
	ranges   []Range
}

// EvaluationBudget bounds one logical group of chapter-title searches. It is
// intentionally caller-owned so Program remains immutable and safe to reuse
// concurrently. A fresh budget is suitable for a single MatchTitle call;
// product code shares one budget across an entire media item's chapters.
type EvaluationBudget struct {
	attempts       int
	inspectedBytes int
	wall           time.Duration
	started        time.Time
}

type pattern struct {
	source, translated string
	expression         *regexp2.Regexp
}

// NewEvaluationBudget returns a bounded accounting object for a complete
// chapter-removal evaluation. It contains no source text or title data.
func NewEvaluationBudget() *EvaluationBudget { return &EvaluationBudget{started: time.Now()} }

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
			if len(specification) > MaxRegexSourceBytes || !utf8.ValidString(specification) {
				return Program{}, fmt.Errorf("%w: regex %d", ErrInvalidSpecification, index)
			}
			translated, err := pyregex.Translate(specification)
			if err != nil || len(translated) > MaxRegexTranslatedBytes {
				return Program{}, fmt.Errorf("%w: regex %d", ErrInvalidSpecification, index)
			}
			expression, err := regexp2.Compile(translated, regexp2.None)
			if err != nil {
				return Program{}, fmt.Errorf("%w: regex %d", ErrInvalidSpecification, index)
			}
			expression.MatchTimeout = RegexMatchTimeout
			program.patterns = append(program.patterns, pattern{
				source: specification, translated: translated, expression: expression,
			})
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
	return program.MatchTitleWithBudget(ctx, title, NewEvaluationBudget())
}

// MatchTitleWithBudget reports whether any configured Python expression
// searches title. The source, translated expression, input, number of
// attempts, inspected bytes, individual match time, and aggregate wall time
// are all bounded. Errors deliberately contain no pattern or title text.
func (program Program) MatchTitleWithBudget(ctx context.Context, title string, budget *EvaluationBudget) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(title) > MaxRegexInputBytes || !utf8.ValidString(title) {
		return false, fmt.Errorf("%w: regular expression input", ErrLimit)
	}
	if budget == nil {
		budget = NewEvaluationBudget()
	}
	if budget.started.IsZero() {
		budget.started = time.Now()
	}
	for _, item := range program.patterns {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		budget.attempts++
		budget.inspectedBytes += len(title)
		if budget.attempts > MaxRegexAttempts || budget.inspectedBytes > MaxRegexInspectedBytes || time.Since(budget.started) > RegexAggregateWallBudget {
			return false, fmt.Errorf("%w: %w", ErrLimit, errRegexBudget)
		}
		started := time.Now()
		matched, err := item.expression.MatchString(title)
		budget.wall += time.Since(started)
		if budget.wall > RegexAggregateWallBudget {
			return false, fmt.Errorf("%w: %w", ErrLimit, errRegexBudget)
		}
		if err != nil {
			return false, sanitizeRegexError(err)
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func sanitizeRegexError(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "timeout") || strings.Contains(message, "timed out") {
		return fmt.Errorf("%w: %w", ErrLimit, errRegexTimeout)
	}
	return fmt.Errorf("%w: %w", ErrLimit, errRegexBudget)
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
