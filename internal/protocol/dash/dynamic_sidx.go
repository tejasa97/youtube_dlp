package dash

import (
	"context"
	"fmt"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/fragment"
)

type representationProfile struct {
	key        string
	id         string
	signature  representationSignature
	mediaURL   string
	addressing string
}

type sidxSessionState struct {
	indexBytes  int64
	boxesParsed int
	leafCount   int
}

type segmentAccumulator struct {
	segments        []Segment
	initKey         string
	mediaLeaves     []Segment
	window          []Segment
	seenKeys        map[string]struct{}
	uniqueLeafCount int
	maxLeafCount    int
}

func newSegmentAccumulator(maxLeafCount int) *segmentAccumulator {
	return &segmentAccumulator{
		maxLeafCount: maxLeafCount,
		seenKeys:     make(map[string]struct{}),
	}
}

// effectiveDynamicSIDXMediaLeafLimit returns the maximum unique media leaves a
// dynamic SIDX session may accumulate. It is bounded by the configured
// MaxSegments and by the shared fragment engine hard ceiling minus init
// segments that will be downloaded alongside those leaves. configuredMax must
// already be normalized by NewDownloader (zero becomes default); negative values
// are rejected.
func effectiveDynamicSIDXMediaLeafLimit(configuredMax, initCount int) (int, error) {
	if configuredMax < 0 {
		return 0, fmt.Errorf("%w: got %d, limit %d", fragment.ErrTooManySegments, 0, configuredMax)
	}
	limit := configuredMax
	if limit > maxSegmentsPerRepresentation {
		limit = maxSegmentsPerRepresentation
	}
	if initCount < 0 {
		initCount = 0
	}
	if initCount > fragmentHardSegmentCap {
		return 0, nil
	}
	fragmentMediaCap := fragmentHardSegmentCap - initCount
	if limit > fragmentMediaCap {
		limit = fragmentMediaCap
	}
	return limit, nil
}

func validateDynamicSIDXMaxSegmentsConfigured(configuredMax int) error {
	if configuredMax < 0 {
		return fmt.Errorf("%w: got %d, limit %d", fragment.ErrTooManySegments, 0, configuredMax)
	}
	return nil
}

func initSegmentCountFromMarkers(segments []Segment) int {
	for _, segment := range segments {
		if segment.IndexRange != "" && segment.InitRange != "" {
			return 1
		}
	}
	return 0
}

func validateDynamicSIDXOutputBudget(segments []Segment, configuredMax int) error {
	if err := validateDynamicSIDXMaxSegmentsConfigured(configuredMax); err != nil {
		return err
	}
	initCount := len(segments) - mediaSegmentCount(segments)
	mediaCount := mediaSegmentCount(segments)
	limit, err := effectiveDynamicSIDXMediaLeafLimit(configuredMax, initCount)
	if err != nil {
		return err
	}
	if mediaCount > limit {
		return fmt.Errorf("%w: got %d, limit %d", fragment.ErrTooManySegments, mediaCount, limit)
	}
	return nil
}

func (accumulator *segmentAccumulator) merge(expanded []Segment) error {
	initCount := 0
	var init *Segment
	media := make([]Segment, 0, len(expanded))
	for _, segment := range expanded {
		if segment.Initialize {
			initCount++
			candidate := segment
			init = &candidate
			continue
		}
		media = append(media, segment)
	}
	if initCount > 1 {
		return fmt.Errorf("%w: multiple initialization segments in one dynamic snapshot", ErrUnsupportedAddressing)
	}
	if init != nil {
		key := segmentKey(*init)
		if accumulator.initKey == "" {
			accumulator.initKey = key
			accumulator.segments = append([]Segment{*init}, accumulator.segments...)
		} else if accumulator.initKey != key {
			return fmt.Errorf("%w: initialization identity changed during dynamic session", ErrUnsupportedAddressing)
		}
	}
	return accumulator.mergeMediaWindow(media)
}

// mergeMediaWindow accepts append-only growth and bounded live-window prefix
// eviction. Leaf identity is URL + absolute byte range. Each newly observed
// media leaf is appended to the download plan exactly once; eviction removes
// leaves from the live window only and never from the accumulated plan.
func (accumulator *segmentAccumulator) mergeMediaWindow(updated []Segment) error {
	if err := validateLiveWindowLeaves(updated); err != nil {
		return err
	}
	drop, err := alignLiveWindow(accumulator.window, updated)
	if err != nil {
		return err
	}
	retained := len(accumulator.window) - drop
	for index := retained; index < len(updated); index++ {
		segment := updated[index]
		key := segmentKey(segment)
		if _, exists := accumulator.seenKeys[key]; exists {
			return fmt.Errorf("%w: replayed media leaf after live-window evolution", ErrUnsupportedAddressing)
		}
		for _, existing := range accumulator.mediaLeaves {
			if existing.URL != segment.URL {
				continue
			}
			if segmentsOverlap(existing, segment) {
				return fmt.Errorf("%w: overlapping byte-range evolution", ErrUnsupportedAddressing)
			}
		}
		accumulator.mediaLeaves = append(accumulator.mediaLeaves, segment)
		accumulator.segments = append(accumulator.segments, segment)
		accumulator.seenKeys[key] = struct{}{}
		accumulator.uniqueLeafCount++
		if accumulator.maxLeafCount >= 0 && accumulator.uniqueLeafCount > accumulator.maxLeafCount {
			return fmt.Errorf("%w: got %d, limit %d", fragment.ErrTooManySegments, accumulator.uniqueLeafCount, accumulator.maxLeafCount)
		}
	}
	accumulator.window = append([]Segment(nil), updated...)
	return nil
}

// alignLiveWindow finds the smallest prefix-eviction count such that the
// retained suffix of prior is an exact ordered identity prefix of updated.
// After the first non-empty window, at least one stable retained leaf is
// required as a suffix/prefix anchor. Completely disjoint non-empty evolution
// (full window replacement without a retained identity) fails closed.
func alignLiveWindow(prior, updated []Segment) (int, error) {
	if len(prior) == 0 {
		return 0, nil
	}
	for drop := 0; drop < len(prior); drop++ {
		retained := len(prior) - drop
		if retained > len(updated) {
			continue
		}
		matched := true
		for index := 0; index < retained; index++ {
			if segmentKey(prior[drop+index]) != segmentKey(updated[index]) {
				matched = false
				break
			}
		}
		if matched {
			return drop, nil
		}
	}
	return 0, fmt.Errorf("%w: unanchored live-window evolution", ErrUnsupportedAddressing)
}

func validateLiveWindowLeaves(leaves []Segment) error {
	seen := make(map[string]struct{}, len(leaves))
	for index, segment := range leaves {
		key := segmentKey(segment)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate media leaf identity in live window", ErrUnsupportedAddressing)
		}
		seen[key] = struct{}{}
		for prior := 0; prior < index; prior++ {
			if leaves[prior].URL != segment.URL {
				continue
			}
			if segmentsOverlap(leaves[prior], segment) {
				return fmt.Errorf("%w: overlapping byte-range evolution", ErrUnsupportedAddressing)
			}
		}
	}
	return nil
}

func segmentsOverlap(left, right Segment) bool {
	leftEnd, leftOverflow := safeEnd(left.RangeStart, left.RangeLength)
	rightEnd, rightOverflow := safeEnd(right.RangeStart, right.RangeLength)
	if leftOverflow || rightOverflow {
		return true
	}
	return left.RangeStart < rightEnd && right.RangeStart < leftEnd
}

func hasSIDXMarkers(representations []Representation) bool {
	for _, representation := range representations {
		if segmentHasSIDXMarker(representation.Segments) {
			return true
		}
		for _, periodSegments := range representation.PeriodSegments {
			if segmentHasSIDXMarker(periodSegments) {
				return true
			}
		}
	}
	return false
}

func segmentHasSIDXMarker(segments []Segment) bool {
	for _, segment := range segments {
		if segment.IndexRange != "" {
			return true
		}
	}
	return false
}

func hasNonSIDXMediaSegments(segments []Segment) bool {
	for _, segment := range segments {
		if segment.IndexRange != "" || segment.Initialize {
			continue
		}
		return true
	}
	return false
}

func validateHomogeneousDynamicSIDXSelection(representations []Representation) error {
	if len(representations) == 0 {
		return nil
	}
	sidxCount := 0
	for _, representation := range representations {
		hasSIDX := segmentHasSIDXMarker(representation.Segments)
		if hasNonSIDXMediaSegments(representation.Segments) {
			return fmt.Errorf("%w: mixed dynamic SegmentBase/SIDX and other segment addressing in representation %s", ErrUnsupportedAddressing, representation.ID)
		}
		for _, periodSegments := range representation.PeriodSegments {
			if segmentHasSIDXMarker(periodSegments) {
				hasSIDX = true
			}
			if hasNonSIDXMediaSegments(periodSegments) {
				return fmt.Errorf("%w: mixed dynamic SegmentBase/SIDX and other segment addressing in representation %s", ErrUnsupportedAddressing, representation.ID)
			}
		}
		if hasSIDX {
			sidxCount++
		}
	}
	if sidxCount == 0 {
		return fmt.Errorf("%w: dynamic SegmentBase/SIDX selection is empty", ErrUnsupportedAddressing)
	}
	if sidxCount != len(representations) {
		return fmt.Errorf("%w: every selected representation must use SegmentBase indexRange", ErrUnsupportedAddressing)
	}
	return nil
}

func validateRepresentationMarkers(segments []Segment) error {
	var markerURL string
	markerCount := 0
	for _, segment := range segments {
		if segment.IndexRange == "" {
			continue
		}
		markerCount++
		if markerURL == "" {
			markerURL = segment.URL
			continue
		}
		if segment.URL != markerURL {
			return fmt.Errorf("%w: multiple SegmentBase media URLs in one representation", ErrUnsupportedAddressing)
		}
	}
	if markerCount != 1 {
		return fmt.Errorf("%w: expected exactly one SegmentBase index marker", ErrUnsupportedAddressing)
	}
	for _, segment := range segments {
		if segment.Initialize && segment.IndexRange != "" {
			return fmt.Errorf("%w: initialization cannot share a SegmentBase index marker segment", ErrUnsupportedAddressing)
		}
	}
	return nil
}

func mediaURLFromMarkers(segments []Segment) (string, error) {
	if err := validateRepresentationMarkers(segments); err != nil {
		return "", err
	}
	for _, segment := range segments {
		if segment.IndexRange != "" {
			return segment.URL, nil
		}
	}
	return "", fmt.Errorf("%w: missing SegmentBase index marker", ErrUnsupportedAddressing)
}

func profileForRepresentation(representation Representation) (representationProfile, error) {
	if err := validateRepresentationMarkers(representation.Segments); err != nil {
		return representationProfile{}, err
	}
	mediaURL, err := mediaURLFromMarkers(representation.Segments)
	if err != nil {
		return representationProfile{}, err
	}
	return representationProfile{
		key:        representationKey(representation),
		id:         representation.ID,
		signature:  signatureFor(representation),
		mediaURL:   mediaURL,
		addressing: representation.Addressing,
	}, nil
}

func validateRepresentationStable(profile representationProfile, refreshed Representation) error {
	if refreshed.ID != profile.id {
		return fmt.Errorf("%w: representation identity changed", ErrUnsupportedAddressing)
	}
	if signatureFor(refreshed) != profile.signature {
		return fmt.Errorf("%w: representation track properties changed", ErrUnsupportedAddressing)
	}
	if refreshed.Addressing != profile.addressing {
		return fmt.Errorf("%w: representation segment composition changed from %s to %s", ErrUnsupportedAddressing, profile.addressing, refreshed.Addressing)
	}
	if err := validateRepresentationMarkers(refreshed.Segments); err != nil {
		return err
	}
	mediaURL, err := mediaURLFromMarkers(refreshed.Segments)
	if err != nil {
		return err
	}
	if mediaURL != profile.mediaURL {
		return fmt.Errorf("%w: representation media URL changed", ErrUnsupportedAddressing)
	}
	return nil
}

func matchRepresentation(representations []Representation, profile representationProfile) (Representation, error) {
	var matches []Representation
	for _, representation := range representations {
		if representationKey(representation) == profile.key {
			matches = append(matches, representation)
		}
	}
	switch len(matches) {
	case 0:
		return Representation{}, fmt.Errorf("%w: representation %s disappeared", ErrUnsupportedAddressing, profile.id)
	case 1:
		return matches[0], nil
	default:
		return Representation{}, fmt.Errorf("%w: ambiguous representation match for %s", ErrUnsupportedAddressing, profile.id)
	}
}

func (downloader *Downloader) pollDynamicSIDX(ctx context.Context, manifestURL string, initialMPD MPD, selected *[]Representation) error {
	if err := validateHomogeneousDynamicSIDXSelection(*selected); err != nil {
		return err
	}
	if err := validateDynamicSIDXMaxSegmentsConfigured(downloader.config.MaxSegments); err != nil {
		return err
	}
	profiles := make(map[string]representationProfile, len(*selected))
	accumulators := make(map[string]*segmentAccumulator, len(*selected))
	sessions := make(map[string]*sidxSessionState, len(*selected))
	for index := range *selected {
		profile, err := profileForRepresentation((*selected)[index])
		if err != nil {
			return fmt.Errorf("representation %s: %w", (*selected)[index].ID, err)
		}
		if _, exists := profiles[profile.key]; exists {
			return fmt.Errorf("%w: ambiguous representation match for %s", ErrUnsupportedAddressing, profile.id)
		}
		profiles[profile.key] = profile
		initCount := initSegmentCountFromMarkers((*selected)[index].Segments)
		leafLimit, err := effectiveDynamicSIDXMediaLeafLimit(downloader.config.MaxSegments, initCount)
		if err != nil {
			return err
		}
		accumulators[profile.key] = newSegmentAccumulator(leafLimit)
		sessions[profile.key] = &sidxSessionState{}
	}

	processSnapshot := func(representations []Representation) error {
		for key, profile := range profiles {
			representation, err := matchRepresentation(representations, profile)
			if err != nil {
				return err
			}
			if err := validateRepresentationStable(profile, representation); err != nil {
				return err
			}
			expanded, err := downloader.expandSIDXSegmentsWithSession(ctx, representation.Segments, sessions[key])
			if err != nil {
				return fmt.Errorf("representation %s: %w", representation.ID, err)
			}
			if err := accumulators[key].merge(expanded); err != nil {
				return fmt.Errorf("representation %s: %w", representation.ID, err)
			}
		}
		return nil
	}

	if err := processSnapshot(*selected); err != nil {
		return err
	}

	pollInterval, err := dynamicPollInterval(downloader.config.PollInterval, initialMPD.MinimumUpdatePeriod)
	if err != nil {
		return err
	}

	currentMPD := initialMPD
	snapshots := 1
	for snapshots < downloader.config.DynamicPolls && currentMPD.Dynamic {
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		updated, err := downloader.load(ctx, manifestURL)
		if err != nil {
			return err
		}
		if updated.PeriodCount > 1 {
			return fmt.Errorf("%w: dynamic multi-period manifests", ErrUnsupportedAddressing)
		}
		if err := processSnapshot(updated.Representations); err != nil {
			return err
		}
		snapshots++
		currentMPD = updated
		if downloader.config.PollInterval <= 0 {
			pollInterval, err = dynamicPollInterval(0, updated.MinimumUpdatePeriod)
			if err != nil {
				return err
			}
		}
	}

	for index := range *selected {
		key := representationKey((*selected)[index])
		segments := accumulators[key].segments
		if err := validateDynamicSIDXOutputBudget(segments, downloader.config.MaxSegments); err != nil {
			return fmt.Errorf("representation %s: %w", (*selected)[index].ID, err)
		}
		(*selected)[index].Segments = append([]Segment(nil), segments...)
	}
	return nil
}

func (downloader *Downloader) expandSIDXSegmentsWithSession(ctx context.Context, segments []Segment, session *sidxSessionState) ([]Segment, error) {
	var result []Segment
	for _, segment := range segments {
		if segment.IndexRange == "" {
			result = append(result, segment)
			continue
		}
		expanded, err := downloader.expandOneSIDX(ctx, segment, session)
		if err != nil {
			return nil, err
		}
		result = append(result, expanded...)
	}
	return result, nil
}

func (state *sidxExpansionState) remainingIndexBudget() int64 {
	if state.session != nil {
		return maxCumulativeIndexBytes - state.session.indexBytes
	}
	return maxCumulativeIndexBytes - state.indexBytes
}

func (state *sidxExpansionState) recordIndexTransfer(transferred int64) error {
	if state.session != nil {
		state.session.indexBytes += transferred
		if state.session.indexBytes > maxCumulativeIndexBytes {
			return fmt.Errorf("%w: cumulative index transfer budget exhausted (bytes %d exceed limit %d)", ErrUnsupportedAddressing, state.session.indexBytes, maxCumulativeIndexBytes)
		}
		return nil
	}
	state.indexBytes += transferred
	if state.indexBytes > maxCumulativeIndexBytes {
		return fmt.Errorf("%w: cumulative index transfer budget exhausted (bytes %d exceed limit %d)", ErrUnsupportedAddressing, state.indexBytes, maxCumulativeIndexBytes)
	}
	return nil
}

func (state *sidxExpansionState) incrementBoxCount() (int, error) {
	if state.session != nil {
		state.session.boxesParsed++
		if state.session.boxesParsed > maxSIDXBoxesPerRepresentation {
			return state.session.boxesParsed, fmt.Errorf("%w: parsed SIDX box count %d exceeds limit %d", ErrUnsupportedAddressing, state.session.boxesParsed, maxSIDXBoxesPerRepresentation)
		}
		return state.session.boxesParsed, nil
	}
	state.boxesParsed++
	if state.boxesParsed > maxSIDXBoxesPerRepresentation {
		return state.boxesParsed, fmt.Errorf("%w: parsed SIDX box count %d exceeds limit %d", ErrUnsupportedAddressing, state.boxesParsed, maxSIDXBoxesPerRepresentation)
	}
	return state.boxesParsed, nil
}

func (state *sidxExpansionState) incrementLeafCount() int {
	if state.session != nil {
		state.session.leafCount++
		return state.session.leafCount
	}
	state.leafCount++
	return state.leafCount
}
