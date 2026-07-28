package format

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Limits enforced by the pinned FormatSorter and matched by the Go port.
const (
	maxUserSortFields      = 32
	maxExtractorSortFields = 32
	maxEffectiveSortFields = 64
	maxSortSpecBytes       = 256
	maxSortSpecLength      = maxSortSpecBytes
	maxCombinedLimitTokens = 8
)

// sortToken is the parsed representation of one raw sort specification. It
// records the per-field overrides applied during composition so the comparator
// can consume them without re-parsing.
type sortToken struct {
	canonical  string
	deprecated bool
	reverse    bool
	closest    bool
	limitText  string
	limit      *float64
}

// sortRegex matches the pinned FormatSorter regex:
//
//	r' *((?P<reverse>\+)?(?P<field>[a-zA-Z0-9_]+)((?P<separator>[~:])(?P<limit>.*?))?)? *$'
//
// leading and trailing whitespace is consumed implicitly by anchoring at the
// optional field clause.
var sortRegex = regexp.MustCompile(`^\s*(?P<reverse>\+?)(?P<field>[A-Za-z0-9_]+)(?:(?P<separator>[:~])(?P<limit>.*?))?\s*$`)

// parseSortSpecification parses one raw sort field specification and returns
// the per-field overrides. Empty, malformed, or oversized inputs return
// ErrInvalidPreference. Combined-field limits (multiple colons such as
// `ext:mp4:m4a`) keep the full limit text intact on the token so the
// sorter can split it across the subfields during expansion; a single
// colon still produces a numeric limit for backward compatibility.
func parseSortSpecification(input string) (sortToken, error) {
	if input == "" || len(input) > maxSortSpecLength {
		return sortToken{}, fmt.Errorf("%w: empty or oversized sort field %q", ErrInvalidPreference, input)
	}
	matches := sortRegex.FindStringSubmatch(input)
	if matches == nil {
		return sortToken{}, fmt.Errorf("%w: invalid sort specification %q", ErrInvalidPreference, input)
	}
	if matches[2] == "" {
		return sortToken{}, fmt.Errorf("%w: empty field name in %q", ErrInvalidPreference, input)
	}
	token := sortToken{
		canonical: strings.ToLower(matches[2]),
		reverse:   strings.Contains(matches[1], "+"),
		limitText: matches[4],
	}
	if matches[3] == "~" {
		token.closest = true
	}
	if matches[3] != "" && token.limitText == "" {
		return sortToken{}, fmt.Errorf("%w: empty sort limit in %q", ErrInvalidPreference, input)
	}
	if token.limitText != "" {
		if len(token.limitText) > 64 {
			return sortToken{}, fmt.Errorf("%w: oversized sort limit in %q", ErrInvalidPreference, input)
		}
		if !strings.Contains(token.limitText, ":") {
			if limit, err := parseBoundedNumber(token.limitText); err == nil {
				token.limit = &limit
			}
		}
	}
	setting, target, ok := lookupFieldSetting(token.canonical)
	if !ok {
		// Allow arbitrary syntactically valid field names; their comparison
		// falls back to the generic float-or-string conversion handled by the
		// preference generator. Track the literal name as the canonical.
		token.canonical = strings.ToLower(matches[2])
		return token, nil
	}
	token.canonical = target
	token.deprecated = setting.deprecated
	if token.limitText != "" && token.limit == nil && setting.typ != fieldTypeCombined &&
		setting.convert != "order" && setting.convert != "string" && setting.convert != "float_string" {
		return sortToken{}, fmt.Errorf("%w: invalid sort limit %q for %s", ErrInvalidPreference, token.limitText, token.canonical)
	}
	return token, nil
}

// encodeSortFieldToken re-emits a parsed SortField as the raw specification
// string accepted by parseSortSpecification. It preserves descending and
// closest markers as well as the resolved numeric limit text.
func encodeSortFieldToken(field SortField) string {
	var builder strings.Builder
	if field.Descending {
		builder.WriteByte('+')
	}
	builder.WriteString(field.Field)
	switch {
	case field.CombinedLimit != "":
		if field.Closest {
			builder.WriteByte('~')
		} else {
			builder.WriteByte(':')
		}
		builder.WriteString(field.CombinedLimit)
	case field.LimitText != "":
		if field.Closest {
			builder.WriteByte('~')
		} else {
			builder.WriteByte(':')
		}
		builder.WriteString(field.LimitText)
	case field.Limit != nil:
		if field.Closest {
			builder.WriteByte('~')
		} else {
			builder.WriteByte(':')
		}
		builder.WriteString(formatLimitText(*field.Limit))
	}
	return builder.String()
}

func formatLimitText(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return ""
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// sorter captures the resolved field order and per-field overrides derived
// from user, extractor, and default sources. The struct is constructed once
// per Prepare call and the resulting keys are precomputed per format before
// the stable sort runs.
type sorter struct {
	fields       []sortToken
	settings     []*sorterFieldSetting
	useFreeOrder bool
}

// newSorter composes the effective field-order using the pinned first-
// occurrence-wins rule. options.Sort carries the finalized ordered user sort
// list after CLI/config accumulation and reset processing. extractorFields
// carries the strings from the canonical _format_sort_fields list on Info.
// options.SortForce controls whether priority default fields are inserted.
func newSorter(options Options, info value.Info, extractorFields []string) (*sorter, error) {
	if len(options.Sort) > maxUserSortFields {
		return nil, fmt.Errorf("%w: more than %d user sort fields", ErrInvalidPreference, maxUserSortFields)
	}
	if len(extractorFields) > maxExtractorSortFields {
		return nil, fmt.Errorf("%w: more than %d extractor sort fields", ErrInvalidPreference, maxExtractorSortFields)
	}
	sortForce := options.SortForce
	rawList := make([]string, 0, len(pinnedDefaultOrder)*2)
	for _, field := range pinnedDefaultOrder {
		if !sortForce {
			continue
		}
		if setting, _, ok := lookupFieldSetting(field); ok && setting.forced {
			rawList = append(rawList, field)
		}
	}
	for _, field := range pinnedDefaultOrder {
		if sortForce {
			continue
		}
		if setting, _, ok := lookupFieldSetting(field); ok && (setting.forced || setting.priority) {
			rawList = append(rawList, field)
		}
	}
	for _, field := range options.Sort {
		rawList = append(rawList, encodeSortFieldToken(field))
	}
	rawList = append(rawList, extractorFields...)
	rawList = append(rawList, pinnedDefaultOrder...)

	tokens := make([]sortToken, 0, len(rawList))
	settings := make([]*sorterFieldSetting, 0, len(rawList))
	seen := make(map[string]struct{}, len(rawList))

	for _, raw := range rawList {
		if raw == "" {
			continue
		}
		token, err := parseSortSpecification(raw)
		if err != nil {
			return nil, err
		}
		if _, already := seen[token.canonical]; already {
			continue
		}
		setting, _, known := lookupFieldSetting(token.canonical)
		if !known {
			// Arbitrary user field. Keep the literal token for comparison;
			// its lookup target is the same as the canonical name and the
			// preference generator will fall back to float/string conversion.
			setting = &sorterFieldSetting{
				canonical: token.canonical,
				typ:       fieldTypeField,
				field:     []string{token.canonical},
				convert:   "float_string",
			}
			if token.limitText != "" && token.limit == nil {
				setting.convert = "string"
			}
		} else if setting.convert == "float_string" && token.limitText != "" && token.limit == nil {
			copy := *setting
			copy.convert = "string"
			setting = &copy
		}
		resolveSortTokenLimit(&token, setting, options.PreferFreeFormats)
		if setting.typ == fieldTypeCombined {
			combined := expandCombinedLimits(token, setting)
			// Mark the combined field itself as seen so the trailing default
			// occurrence is deduplicated.
			seen[token.canonical] = struct{}{}
			for _, sub := range combined {
				if _, already := seen[sub.canonical]; already {
					continue
				}
				subSetting, _, ok := lookupFieldSetting(sub.canonical)
				if !ok {
					subSetting = &sorterFieldSetting{
						canonical: sub.canonical,
						typ:       fieldTypeField,
						field:     []string{sub.canonical},
						convert:   "float_string",
					}
				}
				resolveSortTokenLimit(&sub, subSetting, options.PreferFreeFormats)
				tokens = append(tokens, sub)
				settings = append(settings, subSetting)
				seen[sub.canonical] = struct{}{}
			}
			continue
		}
		tokens = append(tokens, token)
		settings = append(settings, setting)
		seen[token.canonical] = struct{}{}
	}

	if len(tokens) > maxEffectiveSortFields {
		return nil, fmt.Errorf("%w: more than %d effective sort fields", ErrInvalidPreference, maxEffectiveSortFields)
	}

	return &sorter{
		fields:       tokens,
		settings:     settings,
		useFreeOrder: options.PreferFreeFormats,
	}, nil
}

func resolveSortTokenLimit(token *sortToken, setting *sorterFieldSetting, useFreeOrder bool) {
	if token == nil || setting == nil || token.limitText == "" || setting.convert != "order" {
		return
	}
	if limit, ok := orderedRank(setting, strings.ToLower(token.limitText), useFreeOrder); ok {
		token.limit = &limit
	}
}

// expandCombinedLimits turns a combined-field sort specification such as
// `ext:video_limit:audio_limit` into a list of sub-field sortTokens that the
// sorter records individually. The combined field's own canonical name is
// omitted from the result; the order of sub-fields matches the canonical
// combination order (vcodec, acodec for codec; vext, aext for ext).
func expandCombinedLimits(token sortToken, setting *sorterFieldSetting) []sortToken {
	var parts []string
	if token.limitText != "" {
		parts = strings.Split(token.limitText, ":")
	}
	if len(parts) > maxCombinedLimitTokens {
		parts = parts[:maxCombinedLimitTokens]
	}
	sub := make([]sortToken, len(setting.field))
	for index, field := range setting.field {
		limitText := ""
		if index < len(parts) {
			limitText = parts[index]
		} else if len(parts) > 0 {
			limitText = parts[0]
		}
		subToken := sortToken{
			canonical:  field,
			deprecated: token.deprecated,
			reverse:    token.reverse,
			closest:    token.closest,
			limitText:  limitText,
		}
		if limitText != "" {
			limit, err := parseBoundedNumber(limitText)
			if err == nil {
				subToken.limit = &limit
			}
		}
		sub[index] = subToken
	}
	return sub
}

// calculatePreference precomputes the comparison key tuple for one format.
// The format object must already have the pinned observable derived fields
// applied; the canonical metadata is mutated exactly once during preparation.
// The returned slice is stable across calls.
func (s *sorter) calculatePreference(format *value.Object) []sortFieldPreference {
	if s == nil {
		return nil
	}
	key := make([]sortFieldPreference, len(s.fields))
	for index := range s.fields {
		token := s.fields[index]
		setting := s.settings[index]
		preference := calculateFieldPreference(format, setting, token, s.useFreeOrder)
		key[index] = preference
	}
	return key
}

// calculateFieldPreference applies the pinned _calculate_field_preference_
// logic for a single canonical field against the supplied format object.
func calculateFieldPreference(format *value.Object, setting *sorterFieldSetting, token sortToken, useFreeOrder bool) sortFieldPreference {
	derived := deriveFieldValue(setting, format)
	return preferenceForValue(setting, token, derived, useFreeOrder)
}

// preferenceForValue computes the (-10, 0) / (-1, value, 0) / (0, ...) /
// (1, value, 0) tuple for a single derived value, applying the field's
// reverse/closest/limit overrides. The setting.typ extractor type applies the
// pinned _calculate_field_preference_from_value rule that collapses None or
// values at/over max to -1. Fields with convert="string" always emit the
// (1, value, 0) class so strings sort above numerics per the pinned
// _calculate_field_preference_from_value rule.
func preferenceForValue(setting *sorterFieldSetting, token sortToken, raw value.Value, useFreeOrder bool) sortFieldPreference {
	if setting.convert == "string" {
		if raw.IsMissing() || raw.IsNull() {
			return missingPreference()
		}
		text, ok := raw.StringValue()
		if !ok {
			return missingPreference()
		}
		return stringPreference(text)
	}
	if setting.typ == fieldTypeExtractor {
		value, ok := numericValue(raw)
		if !ok {
			return numericPreference(-1, 0)
		}
		if setting.maxSet && setting.max != nil && value >= *setting.max {
			return numericPreference(-1, 0)
		}
		return numericPreference(value, 0)
	}
	if setting.convert == "float_string" {
		if number, ok := numericValue(raw); ok {
			return limitedNumericPreference(number, token)
		}
		if text, ok := raw.StringValue(); ok {
			return stringPreference(text)
		}
		return missingPreference()
	}
	if raw.IsMissing() || raw.IsNull() {
		if setting.convert == "order" {
			if resolved, ok := orderedNoneRank(setting, useFreeOrder); ok {
				return limitedNumericPreference(resolved, token)
			}
		}
		if setting.hasDefault {
			return limitedNumericPreference(setting.defaultVal, token)
		}
		return missingPreference()
	}
	resolved, ok := resolveFieldValue(setting, raw, useFreeOrder)
	if !ok {
		if setting.hasDefault {
			return limitedNumericPreference(setting.defaultVal, token)
		}
		return missingPreference()
	}
	return limitedNumericPreference(resolved, token)
}

func limitedNumericPreference(resolved float64, token sortToken) sortFieldPreference {
	limit, reverse, closest := token.limit, token.reverse, token.closest

	switch {
	case closest && limit != nil:
		secondary := resolved - *limit
		if !reverse {
			secondary = *limit - resolved
		}
		return numericPreference(-mathAbs(resolved-*limit), secondary)
	case limit == nil:
		if reverse {
			return numericPreference(-resolved, 0)
		}
		return numericPreference(resolved, 0)
	case resolved <= *limit:
		if reverse {
			return numericPreference(-resolved, 0)
		}
		return numericPreference(resolved, 0)
	default:
		return numericPreference(-resolved, 0)
	}
}

func numericValue(raw value.Value) (float64, bool) {
	if raw.IsMissing() || raw.IsNull() {
		return 0, false
	}
	if integer, ok := raw.Int(); ok {
		return float64(integer), true
	}
	if floating, ok := raw.Float(); ok {
		if math.IsNaN(floating) || math.IsInf(floating, 0) {
			return 0, false
		}
		return floating, true
	}
	if text, ok := raw.StringValue(); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// sortStable is the comparator and sort driver. Inputs are cloned and may be
// observed to be in any order; the result is a fresh slice ordered worst to
// best according to the precomputed preference tuple.
func (s *sorter) sortStable(formats []*value.Object) []*value.Object {
	if len(formats) == 0 {
		return formats
	}
	keys := make([][]sortFieldPreference, len(formats))
	for index, format := range formats {
		keys[index] = s.calculatePreference(format)
	}
	ordered := make([]*value.Object, len(formats))
	copy(ordered, formats)
	sort.SliceStable(ordered, func(leftIndex, rightIndex int) bool {
		left := ordered[leftIndex]
		right := ordered[rightIndex]
		leftKey := keys[originalIndex(formats, left)]
		rightKey := keys[originalIndex(formats, right)]
		// Return true when left is strictly worse than right so the final
		// order is worst-to-best. Equal keys return false to preserve
		// the original input order.
		return comparePreferenceTuples(leftKey, rightKey) < 0
	})
	return ordered
}

// comparePreferenceTuples compares two preference key tuples field-by-field
// using the typed comparator. Returns -1 when left is worse, +1 when left is
// better, and 0 when both tuples are equal.
func comparePreferenceTuples(left, right []sortFieldPreference) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if cmp := compareFieldPreferences(left[index], right[index]); cmp != 0 {
			return cmp
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

// originalIndex returns the original index of the supplied format pointer in
// the precomputed keys slice. The lookup is exact and stable; the sorter uses
// pointer identity rather than equality to keep the lookup deterministic and
// to preserve metadata/header association across the sort.
func originalIndex(formats []*value.Object, target *value.Object) int {
	for index, current := range formats {
		if current == target {
			return index
		}
	}
	return 0
}

// mathAbs mirrors math.Abs but avoids importing math in this file's hot
// paths.
func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// applyExtensionTiebreaker sorts a worst-to-best canonical list using the
// supplied extension preference list. It is applied only after the pinned
// preference tuple compares equal and only when the legacy Go option is set.
// The Go option is documented as a deliberate legacy override; the pinned
// oracle is unchanged when PreferExtensions is empty.
func applyExtensionTiebreaker(formats []*value.Object, preferences []string) []*value.Object {
	if len(preferences) == 0 || len(formats) <= 1 {
		return formats
	}
	out := make([]*value.Object, len(formats))
	copy(out, formats)
	sort.SliceStable(out, func(leftIndex, rightIndex int) bool {
		left := extensionRank(out[leftIndex], preferences)
		right := extensionRank(out[rightIndex], preferences)
		return left > right
	})
	return out
}

// extractExtractorSortFields returns the canonical sort fields from the
// extractor-provided _format_sort_fields metadata. missing/null return an
// empty slice; non-list inputs or list members that are not strings return
// ErrInvalidPreference.
func extractExtractorSortFields(info value.Info) ([]string, error) {
	raw := info.Lookup("_format_sort_fields")
	if raw.IsMissing() || raw.IsNull() {
		return nil, nil
	}
	list, ok := raw.ListValue()
	if !ok {
		return nil, fmt.Errorf("%w: _format_sort_fields must be a list", ErrInvalidPreference)
	}
	if len(list) > maxExtractorSortFields {
		return nil, fmt.Errorf("%w: _format_sort_fields exceeds %d entries", ErrInvalidPreference, maxExtractorSortFields)
	}
	out := make([]string, 0, len(list))
	for index, item := range list {
		text, ok := item.StringValue()
		if !ok {
			return nil, fmt.Errorf("%w: _format_sort_fields[%d] is not a string", ErrInvalidPreference, index)
		}
		out = append(out, text)
	}
	return out, nil
}
