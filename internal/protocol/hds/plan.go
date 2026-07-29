package hds

import (
	"fmt"
	"math"
	"net/url"
	"strings"
)

// Plan bounds. These mirror f4m.py's expectation that fragment counts are
// bounded; we further cap the absolute total.
const (
	maxPlanFragments = 100000
)

// Fragment is one (segment, fragment-number) pair scheduled by BuildPlan.
type Fragment struct {
	Segment uint32
	Number  uint32
	URL     string
}

// BuildPlan materializes the (segment, fragment) pairs declared by an ASRT and
// the AFRT ordering. Only segments from the first ASRT box are honored
// (matching f4m.py); the AFRT determines only the starting fragment number.
//
// Per f4m.py the same FirstSegment value is emitted for every fragment in the
// run; we do NOT add the in-run offset to FirstSegment.
func BuildPlan(bootstrap Bootstrap) ([]Fragment, error) {
	if len(bootstrap.Segments) == 0 || len(bootstrap.Fragments) == 0 {
		return nil, fmt.Errorf("%w: missing ASRT or AFRT runs", ErrInvalidManifest)
	}
	firstFragment := bootstrap.Fragments[0].First
	var total uint64
	for _, run := range bootstrap.Segments {
		if run.FragmentsPerSegment == 0 {
			continue
		}
		total += uint64(run.FragmentsPerSegment)
		if total > maxPlanFragments {
			return nil, fmt.Errorf("%w: total fragments %d", ErrTooManyFragments, total)
		}
	}
	if total == 0 {
		return nil, fmt.Errorf("%w: no fragments in ASRT", ErrInvalidManifest)
	}
	planned := make([]Fragment, 0, total)
	index := uint64(firstFragment)
	for _, run := range bootstrap.Segments {
		for offset := uint32(0); offset < run.FragmentsPerSegment; offset++ {
			planned = append(planned, Fragment{
				Segment: run.FirstSegment,
				Number:  uint32(index),
			})
			index++
			if index > math.MaxUint32 {
				return nil, fmt.Errorf("%w: fragment number overflow", ErrTooManyFragments)
			}
		}
	}
	return planned, nil
}

// ResolveFragmentURLs constructs the final F4F URL for each plan entry by
// appending `Seg<segment>-Frag<number>` to the media URL's path and merging
// query components from the existing media URL, the F4M pv-2.0 attribute, and
// any caller-provided extra query.
//
// Query order matches f4m.py: existing media query first (when present), then
// pv-2.0 (stripped of trailing semicolons), then extra params. We preserve
// every byte of the existing query — including duplicates and signed-signature
// parameters — by concatenating raw strings rather than re-encoding through
// url.Values, which would deduplicate and re-order keys.
func ResolveFragmentURLs(mediaURL string, pv2Query string, extraQuery string, plan []Fragment) ([]Fragment, error) {
	if mediaURL == "" {
		return nil, fmt.Errorf("%w: empty media URL", ErrInvalidManifest)
	}
	parsed, err := url.Parse(mediaURL)
	if err != nil {
		return nil, fmt.Errorf("%w: media URL parse", ErrInvalidManifest)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: media URL scheme", ErrInvalidManifest)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%w: media URL host", ErrInvalidManifest)
	}
	mergedQuery := buildFragmentQuery(parsed.RawQuery, pv2Query, extraQuery)
	resolved := make([]Fragment, len(plan))
	for i, item := range plan {
		name := fmt.Sprintf("Seg%d-Frag%d", item.Segment, item.Number)
		clone := *parsed
		clone.Path = parsed.Path + name
		// Clear RawPath: when set, url.URL uses it instead of Path for
		// encoding. We mutated Path so we must reset RawPath so the new path
		// is escaped consistently.
		clone.RawPath = ""
		clone.ForceQuery = false
		clone.RawQuery = mergedQuery
		resolved[i] = Fragment{Segment: item.Segment, Number: item.Number, URL: clone.String()}
	}
	return resolved, nil
}

// buildFragmentQuery concatenates the existing media query, the validated
// pv-2.0 component, and any extra query params using `&` separators. Each
// non-empty input is validated for `key=value` shape and skipped otherwise so a
// malformed pv-2.0 cannot poison the signed URL.
func buildFragmentQuery(existingRaw string, pv2 string, extra string) string {
	parts := make([]string, 0, 3)
	if existingRaw != "" {
		parts = append(parts, existingRaw)
	}
	if pv := strings.TrimSpace(pv2); pv != "" {
		pv = strings.TrimRight(pv, ";")
		if validateQueryComponent(pv) {
			parts = append(parts, pv)
		}
	}
	if ex := strings.TrimSpace(extra); ex != "" {
		if validateQueryComponent(ex) {
			parts = append(parts, ex)
		}
	}
	return strings.Join(parts, "&")
}

// validateQueryComponent enforces the `k=v(&k=v)*` shape. We deliberately do
// not parse each pair because signed queries may contain characters that would
// be re-encoded; we only enforce basic structure to reject obviously malformed
// payloads.
func validateQueryComponent(raw string) bool {
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			return false
		}
		if !strings.Contains(pair, "=") {
			return false
		}
		if strings.HasPrefix(pair, "=") || strings.HasSuffix(pair, "=") {
			return false
		}
	}
	return true
}
