package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
)

var errInvalidPlaylistItems = errors.New("invalid playlist items")

const (
	maxPlaylistItemSpecBytes = 4 << 10
	maxPlaylistItemSegments  = 256
	maxPlaylistItemMagnitude = 1_000_000_000
)

var playlistItemSegmentPattern = regexp.MustCompile(`^([+-]?[0-9]+)?([:-]([+-]?[0-9]+|inf(inite)?)?(:([+-]?[0-9]+))?)?$`)

type playlistItemSpec struct {
	start       *int64
	end         *int64
	endInfinite bool
	step        int64
}

func parsePlaylistItems(input string) ([]playlistItemSpec, error) {
	if input == "" || len(input) > maxPlaylistItemSpecBytes {
		return nil, errInvalidPlaylistItems
	}
	segments := strings.Split(input, ",")
	if len(segments) > maxPlaylistItemSegments {
		return nil, errInvalidPlaylistItems
	}
	specs := make([]playlistItemSpec, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			return nil, errInvalidPlaylistItems
		}
		match := playlistItemSegmentPattern.FindStringSubmatch(segment)
		if match == nil {
			return nil, fmt.Errorf("%w: segment syntax", errInvalidPlaylistItems)
		}
		start, err := parsePlaylistItemInteger(match[1])
		if err != nil {
			return nil, err
		}
		if match[2] == "" {
			if start == nil {
				return nil, errInvalidPlaylistItems
			}
			value := *start
			specs = append(specs, playlistItemSpec{start: &value, end: &value, step: 1})
			continue
		}
		endInfinite := match[3] == "inf" || match[3] == "infinite"
		var end *int64
		if !endInfinite {
			end, err = parsePlaylistItemInteger(match[3])
			if err != nil {
				return nil, err
			}
		}
		step := int64(1)
		if match[6] != "" {
			parsed, parseErr := parsePlaylistItemInteger(match[6])
			if parseErr != nil {
				return nil, parseErr
			}
			step = *parsed
		}
		if step == 0 {
			return nil, fmt.Errorf("%w: zero step", errInvalidPlaylistItems)
		}
		specs = append(specs, playlistItemSpec{start: start, end: end, endInfinite: endInfinite, step: step})
	}
	return specs, nil
}

func playlistItemsOverrideRange(options PlaylistOptions) bool {
	if options.Items == "" {
		return false
	}
	start, end := normalizedPlaylistRange(options)
	return start != 1 || end != 0
}

func parsePlaylistItemInteger(raw string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < -maxPlaylistItemMagnitude || value > maxPlaylistItemMagnitude {
		return nil, fmt.Errorf("%w: integer bound", errInvalidPlaylistItems)
	}
	return &value, nil
}

func expandPlaylistItemSpecs(ctx context.Context, specs []playlistItemSpec, length int) ([]int, error) {
	seen := make(map[int]struct{}, length)
	result := make([]int, 0)
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start := playlistItemStart(spec, length)
		stop := playlistItemStop(spec, length)
		if spec.step > 0 {
			for index := 0; index < length; index++ {
				if index&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				position := int64(index)
				if position < start || position >= stop || (position-start)%spec.step != 0 {
					continue
				}
				if _, exists := seen[index]; !exists {
					seen[index] = struct{}{}
					result = append(result, index+1)
				}
			}
			continue
		}
		step := -spec.step
		for index := length - 1; index >= 0; index-- {
			if index&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			position := int64(index)
			if position > start || position <= stop || (start-position)%step != 0 {
				continue
			}
			if _, exists := seen[index]; !exists {
				seen[index] = struct{}{}
				result = append(result, index+1)
			}
		}
	}
	return result, nil
}

func playlistItemStart(spec playlistItemSpec, length int) int64 {
	if spec.start == nil {
		if spec.step > 0 {
			return 0
		}
		return int64(length - 1)
	}
	return resolvePlaylistItemIndex(*spec.start, length)
}

func playlistItemStop(spec playlistItemSpec, length int) int64 {
	if spec.endInfinite {
		return maxPlaylistItemMagnitude + 1
	}
	if spec.end == nil {
		if spec.step > 0 {
			return maxPlaylistItemMagnitude + 1
		}
		return -1
	}
	stop := resolvePlaylistItemIndex(*spec.end, length)
	if spec.step > 0 {
		return stop + 1
	}
	return stop - 1
}

func resolvePlaylistItemIndex(index int64, length int) int64 {
	if index >= 0 {
		return index - 1
	}
	return int64(length) + index
}

func finitePlaylistItemCollectionLimit(ctx context.Context, specs []playlistItemSpec) (int, bool, error) {
	limit := int64(0)
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		if spec.step <= 0 || spec.endInfinite || spec.start != nil && *spec.start < 0 || spec.end == nil || *spec.end < 0 {
			return 0, false, nil
		}
		start := int64(0)
		if spec.start != nil {
			start = *spec.start - 1
		}
		if start < 0 {
			start += ((-start + spec.step - 1) / spec.step) * spec.step
		}
		stop := *spec.end
		if start >= stop {
			continue
		}
		last := start + ((stop-1-start)/spec.step)*spec.step + 1
		if last > maxPlaylistEntries {
			return maxPlaylistEntries + 1, true, nil
		}
		if last > limit {
			limit = last
		}
	}
	return int(limit), true, nil
}

type playlistItemsIterator struct {
	source   extractor.EntryIterator
	specs    []playlistItemSpec
	entries  []indexedPlaylistEntry
	position int
	loaded   bool
	done     bool
}

func (iterator *playlistItemsIterator) Next(ctx context.Context) (indexedPlaylistEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		iterator.done = true
		return indexedPlaylistEntry{}, false, err
	}
	if iterator.done {
		return indexedPlaylistEntry{}, false, nil
	}
	if !iterator.loaded {
		if err := iterator.load(ctx); err != nil {
			iterator.done = true
			return indexedPlaylistEntry{}, false, err
		}
	}
	if iterator.position >= len(iterator.entries) {
		iterator.done = true
		return indexedPlaylistEntry{}, false, nil
	}
	entry := iterator.entries[iterator.position]
	iterator.position++
	return entry, true, nil
}

func (iterator *playlistItemsIterator) load(ctx context.Context) error {
	iterator.loaded = true
	limit, finite, err := finitePlaylistItemCollectionLimit(ctx, iterator.specs)
	if err != nil {
		return err
	}
	entries := make([]extractor.Entry, 0)
	for !finite || len(entries) < limit {
		entry, ok, nextErr := iterator.source.Next(ctx)
		if nextErr != nil {
			return nextErr
		}
		if !ok {
			break
		}
		if len(entries) >= maxPlaylistEntries {
			return extractor.ErrPlaylistLimit
		}
		entries = append(entries, entry)
	}
	indexes, err := expandPlaylistItemSpecs(ctx, iterator.specs, len(entries))
	if err != nil {
		return err
	}
	iterator.entries = make([]indexedPlaylistEntry, 0, len(indexes))
	for _, index := range indexes {
		iterator.entries = append(iterator.entries, indexedPlaylistEntry{Entry: entries[index-1], SourceIndex: index})
	}
	return nil
}
