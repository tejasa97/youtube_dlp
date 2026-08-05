package simplefilter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 8, 1, 15, 30, 0, 0, time.UTC) }
}

func info(fields ...value.Field) value.Info {
	return value.NewInfo(value.NewObject(fields...))
}

// check runs Checker.Check and fails the test on an unexpected evaluation
// error, keeping rejection assertions readable.
func check(t *testing.T, checker *Checker, info value.Info) (string, bool) {
	t.Helper()
	reason, rejected, err := checker.Check(context.Background(), info)
	if err != nil {
		t.Fatalf("Check error: %v", err)
	}
	return reason, rejected
}

func TestDateRangeStrictParse(t *testing.T) {
	clock := fixedClock()
	for _, test := range []struct {
		input string
		want  string
	}{
		{"20240101", "2024-01-01"},
		{"now", "2026-08-01"},
		{"today", "2026-08-01"},
		{"yesterday", "2026-07-31"},
		{"today-2weeks", "2026-07-18"},
		{"now-1day", "2026-07-31"},
		{"today-3months", "2026-05-01"},
		{"today-1year", "2025-08-01"},
		{"today-10days", "2026-07-22"},
	} {
		parsed, err := parseStrictDate(test.input, clock)
		if err != nil {
			t.Fatalf("%q: %v", test.input, err)
		}
		if got := parsed.Format("2006-01-02"); got != test.want {
			t.Fatalf("%q = %s, want %s", test.input, got, test.want)
		}
	}
	for _, input := range []string{"", "2024-1-1", "2024-13-01", "yesterday-", "now-1", "now-1hours", "tomorrow", "now+1day"} {
		if _, err := parseStrictDate(input, clock); err == nil {
			t.Fatalf("%q: expected invalid date error", input)
		}
	}
}

func TestDateRangeContainsAndMessage(t *testing.T) {
	clock := fixedClock()
	checker, err := New(Options{DateAfter: "20240101", DateBefore: "20241231", Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		uploadDate string
		rejected   bool
		want       string
	}{
		{"20240101", false, ""},
		{"20241231", false, ""},
		{"20240102", false, ""},
		{"20231231", true, "2023-12-31 upload date is not in range 2024-01-01 to 2024-12-31"},
		{"20250101", true, "2025-01-01 upload date is not in range 2024-01-01 to 2024-12-31"},
	} {
		reason, rejected := check(t, checker, info(
			value.Field{Key: "id", Value: value.String("v1")},
			value.Field{Key: "title", Value: value.String("Video")},
			value.Field{Key: "upload_date", Value: value.String(test.uploadDate)},
		))
		if rejected != test.rejected || (rejected && reason != test.want) {
			t.Fatalf("date=%s rejected=%v reason=%q want rejected=%v reason=%q", test.uploadDate, rejected, reason, test.rejected, test.want)
		}
	}
}

func TestDateSingleDay(t *testing.T) {
	checker, err := New(Options{Date: "20240815", Clock: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	if reason, rejected := check(t, checker, info(
		value.Field{Key: "upload_date", Value: value.String("20240815")},
	)); rejected {
		t.Fatalf("same-day date rejected: %q", reason)
	}
	if reason, rejected := check(t, checker, info(
		value.Field{Key: "upload_date", Value: value.String("20240816")},
	)); !rejected || !strings.Contains(reason, "2024-08-16 upload date is not in range 2024-08-15 to 2024-08-15") {
		t.Fatalf("next-day date not rejected: %q %v", reason, rejected)
	}
}

func TestDateRangeInvalidBounds(t *testing.T) {
	if _, err := New(Options{DateAfter: "20250101", DateBefore: "20240101"}); err == nil {
		t.Fatal("expected inverted range error")
	}
	if _, err := New(Options{Date: "not-a-date"}); err == nil {
		t.Fatal("expected invalid date error")
	}
}

func TestMatchTitleAndRejectTitle(t *testing.T) {
	checker, err := New(Options{MatchTitle: `^Native`, RejectTitle: `(live|stream)`})
	if err != nil {
		t.Fatal(err)
	}
	if reason, rejected := check(t, checker, info(
		value.Field{Key: "title", Value: value.String("native fixture")},
	)); rejected {
		t.Fatalf("case-insensitive match rejected: %q", reason)
	}
	if reason, rejected := check(t, checker, info(
		value.Field{Key: "title", Value: value.String("Other Video")},
	)); !rejected || reason != `"Other Video" title did not match pattern "^Native"` {
		t.Fatalf("want title mismatch rejection, got %q %v", reason, rejected)
	}
	if reason, rejected := check(t, checker, info(
		value.Field{Key: "title", Value: value.String("Native Live Stream")},
	)); !rejected || reason != `"Native Live Stream" title matched reject pattern "(live|stream)"` {
		t.Fatalf("want reject-title rejection, got %q %v", reason, rejected)
	}
	// Absent title never rejects.
	if _, rejected := check(t, checker, info(
		value.Field{Key: "id", Value: value.String("v1")},
	)); rejected {
		t.Fatal("absent title rejected")
	}
}

func TestInvalidTitlePattern(t *testing.T) {
	if _, err := New(Options{MatchTitle: "("}); err == nil {
		t.Fatal("expected invalid pattern error")
	}
}

func TestViewCountFilters(t *testing.T) {
	minViews, maxViews := int64(100), int64(1000)
	checker, err := New(Options{MinViews: &minViews, MaxViews: &maxViews})
	if err != nil {
		t.Fatal(err)
	}
	base := []value.Field{
		{Key: "id", Value: value.String("v1")},
		{Key: "title", Value: value.String("Video")},
	}
	if reason, rejected := check(t, checker, info(append(base,
		value.Field{Key: "view_count", Value: value.Int(500)})...)); rejected {
		t.Fatalf("in-range view count rejected: %q", reason)
	}
	if reason, rejected := check(t, checker, info(append(base,
		value.Field{Key: "view_count", Value: value.Int(50)})...)); !rejected ||
		reason != "Skipping Video, because it has not reached minimum view count (50/100)" {
		t.Fatalf("want min-view rejection, got %q %v", reason, rejected)
	}
	if reason, rejected := check(t, checker, info(append(base,
		value.Field{Key: "view_count", Value: value.Int(2000)})...)); !rejected ||
		reason != "Skipping Video, because it has exceeded the maximum view count (2000/1000)" {
		t.Fatalf("want max-view rejection, got %q %v", reason, rejected)
	}
	// Absent view count never rejects.
	if _, rejected := check(t, checker, info(base...)); rejected {
		t.Fatal("absent view count rejected")
	}
}

func TestAgeLimitFilter(t *testing.T) {
	ageLimit := int64(18)
	checker, err := New(Options{AgeLimit: &ageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if reason, rejected := check(t, checker, info(
		value.Field{Key: "age_limit", Value: value.Int(16)},
	)); rejected {
		t.Fatalf("age 16 rejected by limit 18: %q", reason)
	}
	if reason, rejected := check(t, checker, info(
		value.Field{Key: "age_limit", Value: value.Int(19)},
	)); !rejected || reason != `Skipping "entry" because it is age restricted` {
		t.Fatalf("want age rejection, got %q %v", reason, rejected)
	}
	// Absent content age never rejects.
	if _, rejected := check(t, checker, info(
		value.Field{Key: "id", Value: value.String("v1")},
	)); rejected {
		t.Fatal("absent age_limit rejected")
	}
}

func TestCheckOrderMatchesReference(t *testing.T) {
	// Title rejections precede date/views/age checks.
	checker, err := New(Options{MatchTitle: `^Keep$`, Date: "20240101"})
	if err != nil {
		t.Fatal(err)
	}
	reason, rejected := check(t, checker, info(
		value.Field{Key: "title", Value: value.String("Drop Me")},
		value.Field{Key: "upload_date", Value: value.String("20240101")},
	))
	if !rejected || !strings.HasPrefix(reason, `"Drop Me" title did not match`) {
		t.Fatalf("title check must precede date check, got %q %v", reason, rejected)
	}
}

func TestNoFiltersNeverRejects(t *testing.T) {
	checker, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if reason, rejected := check(t, checker, info(
		value.Field{Key: "id", Value: value.String("v1")},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "view_count", Value: value.Int(1)},
		value.Field{Key: "age_limit", Value: value.Int(99)},
		value.Field{Key: "upload_date", Value: value.String("20240101")},
	)); rejected {
		t.Fatalf("no filters rejected: %q", reason)
	}
}

func TestCheckOverlongUploadDateFailsClosed(t *testing.T) {
	checker, err := New(Options{DateAfter: "20240101"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = checker.Check(context.Background(), info(
		value.Field{Key: "upload_date", Value: value.String(strings.Repeat("1", maxEvaluatedDateString+1))},
	))
	if !errors.Is(err, ErrEvaluation) {
		t.Fatalf("overlong upload date must fail closed with ErrEvaluation, got %v", err)
	}
}

func TestCheckRegexTimeoutPropagates(t *testing.T) {
	// A catastrophic backtracking pattern against a long non-matching title
	// exceeds the bounded match timeout and must surface as a typed error
	// instead of a rejection or a pass.
	checker, err := New(Options{MatchTitle: `(a+)+$`})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = checker.Check(context.Background(), info(
		value.Field{Key: "title", Value: value.String(strings.Repeat("a", 200) + "b")},
	))
	if !errors.Is(err, ErrEvaluation) {
		t.Fatalf("regex timeout must propagate as ErrEvaluation, got %v", err)
	}
	// The reject-title path must fail the same way instead of accepting.
	checker, err = New(Options{RejectTitle: `(a+)+$`})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = checker.Check(context.Background(), info(
		value.Field{Key: "title", Value: value.String(strings.Repeat("a", 200) + "b")},
	))
	if !errors.Is(err, ErrEvaluation) {
		t.Fatalf("reject-title timeout must propagate as ErrEvaluation, got %v", err)
	}
}

func TestCheckCancellationPropagates(t *testing.T) {
	checker, err := New(Options{MatchTitle: ".*"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := checker.Check(ctx, info(
		value.Field{Key: "title", Value: value.String("anything")},
	)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context must propagate, got %v", err)
	}
}
