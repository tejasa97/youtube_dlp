package format

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxSortFields = 32
	maxRegexBytes = 4096
)

var ErrInvalidPreference = errors.New("invalid format preference")

// Options controls deterministic preference ordering. The zero value retains
// historical best/worst behaviour while rejecting confirmed DRM formats.
type Options struct {
	Sort              []SortField
	PreferFreeFormats bool
	PreferExtensions  []string
	AllowDRM          bool
}

// SortField is compatible with common yt-dlp FIELD, +FIELD, FIELD:LIMIT, and
// FIELD~LIMIT forms. Descending means lower values win; Closest selects the
// value nearest Limit.
type SortField struct {
	Field      string
	Descending bool
	Closest    bool
	Limit      *float64
}

// ParseSortField parses one bounded user preference token.
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
		limit, err := parseBoundedNumber(input[separator+1:])
		if err != nil {
			return SortField{}, fmt.Errorf("%w: invalid sort limit: %v", ErrInvalidPreference, err)
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
	drm, ok := object.Lookup("has_drm").Bool()
	return ok && drm
}

func orderFormats(formats []*value.Object, options Options) []*value.Object {
	ordered := append([]*value.Object(nil), formats...)
	sort.SliceStable(ordered, func(leftIndex, rightIndex int) bool {
		left, right := ordered[leftIndex], ordered[rightIndex]
		if l, r := extractorPreference(left), extractorPreference(right); l != r {
			return l > r
		}
		for _, field := range options.Sort {
			if cmp := compareSortField(left, right, field); cmp != 0 {
				return cmp > 0
			}
		}
		if len(options.PreferExtensions) > 0 {
			l, r := extensionRank(left, options.PreferExtensions), extensionRank(right, options.PreferExtensions)
			if l != r {
				return l > r
			}
		}
		if options.PreferFreeFormats {
			l, r := freeRank(left), freeRank(right)
			if l != r {
				return l > r
			}
		}
		return false
	})
	return ordered
}

func extractorPreference(object *value.Object) float64 {
	preference, ok := numeric(object.Lookup("preference"))
	if !ok || math.IsNaN(preference) || math.IsInf(preference, 0) {
		return 0
	}
	return preference
}

func compareSortField(left, right *value.Object, field SortField) int {
	l, lOK := numeric(left.Lookup(field.Field))
	r, rOK := numeric(right.Lookup(field.Field))
	if !lOK || !rOK {
		ls, lsOK := left.Lookup(field.Field).StringValue()
		rs, rsOK := right.Lookup(field.Field).StringValue()
		if !lsOK || !rsOK {
			if lsOK {
				return 1
			}
			if rsOK {
				return -1
			}
			return 0
		}
		if ls == rs {
			return 0
		}
		if field.Descending {
			if ls < rs {
				return 1
			}
			return -1
		}
		if ls > rs {
			return 1
		}
		return -1
	}
	if field.Closest && field.Limit != nil {
		l, r = -math.Abs(l-*field.Limit), -math.Abs(r-*field.Limit)
	}
	if l == r {
		return 0
	}
	if field.Descending {
		if l < r {
			return 1
		}
		return -1
	}
	if l > r {
		return 1
	}
	return -1
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
