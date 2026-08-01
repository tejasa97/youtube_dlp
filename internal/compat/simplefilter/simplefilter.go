// Package simplefilter implements the bounded simple metadata filters of
// yt-dlp's --match-title/--reject-title, --date/--dateafter/--datebefore,
// --min-views/--max-views, and --age-limit options. It mirrors the pinned
// reference behavior of YoutubeDL._match_entry's simple checks: rejections
// carry the exact reference messages, absent fields never reject, and the
// checks run in the reference order (title, upload date, view count, age).
package simplefilter

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/ytdlp-go/ytdlp/internal/compat/pyregex"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxPatternBytes        = 512
	regexMatchTimeout      = 25 * time.Millisecond
	maxEvaluatedDateString = 256
)

// ErrInvalidDate reports a date that the pinned date_from_str grammar
// rejects. The message mirrors Python's ValueError text.
var ErrInvalidDate = errors.New("invalid date format")

// ErrEvaluation reports a bounded simple-filter evaluation failure (regex
// timeout or resource limit). It is a typed failure: the entry must fail
// closed rather than being skipped or accepted based on the partial result.
var ErrEvaluation = errors.New("simple filter evaluation failed")

// Options carries the parsed CLI-level simple filter settings. Zero values
// mean the corresponding filter is disabled, matching the reference defaults
// of None.
type Options struct {
	MatchTitle  string
	RejectTitle string
	// Date selects a single upload day and takes precedence over DateAfter
	// and DateBefore (the reference CLI resolves the conflict before the
	// checker is built).
	Date       string
	DateAfter  string
	DateBefore string
	MinViews   *int64
	MaxViews   *int64
	AgeLimit   *int64
	// Clock resolves relative dates (now/today/yesterday). It defaults to
	// time.Now and is injectable for deterministic tests.
	Clock func() time.Time
}

// Checker evaluates the configured simple filters against media metadata.
// It is immutable after construction and safe for concurrent use.
type Checker struct {
	matchTitle     *regexp2.Regexp
	rejectTitle    *regexp2.Regexp
	matchTitleRaw  string
	rejectTitleRaw string
	dateRange      *DateRange
	minViews       *int64
	maxViews       *int64
	ageLimit       *int64
}

// New compiles the configured filters. Invalid title patterns and invalid
// dates are rejected here so callers fail before any extraction starts.
func New(options Options) (*Checker, error) {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	checker := &Checker{
		minViews: options.MinViews, maxViews: options.MaxViews,
		ageLimit: options.AgeLimit,
	}
	if options.MatchTitle != "" {
		compiled, err := compileTitlePattern(options.MatchTitle)
		if err != nil {
			return nil, err
		}
		checker.matchTitle, checker.matchTitleRaw = compiled, options.MatchTitle
	}
	if options.RejectTitle != "" {
		compiled, err := compileTitlePattern(options.RejectTitle)
		if err != nil {
			return nil, err
		}
		checker.rejectTitle, checker.rejectTitleRaw = compiled, options.RejectTitle
	}
	if options.Date != "" {
		day, err := parseStrictDate(options.Date, options.Clock)
		if err != nil {
			return nil, err
		}
		checker.dateRange = newDateRange(day, day)
	} else if options.DateAfter != "" || options.DateBefore != "" {
		start, end, err := parseDateRange(options.DateAfter, options.DateBefore, options.Clock)
		if err != nil {
			return nil, err
		}
		checker.dateRange = newDateRange(start, end)
	}
	return checker, nil
}

// Check evaluates the simple filters against info in the reference order:
// title match/reject, upload date range, view count, age limit. It returns
// the exact reference rejection message and true when the entry is rejected.
// Absent fields never reject. Evaluation failures (regex timeouts, oversized
// inputs) return a typed error so callers fail closed instead of skipping or
// accepting content on a partial result.
func (checker *Checker) Check(ctx context.Context, info value.Info) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if title, ok := info.Title(); ok {
		if checker.matchTitle != nil {
			matched, err := checker.matchTitle.MatchString(title)
			if err != nil {
				return "", false, fmt.Errorf("%w: title match: %v", ErrEvaluation, err)
			}
			if !matched {
				return fmt.Sprintf(`"%s" title did not match pattern "%s"`, title, checker.matchTitleRaw), true, nil
			}
		}
		if checker.rejectTitle != nil {
			matched, err := checker.rejectTitle.MatchString(title)
			if err != nil {
				return "", false, fmt.Errorf("%w: title reject: %v", ErrEvaluation, err)
			}
			if matched {
				return fmt.Sprintf(`"%s" title matched reject pattern "%s"`, title, checker.rejectTitleRaw), true, nil
			}
		}
	}
	if checker.dateRange != nil {
		if date, ok := info.Lookup("upload_date").StringValue(); ok {
			if len(date) > maxEvaluatedDateString {
				return "", false, fmt.Errorf("%w: upload date exceeds %d bytes", ErrEvaluation, maxEvaluatedDateString)
			}
			iso := date
			if parsed, err := parseUploadDate(date); err == nil {
				iso = parsed.Format("2006-01-02")
			}
			if inRange, err := checker.dateRange.Contains(date); err != nil || !inRange {
				return fmt.Sprintf("%s upload date is not in range %s", iso, checker.dateRange), true, nil
			}
		}
	}
	if viewCount, ok := info.Lookup("view_count").Int(); ok {
		title := videoTitle(info)
		if checker.minViews != nil && viewCount < *checker.minViews {
			return fmt.Sprintf("Skipping %s, because it has not reached minimum view count (%d/%d)", title, viewCount, *checker.minViews), true, nil
		}
		if checker.maxViews != nil && viewCount > *checker.maxViews {
			return fmt.Sprintf("Skipping %s, because it has exceeded the maximum view count (%d/%d)", title, viewCount, *checker.maxViews), true, nil
		}
	}
	if contentAge, ok := info.Lookup("age_limit").Int(); ok {
		if checker.ageLimit != nil && *checker.ageLimit < contentAge {
			return fmt.Sprintf("Skipping \"%s\" because it is age restricted", videoTitle(info)), true, nil
		}
	}
	return "", false, nil
}

func videoTitle(info value.Info) string {
	if title, ok := info.Title(); ok {
		return title
	}
	if id, ok := info.ID(); ok {
		return id
	}
	return "entry"
}

// DateRange mirrors yt-dlp's utils.DateRange with inclusive bounds. The
// message form is "<start> to <end>" with ISO dates.
type DateRange struct {
	start time.Time
	end   time.Time
}

// newDateRange builds an inclusive range from two resolved dates.
func newDateRange(start, end time.Time) *DateRange {
	return &DateRange{start: start, end: end}
}

// Contains reports whether the upload date string lies within the inclusive
// range. Unparseable dates are treated as outside the range, matching the
// reference behavior of raising on invalid upload dates.
func (r *DateRange) Contains(date string) (bool, error) {
	parsed, err := parseUploadDate(date)
	if err != nil {
		return false, err
	}
	return !parsed.Before(r.start) && !parsed.After(r.end), nil
}

func (r *DateRange) String() string {
	return r.start.Format("2006-01-02") + " to " + r.end.Format("2006-01-02")
}

var (
	strictDatePattern   = regexp.MustCompile(`^\d{8}$|^(now|today|yesterday)(-\d+(day|week|month|year)s?)?$`)
	relativeDatePattern = regexp.MustCompile(`^(now|today|yesterday)(?:-(\d+)(day|week|month|year)s?)?$`)
	uploadDatePattern   = regexp.MustCompile(`^\d{8}$|^\d{4}-\d{2}-\d{2}$`)
)

// parseStrictDate mirrors date_from_str(date, strict=True): only YYYYMMDD and
// the bounded relative forms are accepted.
func parseStrictDate(input string, clock func() time.Time) (time.Time, error) {
	if !strictDatePattern.MatchString(input) {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidDate, input)
	}
	return resolveDate(input, clock)
}

// parseUploadDate mirrors date_from_str(date) without strict mode for the
// upload_date values extractors produce: YYYYMMDD and YYYY-MM-DD.
func parseUploadDate(input string) (time.Time, error) {
	if !uploadDatePattern.MatchString(input) {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidDate, input)
	}
	return time.ParseInLocation("20060102", strings.ReplaceAll(input, "-", ""), time.UTC)
}

// parseDateRange resolves the --dateafter/--datebefore bounds. Missing bounds
// default to the minimum and maximum supported dates like DateRange().
func parseDateRange(after, before string, clock func() time.Time) (time.Time, time.Time, error) {
	start := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	var err error
	if after != "" {
		if start, err = parseStrictDate(after, clock); err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if before != "" {
		if end, err = parseStrictDate(before, clock); err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if start.After(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("date range: the start date must be before the end date")
	}
	return start, end, nil
}

func resolveDate(input string, clock func() time.Time) (time.Time, error) {
	if len(input) == 8 && input[0] >= '0' && input[0] <= '9' {
		return time.ParseInLocation("20060102", input, time.UTC)
	}
	match := relativeDatePattern.FindStringSubmatch(input)
	if match == nil {
		return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidDate, input)
	}
	base := dateOf(clock())
	switch match[1] {
	case "yesterday":
		base = base.AddDate(0, 0, -1)
	}
	if match[2] != "" {
		amount, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil || amount < 0 {
			return time.Time{}, fmt.Errorf("%w: %q", ErrInvalidDate, input)
		}
		switch match[3] {
		case "day":
			base = base.AddDate(0, 0, -int(amount))
		case "week":
			base = base.AddDate(0, 0, -7*int(amount))
		case "month":
			base = addMonths(base, -int(amount))
		case "year":
			base = addYears(base, -int(amount))
		}
	}
	return base, nil
}

func dateOf(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// addMonths mirrors utils.datetime_add_months: the day is clamped to the
// destination month's length.
func addMonths(date time.Time, months int) time.Time {
	year, month, day := date.Date()
	total := int(month) - 1 + months
	year += total / 12
	month = time.Month(total%12 + 1)
	last := daysIn(year, month)
	if day > last {
		day = last
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// addYears shifts the year while clamping a leap day to February 28.
func addYears(date time.Time, years int) time.Time {
	year, month, day := date.Date()
	if month == time.February && day == 29 && !isLeap(year+years) {
		day = 28
	}
	return time.Date(year+years, month, day, 0, 0, 0, 0, time.UTC)
}

func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

// compileTitlePattern mirrors re.search(pattern, title, re.IGNORECASE):
// the pattern is translated with the shared Python regex machinery and
// compiled case-insensitively with a bounded match timeout.
func compileTitlePattern(pattern string) (*regexp2.Regexp, error) {
	if len(pattern) == 0 || len(pattern) > maxPatternBytes {
		return nil, fmt.Errorf("invalid title pattern %q", pattern)
	}
	translated, err := pyregex.Translate(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid title pattern %q", pattern)
	}
	compiled, err := regexp2.Compile(translated, regexp2.IgnoreCase)
	if err != nil {
		return nil, fmt.Errorf("invalid title pattern %q", pattern)
	}
	compiled.MatchTimeout = regexMatchTimeout
	return compiled, nil
}
