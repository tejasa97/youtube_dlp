package format

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	maxSortFields = 32
	maxRegexBytes = 4096
)

var (
	fieldPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	formatIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

var ErrInvalidPreference = errors.New("invalid format preference")

// Options controls deterministic preference ordering. The zero value retains
// historical best/worst behaviour while rejecting confirmed DRM formats.
//
// Sort carries the final ordered user sort list after CLI/config accumulation
// and reset processing. Repeated CLI -S flags append fields in occurrence
// order; --format-sort-reset clears previously accumulated user fields. PR 9
// exposes those CLI operations; PR 4 documents the boundary and tests that
// Options.Sort order is preserved exactly.
//
// PreferExtensions is retained for Go API compatibility. It is inserted at
// the extension position of the canonical tuple, after quality fields. It
// must not alter the pinned oracle when empty.
//
// AllowMultipleVideoStreams and AllowMultipleAudioStreams mirror yt-dlp's
// `--allow-multiple-video-streams` / `--allow-multiple-audio-streams` flags.
// Both default to false, matching Python's pinned stream suppression
// behaviour.
type Options struct {
	Sort                      []SortField
	SortForce                 bool
	PreferFreeFormats         bool
	PreferExtensions          []string
	AllowDRM                  bool
	AllowMultipleVideoStreams bool
	AllowMultipleAudioStreams bool
}

// SortField is compatible with common yt-dlp FIELD, +FIELD, FIELD:LIMIT, and
// FIELD~LIMIT forms. Descending means lower values win; Closest selects the
// value nearest Limit. CombinedLimit captures multi-colon limit text (such
// as `ext:mp4:m4a`) so the sorter can split it across subfields during
// expansion. LimitText preserves a non-numeric ordered-field limit such as
// `vcodec:vp9`; both are mutually exclusive with Limit.
type SortField struct {
	Field         string
	Descending    bool
	Closest       bool
	Limit         *float64
	LimitText     string
	CombinedLimit string
}

// ParseSortField parses one bounded user preference token. Combined-field
// limits (multiple colons such as `ext:mp4:m4a`) are accepted by storing
// the raw limit text in CombinedLimit; the sorter expands them when it
// expands the combined field.
func ParseSortField(input string) (SortField, error) {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 256 {
		return SortField{}, fmt.Errorf("%w: empty or oversized sort field", ErrInvalidPreference)
	}
	field := SortField{}
	if input[0] == '+' {
		field.Descending, input = true, input[1:]
	}
	separator := strings.IndexAny(input, ":~")
	if separator >= 0 {
		field.Closest = input[separator] == '~'
		rawLimit := input[separator+1:]
		field.Field = strings.ToLower(input[:separator])
		if !fieldPattern.MatchString(field.Field) {
			return SortField{}, fmt.Errorf("%w: invalid field %q", ErrInvalidPreference, field.Field)
		}
		if rawLimit == "" || len(rawLimit) > 64 {
			return SortField{}, fmt.Errorf("%w: empty or oversized sort limit", ErrInvalidPreference)
		}
		if strings.ContainsAny(rawLimit, ":~") {
			// Combined-field limit. Defer numeric parsing.
			field.CombinedLimit = rawLimit
			return field, nil
		}
		limit, err := parseBoundedNumber(rawLimit)
		if err != nil {
			setting, _, known := lookupFieldSetting(field.Field)
			if known && setting.typ == fieldTypeCombined {
				field.CombinedLimit = rawLimit
				return field, nil
			}
			if !known {
				field.LimitText = rawLimit
				return field, nil
			}
			if setting.convert != "order" && setting.convert != "string" && setting.convert != "float_string" {
				return SortField{}, fmt.Errorf("%w: invalid sort limit: %v", ErrInvalidPreference, err)
			}
			field.LimitText = rawLimit
			return field, nil
		}
		field.Limit = &limit
		input = input[:separator]
	}
	if !fieldPattern.MatchString(input) {
		return SortField{}, fmt.Errorf("%w: invalid field %q", ErrInvalidPreference, input)
	}
	field.Field = strings.ToLower(input)
	return field, nil
}

func ParseSortFields(inputs []string) ([]SortField, error) {
	if len(inputs) > maxSortFields {
		return nil, fmt.Errorf("%w: more than %d sort fields", ErrInvalidPreference, maxSortFields)
	}
	fields := make([]SortField, 0, len(inputs))
	for _, input := range inputs {
		field, err := ParseSortField(input)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func (options Options) validate() error {
	if len(options.Sort) > maxSortFields || len(options.PreferExtensions) > maxSortFields {
		return fmt.Errorf("%w: preference list exceeds %d entries", ErrInvalidPreference, maxSortFields)
	}
	for _, item := range options.Sort {
		if !fieldPattern.MatchString(item.Field) {
			return fmt.Errorf("%w: invalid sort field %q", ErrInvalidPreference, item.Field)
		}
		if item.Limit != nil && (math.IsNaN(*item.Limit) || math.IsInf(*item.Limit, 0)) {
			return fmt.Errorf("%w: non-finite sort limit", ErrInvalidPreference)
		}
	}
	seen := make(map[string]struct{}, len(options.PreferExtensions))
	for _, extension := range options.PreferExtensions {
		extension = strings.ToLower(extension)
		if len(extension) == 0 || len(extension) > 16 || !formatIDPattern.MatchString(extension) {
			return fmt.Errorf("%w: invalid extension preference", ErrInvalidPreference)
		}
		if _, duplicate := seen[extension]; duplicate {
			return fmt.Errorf("%w: duplicate extension preference %q", ErrInvalidPreference, extension)
		}
		seen[extension] = struct{}{}
	}
	return nil
}

func parseBoundedNumber(input string) (float64, error) {
	if input == "" || len(input) > 64 {
		return 0, errors.New("empty or oversized number")
	}
	multiplier := float64(1)
	last := input[len(input)-1]
	switch last {
	case 'K', 'k':
		multiplier, input = 1e3, input[:len(input)-1]
	case 'M', 'm':
		multiplier, input = 1e6, input[:len(input)-1]
	case 'G', 'g':
		multiplier, input = 1e9, input[:len(input)-1]
	case 'T', 't':
		multiplier, input = 1e12, input[:len(input)-1]
	}
	parsed, err := strconv.ParseFloat(input, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, errors.New("not a finite number")
	}
	return parsed * multiplier, nil
}

func isDRM(object *value.Object) bool {
	raw := object.Lookup("has_drm")
	if drm, ok := raw.Bool(); ok {
		return drm
	}
	if text, ok := raw.StringValue(); ok {
		return text != "" && text != "maybe"
	}
	if integer, ok := raw.Int(); ok {
		return integer != 0
	}
	if floating, ok := raw.Float(); ok {
		return floating != 0
	}
	return false
}

func extensionRank(object *value.Object, preferences []string) int {
	ext, _ := object.Lookup("ext").StringValue()
	for index, preference := range preferences {
		if strings.EqualFold(ext, preference) {
			return len(preferences) - index
		}
	}
	return 0
}

func freeRank(object *value.Object) int {
	ext, _ := object.Lookup("ext").StringValue()
	if ext == "webm" || ext == "ogg" || ext == "opus" {
		return 1
	}
	return 0
}

// extractorPreference is retained for the legacy atom-match scoring path used
// by quality and extension selectors. PR 5 replaces this path; PR 4 leaves
// it untouched. The pinned canonical sort path computes preference through
// the new sorter.
func extractorPreference(object *value.Object) float64 {
	preference, ok := numeric(object.Lookup("preference"))
	if !ok || math.IsNaN(preference) || math.IsInf(preference, 0) {
		return 0
	}
	return preference
}
