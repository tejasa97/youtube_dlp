package hls

import (
	"errors"
	"fmt"
	"math"
	"time"
)

var (
	ErrInvalidDiscontinuityGroups = errors.New("invalid HLS discontinuity groups")
	ErrUnknownDiscontinuityGroup  = errors.New("unknown HLS discontinuity group")
	ErrNoSelectableGroup          = errors.New("HLS playlist has no selectable discontinuity group")
)

const maxDiscontinuityGroups = maxPlaylistEntries

// DiscontinuityGroupID is the stable identity of a discontinuity group.
//
// DiscontinuitySequence is the absolute value established by
// EXT-X-DISCONTINUITY-SEQUENCE and incremented by each exact
// EXT-X-DISCONTINUITY tag. It is deliberately not the snapshot-local Index
// exposed by DiscontinuityGroup.
type DiscontinuityGroupID struct {
	DiscontinuitySequence int64
}

// DiscontinuitySelection selects at most one group from an already-selected
// media playlist. A nil GroupID selects the first output-eligible group in
// playlist order. Multi-group selection is owned by a later product layer.
type DiscontinuitySelection struct {
	GroupID *DiscontinuityGroupID
}

// MapTransition identifies an initialization-map change in a group. The
// SegmentIndex refers to the canonical group segment list, after partial
// segments superseded by a complete segment have been removed.
type MapTransition struct {
	SegmentIndex int
	Map          *Map
}

// DiscontinuityGroup is one contiguous HLS discontinuity epoch visible in a
// parsed media-playlist snapshot. Index is display-only; ID is the stable HLS
// identity that callers must retain across snapshots.
type DiscontinuityGroup struct {
	Index int
	ID    DiscontinuityGroupID

	// Segments includes canonical ad and media segments. Advertisement
	// segments remain available for protocol metadata; selection plans omit
	// them from their downloadable Segments field.
	Segments []Segment

	MapTransitions        []MapTransition
	FirstSequence         int64
	LastSequence          int64
	Duration              time.Duration
	MediaDuration         time.Duration
	SegmentCount          int
	MediaSegments         int
	AdvertisementSegments int
	PartialSegments       int
	Selectable            bool
}

// DiscontinuitySelectionPlan is the protocol-level plan for one selected
// discontinuity group. It contains no destination, archive, transaction, or
// user-facing filename state.
type DiscontinuitySelectionPlan struct {
	Group    DiscontinuityGroup
	Segments []Segment
}

// SelectionPlan is retained as a concise alias for integration code that does
// not need the longer HLS-specific name.
type SelectionPlan = DiscontinuitySelectionPlan

// BuildDiscontinuityGroups expands an already-selected HLS media playlist
// into deterministic, snapshot-local display groups. Group identity follows
// the playlist's absolute discontinuity sequence, not the display index.
//
// Parts and their later complete segment are canonicalized so a caller never
// receives both forms for the same media sequence. Advertisement segments are
// retained in the group metadata and excluded only when a selection plan is
// built.
func BuildDiscontinuityGroups(media *MediaPlaylist) ([]DiscontinuityGroup, error) {
	if media == nil {
		return nil, fmt.Errorf("%w: media playlist is nil", ErrInvalidDiscontinuityGroups)
	}
	if len(media.Segments) > maxPlaylistEntries {
		return nil, fmt.Errorf("%w: segment count exceeds %d", ErrInvalidDiscontinuityGroups, maxPlaylistEntries)
	}
	if len(media.Segments) == 0 {
		return []DiscontinuityGroup{}, nil
	}

	rawGroups := make([][]Segment, 0, 1)
	groupIDs := make([]DiscontinuityGroupID, 0, 1)
	seenIDs := make(map[DiscontinuityGroupID]struct{}, 1)
	currentID := DiscontinuityGroupID{DiscontinuitySequence: media.Segments[0].DiscontinuitySequence}
	if currentID.DiscontinuitySequence < 0 {
		return nil, fmt.Errorf("%w: negative discontinuity sequence %d", ErrInvalidDiscontinuityGroups, currentID.DiscontinuitySequence)
	}
	if currentID.DiscontinuitySequence < media.DiscontinuitySequence {
		return nil, fmt.Errorf("%w: first segment discontinuity sequence %d precedes playlist sequence %d", ErrInvalidDiscontinuityGroups, currentID.DiscontinuitySequence, media.DiscontinuitySequence)
	}
	current := make([]Segment, 0, len(media.Segments))

	finish := func() error {
		if len(current) == 0 {
			return nil
		}
		if len(rawGroups) >= maxDiscontinuityGroups {
			return fmt.Errorf("%w: group count exceeds %d", ErrInvalidDiscontinuityGroups, maxDiscontinuityGroups)
		}
		if _, duplicate := seenIDs[currentID]; duplicate {
			return fmt.Errorf("%w: discontinuity sequence %d occurs in multiple groups", ErrInvalidDiscontinuityGroups, currentID.DiscontinuitySequence)
		}
		seenIDs[currentID] = struct{}{}
		rawGroups = append(rawGroups, current)
		groupIDs = append(groupIDs, currentID)
		current = make([]Segment, 0)
		return nil
	}

	for index, segment := range media.Segments {
		if segment.DiscontinuitySequence < 0 {
			return nil, fmt.Errorf("%w: segment %d has negative discontinuity sequence %d", ErrInvalidDiscontinuityGroups, index, segment.DiscontinuitySequence)
		}
		if segment.Duration < 0 {
			return nil, fmt.Errorf("%w: segment %d has negative duration", ErrInvalidDiscontinuityGroups, index)
		}
		segmentID := DiscontinuityGroupID{DiscontinuitySequence: segment.DiscontinuitySequence}
		if segmentID != currentID {
			if segmentID.DiscontinuitySequence < currentID.DiscontinuitySequence {
				return nil, fmt.Errorf("%w: discontinuity sequence regresses from %d to %d", ErrInvalidDiscontinuityGroups, currentID.DiscontinuitySequence, segmentID.DiscontinuitySequence)
			}
			if err := finish(); err != nil {
				return nil, err
			}
			currentID = segmentID
		}
		if len(current) > 0 && segment.Discontinuity && segmentID == currentID {
			return nil, fmt.Errorf("%w: segment %d repeats a discontinuity boundary without changing sequence", ErrInvalidDiscontinuityGroups, index)
		}
		current = append(current, segment)
	}
	if err := finish(); err != nil {
		return nil, err
	}

	groups := make([]DiscontinuityGroup, len(rawGroups))
	for index, raw := range rawGroups {
		canonical, err := canonicalizeGroupSegments(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: group %d: %w", ErrInvalidDiscontinuityGroups, index, err)
		}
		group, err := summarizeDiscontinuityGroup(index, groupIDs[index], canonical)
		if err != nil {
			return nil, fmt.Errorf("%w: group %d: %w", ErrInvalidDiscontinuityGroups, index, err)
		}
		groups[index] = group
	}
	return groups, nil
}

// BuildDiscontinuitySelectionPlan builds a plan for one group. With the zero
// selection, the first group containing at least one non-ad segment is chosen;
// this makes empty and ad-only groups produce no output while preserving
// deterministic playlist order.
func BuildDiscontinuitySelectionPlan(media *MediaPlaylist, selection DiscontinuitySelection) (DiscontinuitySelectionPlan, error) {
	groups, err := BuildDiscontinuityGroups(media)
	if err != nil {
		return DiscontinuitySelectionPlan{}, err
	}

	var selected *DiscontinuityGroup
	if selection.GroupID == nil {
		for index := range groups {
			if groups[index].Selectable {
				selected = &groups[index]
				break
			}
		}
		if selected == nil {
			return DiscontinuitySelectionPlan{}, ErrNoSelectableGroup
		}
	} else {
		if selection.GroupID.DiscontinuitySequence < 0 {
			return DiscontinuitySelectionPlan{}, fmt.Errorf("%w: negative discontinuity sequence %d", ErrUnknownDiscontinuityGroup, selection.GroupID.DiscontinuitySequence)
		}
		for index := range groups {
			if groups[index].ID == *selection.GroupID {
				selected = &groups[index]
				break
			}
		}
		if selected == nil {
			return DiscontinuitySelectionPlan{}, fmt.Errorf("%w: discontinuity sequence %d", ErrUnknownDiscontinuityGroup, selection.GroupID.DiscontinuitySequence)
		}
		if !selected.Selectable {
			return DiscontinuitySelectionPlan{}, fmt.Errorf("%w: discontinuity sequence %d", ErrNoSelectableGroup, selection.GroupID.DiscontinuitySequence)
		}
	}

	plan := DiscontinuitySelectionPlan{Group: cloneDiscontinuityGroup(*selected)}
	plan.Segments = make([]Segment, 0, selected.MediaSegments)
	for _, segment := range selected.Segments {
		if !segment.Advertisement {
			plan.Segments = append(plan.Segments, cloneSegment(segment))
		}
	}
	return plan, nil
}

// BuildDefaultDiscontinuitySelectionPlan selects the first output-eligible
// group in deterministic playlist order.
func BuildDefaultDiscontinuitySelectionPlan(media *MediaPlaylist) (DiscontinuitySelectionPlan, error) {
	return BuildDiscontinuitySelectionPlan(media, DiscontinuitySelection{})
}

// BuildSelectionPlan is the concise selection-plan entry point for
// integration code. It is equivalent to BuildDiscontinuitySelectionPlan.
func BuildSelectionPlan(media *MediaPlaylist, selection DiscontinuitySelection) (SelectionPlan, error) {
	return BuildDiscontinuitySelectionPlan(media, selection)
}

// SelectDiscontinuityGroup selects one group by its stable absolute HLS
// discontinuity sequence identity.
func SelectDiscontinuityGroup(media *MediaPlaylist, id DiscontinuityGroupID) (DiscontinuitySelectionPlan, error) {
	return BuildDiscontinuitySelectionPlan(media, DiscontinuitySelection{GroupID: &id})
}

type groupPartIdentity struct {
	sequence int64
	part     int
}

type groupPhysicalIdentity struct {
	url                     string
	rangeStart, rangeLength int64
}

func canonicalizeGroupSegments(input []Segment) ([]Segment, error) {
	segments := make([]Segment, 0, len(input))
	alive := make([]bool, 0, len(input))
	partialPositions := make(map[int64][]int)
	partialIdentityPositions := make(map[groupPartIdentity]int)
	partialPhysicalIdentities := make(map[groupPartIdentity]groupPhysicalIdentity)
	completePositions := make(map[int64]int)
	completePhysicalIdentities := make(map[int64]groupPhysicalIdentity)

	appendSegment := func(segment Segment) int {
		segments = append(segments, cloneSegment(segment))
		alive = append(alive, true)
		return len(segments) - 1
	}

	for index, segment := range input {
		if segment.Sequence < 0 {
			return nil, fmt.Errorf("segment %d has negative media sequence %d", index, segment.Sequence)
		}
		if segment.Partial {
			if _, complete := completePositions[segment.Sequence]; complete {
				continue
			}
			identity := groupPartIdentity{sequence: segment.Sequence, part: segment.PartIndex}
			physicalIdentity := physicalIdentityOf(segment)
			if position, duplicate := partialIdentityPositions[identity]; duplicate {
				if partialPhysicalIdentities[identity] != physicalIdentity {
					return nil, fmt.Errorf("part sequence %d index %d has conflicting URL or byte range", segment.Sequence, segment.PartIndex)
				}
				if segments[position].Advertisement && !segment.Advertisement {
					segments[position] = cloneSegment(segment)
				}
				continue
			}
			position := appendSegment(segment)
			partialPositions[segment.Sequence] = append(partialPositions[segment.Sequence], position)
			partialIdentityPositions[identity] = position
			partialPhysicalIdentities[identity] = physicalIdentity
			continue
		}

		if position, exists := completePositions[segment.Sequence]; exists {
			if completePhysicalIdentities[segment.Sequence] != physicalIdentityOf(segment) {
				return nil, fmt.Errorf("complete sequence %d has conflicting URL or byte range", segment.Sequence)
			}
			if segments[position].Advertisement && !segment.Advertisement {
				segments[position] = cloneSegment(segment)
			}
			continue
		}

		if positions := partialPositions[segment.Sequence]; len(positions) > 0 {
			first := positions[0]
			segments[first] = cloneSegment(segment)
			completePositions[segment.Sequence] = first
			completePhysicalIdentities[segment.Sequence] = physicalIdentityOf(segment)
			for _, position := range positions[1:] {
				alive[position] = false
			}
			delete(partialPositions, segment.Sequence)
			continue
		}
		completePositions[segment.Sequence] = appendSegment(segment)
		completePhysicalIdentities[segment.Sequence] = physicalIdentityOf(segment)
	}

	result := make([]Segment, 0, len(segments))
	for index, segment := range segments {
		if alive[index] {
			result = append(result, segment)
		}
	}
	return result, nil
}

func physicalIdentityOf(segment Segment) groupPhysicalIdentity {
	return groupPhysicalIdentity{url: segment.URL, rangeStart: segment.RangeStart, rangeLength: segment.RangeLength}
}

func summarizeDiscontinuityGroup(index int, id DiscontinuityGroupID, segments []Segment) (DiscontinuityGroup, error) {
	group := DiscontinuityGroup{
		Index:        index,
		ID:           id,
		Segments:     append([]Segment(nil), segments...),
		SegmentCount: len(segments),
	}
	if len(segments) > 0 {
		group.FirstSequence = segments[0].Sequence
		group.LastSequence = segments[len(segments)-1].Sequence
	}
	var previousMap *Map
	for segmentIndex, segment := range segments {
		if segment.Duration > 0 {
			if group.Duration > time.Duration(math.MaxInt64)-segment.Duration {
				return DiscontinuityGroup{}, errors.New("duration overflows time.Duration")
			}
			group.Duration += segment.Duration
		}
		if segment.Partial {
			group.PartialSegments++
		}
		if segment.Advertisement {
			group.AdvertisementSegments++
		} else {
			group.MediaSegments++
			if segment.Duration > 0 {
				if group.MediaDuration > time.Duration(math.MaxInt64)-segment.Duration {
					return DiscontinuityGroup{}, errors.New("media duration overflows time.Duration")
				}
				group.MediaDuration += segment.Duration
			}
		}

		if segmentIndex == 0 || !sameMap(previousMap, segment.Map) {
			group.MapTransitions = append(group.MapTransitions, MapTransition{
				SegmentIndex: segmentIndex,
				Map:          cloneMap(segment.Map),
			})
		}
		previousMap = segment.Map
	}
	group.Selectable = group.MediaSegments > 0
	return group, nil
}

func sameMap(left, right *Map) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.URL != right.URL || left.RangeStart != right.RangeStart || left.RangeLength != right.RangeLength {
		return false
	}
	if left.Key == nil || right.Key == nil {
		return left.Key == right.Key
	}
	if left.Key.Method != right.Key.Method || left.Key.URL != right.Key.URL || left.Key.Declaration != right.Key.Declaration || left.Key.snapshot != right.Key.snapshot {
		return false
	}
	return string(left.Key.IV) == string(right.Key.IV)
}

func cloneSegment(input Segment) Segment {
	copy := input
	copy.Map = cloneMap(input.Map)
	copy.Key = cloneKey(input.Key)
	return copy
}

func cloneDiscontinuityGroup(input DiscontinuityGroup) DiscontinuityGroup {
	copy := input
	copy.Segments = make([]Segment, len(input.Segments))
	for index, segment := range input.Segments {
		copy.Segments[index] = cloneSegment(segment)
	}
	copy.MapTransitions = make([]MapTransition, len(input.MapTransitions))
	for index, transition := range input.MapTransitions {
		copy.MapTransitions[index] = MapTransition{
			SegmentIndex: transition.SegmentIndex,
			Map:          cloneMap(transition.Map),
		}
	}
	return copy
}
