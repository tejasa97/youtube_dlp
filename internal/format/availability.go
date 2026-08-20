package format

import "github.com/tejasa97/ytdlp-go/internal/value"

// FormatAvailability reports whether a candidate format object is currently
// selectable by the planner. It mirrors yt-dlp's lazy _check_formats gate
// while remaining externally injected: the format planner never performs IO,
// probing, or any external work itself; the caller supplies an implementation
// if and when such work is needed.
//
// Implementations must be safe to invoke from a single goroutine; the planner
// never calls into FormatAvailability concurrently. They receive the canonical
// *value.Object owned by the Prepared instance and must not mutate it.
type FormatAvailability interface {
	IsAvailable(format *value.Object) (bool, error)
}

// FormatAvailabilityFunc adapts a plain function to the FormatAvailability
// interface. The zero value is not meaningful; the canonical adapter pattern
// is to declare a small function literal at the call site:
//
//	format.PlanWithOptions(selector, format.EvaluationOptions{
//	    Availability: format.FormatAvailabilityFunc(func(o *value.Object) (bool, error) {
//	        // ... external check ...
//	    }),
//	})
type FormatAvailabilityFunc func(format *value.Object) (bool, error)

// IsAvailable implements FormatAvailability.
func (fn FormatAvailabilityFunc) IsAvailable(format *value.Object) (bool, error) {
	return fn(format)
}

// EvaluationOptions bundles evaluator-only knobs that do not participate in
// canonical format preparation. They are passed explicitly to PlanWithOptions
// and PlanSelectWithEvaluationOptions so that:
//
//   - the canonical Preparation/Options path stays free of evaluator concerns,
//   - tests can exercise availability independently of sort/preference options,
//   - later CLI wiring does not have to retro-fit availability into Options.
//
// A zero-value EvaluationOptions means "every candidate is available", which
// matches Python's default `_check_formats` behaviour when no format-check or
// `allow_unplayable_formats` setting is supplied.
type EvaluationOptions struct {
	Availability FormatAvailability
}
