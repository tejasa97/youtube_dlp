package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/sponsorblock"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// enrichWithSponsorBlock performs the optional SponsorBlock metadata
// fetch and writes the normalized chapters onto the given info. The
// function is called only when the public Request explicitly opts in.
//
// The integration is intentionally narrow: it is a metadata fetch and
// normalization stage. Media cutting, chapter rewriting, FFmpeg
// removal, subtitle synchronization, and CLI surface remain out of
// scope and are not exercised here.
//
// The function never panics. All errors are categorized and surface
// through the existing pkg/ytdlp Error mechanism. A valid empty
// response (HTTP 200, HTTP 404, or no matching videoID) is treated
// as success and the result.InfoJSON ends up with an empty
// sponsorblock_chapters list.
func (operation *operation) enrichWithSponsorBlock(ctx context.Context, extractorName string, info *value.Info) error {
	if !operation.request.SponsorBlock.Enabled {
		return nil
	}
	if info == nil {
		return &Error{Category: ErrorInternal, Op: "sponsorblock", Err: errors.New("missing metadata")}
	}
	// SponsorBlock metadata is defined for YouTube only. A
	// non-YouTube extractor paired with the option is a
	// categorized unsupported error rather than a silent
	// success.
	if !extractorSupportsSponsorBlock(extractorName) {
		return &Error{
			Category: ErrorUnsupported,
			Op:       "sponsorblock extractor",
			Err:      fmt.Errorf("sponsorblock: %w", sponsorblock.ErrUnsupported),
		}
	}
	id, hasID := info.ID()
	if !hasID || id == "" {
		return &Error{Category: ErrorInternal, Op: "sponsorblock", Err: errors.New("missing video id")}
	}
	duration := 0.0
	if raw := info.Lookup("duration"); !raw.IsMissing() {
		if d, ok := sponsorblockDuration(raw); ok {
			duration = d
		}
	}
	options := sponsorblock.Options{
		Enabled:    true,
		Categories: operation.request.SponsorBlock.Categories,
		APIBase:    operation.request.SponsorBlock.APIBase,
	}
	result, err := sponsorblock.Fetch(ctx, operation.transport, options, "YouTube", id, duration)
	if err != nil {
		return mapSponsorBlockError(err)
	}
	values := make([]value.Value, 0, len(result.Chapters))
	for _, chapter := range result.Chapters {
		values = append(values, chapterValue(chapter))
	}
	info.Set("sponsorblock_chapters", value.List(values...))
	return nil
}

// extractorSupportsSponsorBlock returns true when extractorName is a
// YouTube-family extractor. Only the watch-page extractor carries
// real durations that the pinned normalizer can use.
func extractorSupportsSponsorBlock(extractorName string) bool {
	switch extractorName {
	case "youtube":
		return true
	}
	return false
}

// sponsorblockDuration extracts a numeric video duration in seconds
// from a value.Info field. The conversion is defensive: SponsorBlock
// timestamps are float64 seconds; the value package stores durations
// as int64 seconds, float64 seconds, or strings depending on the
// extractor. Non-finite values are rejected.
func sponsorblockDuration(raw value.Value) (float64, bool) {
	switch raw.Kind() {
	case value.KindInt:
		seconds, ok := raw.Int()
		if !ok || seconds < 0 {
			return 0, false
		}
		return float64(seconds), true
	case value.KindFloat:
		seconds, ok := raw.Float()
		if !ok {
			return 0, false
		}
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
			return 0, false
		}
		return seconds, true
	case value.KindString:
		text, ok := raw.StringValue()
		if !ok {
			return 0, false
		}
		// Accept "SS" and "SS.SSS" forms only. Anything
		// else is treated as missing so the normalizer
		// keeps all entries (the duration filter is
		// disabled without a known duration).
		seconds, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return 0, false
		}
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
			return 0, false
		}
		return seconds, true
	}
	return 0, false
}

// chapterValue renders one normalized chapter as a value.Object. Only
// the pinned fields are exposed; untrusted extra API fields are
// dropped. StartTime and EndTime are encoded as float64 to match the
// pinned reference's JSON output.
func chapterValue(chapter sponsorblock.Chapter) value.Value {
	object := value.NewObject()
	object.Set("start_time", value.Float(chapter.StartTime))
	object.Set("end_time", value.Float(chapter.EndTime))
	object.Set("category", value.String(chapter.Category))
	object.Set("title", value.String(chapter.Title))
	object.Set("type", value.String(chapter.Type))
	return value.ObjectValue(object)
}

// mapSponsorBlockError translates a categorized SponsorBlock error
// into the existing pkg/ytdlp Error taxonomy. Network and invalid
// metadata remain the dominant failure modes; authentication is
// included for completeness even though SponsorBlock is
// unauthenticated. The function never returns nil for a non-nil
// input but the rendered message never includes the underlying
// error verbatim; it is reduced to the sentinel so secrets and
// URLs cannot leak through the public error chain.
func mapSponsorBlockError(err error) error {
	if err == nil {
		return nil
	}
	category := ErrorInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCancelled
	case errors.Is(err, sponsorblock.ErrInvalidInput):
		category = ErrorInvalidInput
	case errors.Is(err, sponsorblock.ErrUnsupported):
		category = ErrorUnsupported
	case errors.Is(err, sponsorblock.ErrUnavailable):
		category = ErrorInternal
	case errors.Is(err, sponsorblock.ErrNetwork):
		category = ErrorNetwork
	case errors.Is(err, sponsorblock.ErrAuthentication):
		category = ErrorAuthentication
	case errors.Is(err, sponsorblock.ErrIsolation):
		category = ErrorSecurity
	case errors.Is(err, sponsorblock.ErrInvalidMetadata):
		category = ErrorInternal
	}
	if category == ErrorCancelled {
		return &Error{Category: category, Op: "sponsorblock", Err: err}
	}
	return &Error{Category: category, Op: "sponsorblock", Err: errors.New(categorySentinel(category))}
}

// categorySentinel returns a short, static label for one public
// error category. The label never includes caller-controlled
// strings.
func categorySentinel(category ErrorCategory) string {
	switch category {
	case ErrorInvalidInput:
		return "invalid input"
	case ErrorUnsupported:
		return "unsupported"
	case ErrorNetwork:
		return "network failure"
	case ErrorAuthentication:
		return "authentication required"
	case ErrorSecurity:
		return "security violation"
	case ErrorCancelled:
		return "cancelled"
	case ErrorInternal:
		return "internal failure"
	}
	return string(category)
}
