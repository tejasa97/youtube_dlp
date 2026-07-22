package dash

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

// maxIndexRangeBytes bounds the SIDX index fetch to prevent unbounded reads.
const maxIndexRangeBytes = 16 << 20

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

type Config struct {
	Headers             http.Header
	DynamicPolls        int
	PollInterval        time.Duration
	FragmentConcurrency int
	PerHostConcurrency  int
	MaxSegments         int
	MaxSegmentSize      int64
	Attempts            int
	RetryBaseDelay      time.Duration
	RetryMaxDelay       time.Duration
}

type Downloader struct {
	transport Transport
	config    Config
}

type TrackResult struct {
	Representation  Representation
	Download        fragment.Result
	PeriodDownloads []fragment.Result
}

type Result struct {
	Tracks        []TrackResult
	MergeRequired bool
	MultiPeriod   bool
}

func NewDownloader(transport Transport, config Config) *Downloader {
	config.Headers = config.Headers.Clone()
	if config.DynamicPolls <= 0 {
		config.DynamicPolls = 1
	}
	return &Downloader{transport: transport, config: config}
}

func (downloader *Downloader) Download(ctx context.Context, manifestURL, outputRoot, destination string, overwrite bool, sink events.Sink) (Result, error) {
	mpd, err := downloader.load(ctx, manifestURL)
	if err != nil {
		return Result{}, err
	}
	selected, err := selectRepresentations(mpd)
	if err != nil {
		return Result{}, err
	}
	if len(selected) == 0 {
		return Result{}, fmt.Errorf("%w: no selectable representation", ErrInvalidMPD)
	}

	// Expand any SIDX-based segments before dynamic polling or download while
	// retaining static multi-period boundaries for the media concat stage.
	for index := range selected {
		if len(selected[index].PeriodSegments) != 0 {
			selected[index].Segments = nil
			for periodIndex, segments := range selected[index].PeriodSegments {
				expanded, expandErr := downloader.expandSIDXSegments(ctx, false, segments)
				if expandErr != nil {
					return Result{}, fmt.Errorf("representation %s period %d: %w", selected[index].ID, periodIndex, expandErr)
				}
				selected[index].PeriodSegments[periodIndex] = expanded
				selected[index].Segments = append(selected[index].Segments, expanded...)
			}
			continue
		}
		expanded, expandErr := downloader.expandSIDXSegments(ctx, mpd.Dynamic, selected[index].Segments)
		if expandErr != nil {
			return Result{}, fmt.Errorf("representation %s: %w", selected[index].ID, expandErr)
		}
		selected[index].Segments = expanded
	}

	if mpd.Dynamic && downloader.config.DynamicPolls > 1 {
		byID := make(map[string]*Representation, len(selected))
		for index := range selected {
			byID[representationKey(selected[index])] = &selected[index]
		}
		pollInterval := downloader.config.PollInterval
		if pollInterval <= 0 {
			pollInterval = mpd.MinimumUpdatePeriod
		}
		if pollInterval <= 0 {
			pollInterval = time.Second
		}
		for poll := 1; poll < downloader.config.DynamicPolls; poll++ {
			timer := time.NewTimer(pollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return Result{}, ctx.Err()
			case <-timer.C:
			}
			updated, err := downloader.load(ctx, manifestURL)
			if err != nil {
				return Result{}, err
			}
			for _, representation := range updated.Representations {
				if target := byID[representationKey(representation)]; target != nil {
					target.Segments = mergeSegments(target.Segments, representation.Segments)
				}
			}
			if !updated.Dynamic {
				break
			}
			if downloader.config.PollInterval <= 0 && updated.MinimumUpdatePeriod > 0 {
				pollInterval = updated.MinimumUpdatePeriod
			}
		}
	}

	result := Result{MergeRequired: len(selected) > 1, MultiPeriod: mpd.PeriodCount > 1}
	for _, representation := range selected {
		track := TrackResult{Representation: representation}
		if result.MultiPeriod {
			for periodIndex, segments := range representation.PeriodSegments {
				trackDestination := fmt.Sprintf("%s.%s.period-%04d", destination, trackSuffix(representation), periodIndex)
				downloaded, err := downloader.downloadSegments(ctx, segments, outputRoot, trackDestination, overwrite, sink)
				if err != nil {
					removePeriodDownloads(track.PeriodDownloads)
					for _, completedTrack := range result.Tracks {
						removePeriodDownloads(completedTrack.PeriodDownloads)
					}
					return Result{}, fmt.Errorf("representation %s period %d: %w", representation.ID, periodIndex, err)
				}
				track.PeriodDownloads = append(track.PeriodDownloads, downloaded)
			}
			result.Tracks = append(result.Tracks, track)
			continue
		}
		trackDestination := destination
		if len(selected) > 1 {
			trackDestination += "." + trackSuffix(representation)
		}
		downloaded, err := downloader.downloadSegments(ctx, representation.Segments, outputRoot, trackDestination, overwrite, sink)
		if err != nil {
			return Result{}, fmt.Errorf("representation %s: %w", representation.ID, err)
		}
		track.Download = downloaded
		result.Tracks = append(result.Tracks, track)
	}
	return result, nil
}

func (downloader *Downloader) downloadSegments(ctx context.Context, plan []Segment, outputRoot, destination string, overwrite bool, sink events.Sink) (fragment.Result, error) {
	segments := make([]fragment.Segment, len(plan))
	for index, segment := range plan {
		segments[index] = fragment.Segment{URL: segment.URL, RangeStart: segment.RangeStart, RangeLength: segment.RangeLength}
	}
	return fragment.New(downloader.transport).Download(ctx, fragment.Job{
		Segments: segments, Headers: downloader.config.Headers, OutputRoot: outputRoot, Destination: destination,
		Concurrency: downloader.config.FragmentConcurrency, PerHostConcurrency: downloader.config.PerHostConcurrency,
		MaxSegments: downloader.config.MaxSegments, MaxSegmentSize: downloader.config.MaxSegmentSize,
		Attempts: downloader.config.Attempts, RetryBaseDelay: downloader.config.RetryBaseDelay,
		RetryMaxDelay: downloader.config.RetryMaxDelay, Overwrite: overwrite,
	}, sink)
}

func removePeriodDownloads(downloads []fragment.Result) {
	for _, download := range downloads {
		_ = os.Remove(download.Path)
	}
}

func representationKey(representation Representation) string {
	return trackSuffix(representation) + "\x00" + representation.ID
}

func (downloader *Downloader) load(ctx context.Context, manifestURL string) (MPD, error) {
	var body []byte
	var err error
	if len(downloader.config.Headers) == 0 {
		body, _, err = downloader.transport.ReadPage(ctx, manifestURL)
	} else {
		body, _, err = network.ReadPageWithHeaders(ctx, downloader.transport, manifestURL, downloader.config.Headers, 16<<20)
	}
	if err != nil {
		return MPD{}, err
	}
	return Parse(manifestURL, body)
}

func selectRepresentations(mpd MPD) ([]Representation, error) {
	if mpd.PeriodCount <= 1 {
		return selectBestRepresentations(mpd.Representations), nil
	}
	if mpd.Dynamic {
		return nil, fmt.Errorf("%w: dynamic multi-period manifests", ErrUnsupportedAddressing)
	}

	periods := make([][]Representation, mpd.PeriodCount)
	for _, representation := range mpd.Representations {
		if representation.PeriodIndex < 0 || representation.PeriodIndex >= len(periods) {
			return nil, fmt.Errorf("%w: invalid period index", ErrInvalidMPD)
		}
		periods[representation.PeriodIndex] = append(periods[representation.PeriodIndex], representation)
	}
	for index, period := range periods {
		if len(period) == 0 {
			return nil, fmt.Errorf("%w: period %d has no representations", ErrUnsupportedAddressing, index)
		}
	}

	kinds := make(map[string]struct{})
	for _, period := range periods {
		for _, representation := range period {
			kinds[representationKind(representation)] = struct{}{}
		}
	}
	orderedKinds := make([]string, 0, len(kinds))
	for kind := range kinds {
		orderedKinds = append(orderedKinds, kind)
	}
	sort.Strings(orderedKinds)

	selected := make([]Representation, 0, len(orderedKinds))
	for _, kind := range orderedKinds {
		periodFormats := make([]map[representationSignature]Representation, len(periods))
		for periodIndex, period := range periods {
			periodFormats[periodIndex] = make(map[representationSignature]Representation)
			for _, representation := range period {
				if representationKind(representation) != kind {
					continue
				}
				if !representation.Fragmented {
					continue
				}
				signature := signatureFor(representation)
				if _, exists := periodFormats[periodIndex][signature]; !exists {
					periodFormats[periodIndex][signature] = representation
				}
			}
		}

		var bestSignature representationSignature
		bestFound := false
		for signature := range periodFormats[0] {
			compatible := true
			for periodIndex := 1; periodIndex < len(periodFormats); periodIndex++ {
				if _, exists := periodFormats[periodIndex][signature]; !exists {
					compatible = false
					break
				}
			}
			if compatible && (!bestFound || betterSignature(signature, bestSignature)) {
				bestSignature, bestFound = signature, true
			}
		}
		if !bestFound {
			return nil, fmt.Errorf("%w: no compatible %s representation across %d periods", ErrUnsupportedAddressing, kind, len(periods))
		}

		combined := periodFormats[0][bestSignature]
		combined.Segments = nil
		combined.PeriodSegments = make([][]Segment, len(periodFormats))
		for periodIndex := range periodFormats {
			periodRepresentation := periodFormats[periodIndex][bestSignature]
			if len(combined.Segments) > maxSegmentsPerRepresentation-len(periodRepresentation.Segments) {
				return nil, fmt.Errorf("%w: combined %s segment count exceeds %d", ErrUnsupportedAddressing, kind, maxSegmentsPerRepresentation)
			}
			combined.PeriodSegments[periodIndex] = append([]Segment(nil), periodRepresentation.Segments...)
			combined.Segments = append(combined.Segments, periodRepresentation.Segments...)
		}
		selected = append(selected, combined)
	}
	return selected, nil
}

type representationSignature struct {
	Kind      string
	MimeType  string
	Codecs    string
	Language  string
	FrameRate string
	AudioRate string
	Bandwidth int64
	Width     int
	Height    int
}

func signatureFor(representation Representation) representationSignature {
	return representationSignature{
		Kind: representationKind(representation), MimeType: representation.MimeType,
		Codecs: representation.Codecs, Language: representation.Language,
		FrameRate: representation.FrameRate, AudioRate: representation.AudioRate,
		Bandwidth: representation.Bandwidth,
		Width:     representation.Width, Height: representation.Height,
	}
}

func betterSignature(candidate, current representationSignature) bool {
	if candidate.Bandwidth != current.Bandwidth {
		return candidate.Bandwidth > current.Bandwidth
	}
	if candidate.Height != current.Height {
		return candidate.Height > current.Height
	}
	if candidate.Width != current.Width {
		return candidate.Width > current.Width
	}
	if candidate.Codecs != current.Codecs {
		return candidate.Codecs < current.Codecs
	}
	if candidate.Language != current.Language {
		return candidate.Language < current.Language
	}
	if candidate.FrameRate != current.FrameRate {
		return candidate.FrameRate < current.FrameRate
	}
	if candidate.AudioRate != current.AudioRate {
		return candidate.AudioRate < current.AudioRate
	}
	return candidate.MimeType < current.MimeType
}

func selectBestRepresentations(representations []Representation) []Representation {
	best := make(map[string]Representation)
	for _, representation := range representations {
		kind := representationKind(representation)
		current, exists := best[kind]
		if !exists || representation.Bandwidth > current.Bandwidth {
			best[kind] = representation
		}
	}
	result := make([]Representation, 0, len(best))
	for _, representation := range best {
		result = append(result, representation)
	}
	sort.Slice(result, func(left, right int) bool {
		return trackSuffix(result[left]) < trackSuffix(result[right])
	})
	return result
}

func representationKind(representation Representation) string {
	kind := representation.ContentType
	if kind == "" {
		kind = strings.SplitN(representation.MimeType, "/", 2)[0]
	}
	if kind != "audio" && kind != "video" {
		kind = "media"
	}
	return kind
}

func mergeSegments(existing, updated []Segment) []Segment {
	seen := make(map[string]struct{}, len(existing))
	result := append([]Segment(nil), existing...)
	for _, segment := range existing {
		seen[segmentKey(segment)] = struct{}{}
	}
	for _, segment := range updated {
		if _, exists := seen[segmentKey(segment)]; exists {
			continue
		}
		result = append(result, segment)
		seen[segmentKey(segment)] = struct{}{}
	}
	return result
}

func segmentKey(segment Segment) string {
	return fmt.Sprintf("%s:%d:%d", segment.URL, segment.RangeStart, segment.RangeLength)
}

func trackSuffix(representation Representation) string {
	kind := representation.ContentType
	if kind == "" {
		kind = strings.SplitN(representation.MimeType, "/", 2)[0]
	}
	if kind != "audio" && kind != "video" {
		kind = "media"
	}
	return filepath.Clean(kind)
}

// expandSIDXSegments detects segments that require SIDX expansion (IndexRange
// set), fetches and parses the SIDX box, and returns the expanded segment plan.
// Segments without IndexRange are passed through unchanged.
//
// Dynamic manifests with SegmentBase/SIDX are explicitly rejected because
// stale SIDX data cannot be safely applied to a resource that may have changed
// between polls. This is the smaller provably-correct behavior.
func (downloader *Downloader) expandSIDXSegments(ctx context.Context, dynamic bool, segments []Segment) ([]Segment, error) {
	var result []Segment
	for _, segment := range segments {
		if segment.IndexRange == "" {
			result = append(result, segment)
			continue
		}
		if dynamic {
			return nil, fmt.Errorf("%w: dynamic SegmentBase/SIDX is not supported", ErrUnsupportedAddressing)
		}
		expanded, err := downloader.expandOneSIDX(ctx, segment)
		if err != nil {
			return nil, err
		}
		result = append(result, expanded...)
	}
	return result, nil
}

// expandOneSIDX fetches the index range for a single SIDX marker segment,
// parses the SIDX box, and expands it into concrete byte-range media segments.
func (downloader *Downloader) expandOneSIDX(ctx context.Context, marker Segment) ([]Segment, error) {
	rangeStart, rangeEnd, err := parseByteRange(marker.IndexRange)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedAddressing, err)
	}
	rangeLength := rangeEnd - rangeStart + 1
	if rangeLength > maxIndexRangeBytes {
		return nil, fmt.Errorf("%w: index range %d bytes exceeds limit", ErrUnsupportedAddressing, rangeLength)
	}

	// Fetch the index range bytes.
	indexData, err := downloader.fetchIndexRange(ctx, marker.URL, rangeStart, rangeLength)
	if err != nil {
		return nil, err
	}

	// Parse the SIDX box.
	sidx, sidxOffset, err := ParseSIDX(indexData)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedAddressing, err)
	}

	// The base offset for media range computation is the absolute position of
	// the SIDX box within the media resource: the index range start plus the
	// offset of the sidx box within the fetched data.
	baseOffset := rangeStart + int64(sidxOffset)

	mediaRanges, err := sidx.MediaRanges(baseOffset)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedAddressing, err)
	}
	if len(mediaRanges) > maxSegmentsPerRepresentation {
		return nil, fmt.Errorf("%w: %d segments", ErrUnsupportedAddressing, len(mediaRanges))
	}

	// Build the expanded segment list.
	var result []Segment

	// Prepend initialization segment if specified.
	if marker.InitRange != "" {
		initStart, initEnd, parseErr := parseByteRange(marker.InitRange)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: initialization range: %v", ErrUnsupportedAddressing, parseErr)
		}
		// Reject any overlap between the initialization range and media ranges.
		// Partial trimming could corrupt codec configuration; full omission
		// discards required bytes. Explicit rejection is the safe choice.
		for _, mediaRange := range mediaRanges {
			mediaEnd := mediaRange.Start + mediaRange.Length - 1
			if initStart <= mediaEnd && initEnd >= mediaRange.Start {
				return nil, fmt.Errorf("%w: initialization range %d-%d overlaps media range %d-%d", ErrUnsupportedAddressing, initStart, initEnd, mediaRange.Start, mediaEnd)
			}
		}
		result = append(result, Segment{
			URL:         marker.URL,
			RangeStart:  initStart,
			RangeLength: initEnd - initStart + 1,
			Initialize:  true,
		})
	}

	// Expand media ranges into segments.
	for _, mediaRange := range mediaRanges {
		result = append(result, Segment{
			URL:         marker.URL,
			RangeStart:  mediaRange.Start,
			RangeLength: mediaRange.Length,
		})
	}
	return result, nil
}

// fetchIndexRange performs a bounded HTTP range request for the SIDX index.
// It propagates configured headers and preserves cancellation. It requires a
// 206 Partial Content response, or safely handles a bounded 200 response only
// if the exact requested bytes can be extracted.
func (downloader *Downloader) fetchIndexRange(ctx context.Context, mediaURL string, rangeStart, rangeLength int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create index range request: %w", err)
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, rangeStart+rangeLength-1))
	for key, values := range downloader.config.Headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	response, err := downloader.transport.Do(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("index range request: %w", err)
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusPartialContent:
		// Content-Range is mandatory for 206 responses. A correctly sized body
		// does not prove it came from the requested offset.
		contentRange := response.Header.Get("Content-Range")
		if contentRange == "" {
			return nil, fmt.Errorf("%w: 206 response missing Content-Range header", ErrUnsupportedAddressing)
		}
		if !validContentRange(contentRange, rangeStart, rangeLength) {
			return nil, fmt.Errorf("%w: Content-Range mismatch", ErrUnsupportedAddressing)
		}
	case http.StatusOK:
		// Server ignored the Range header. Only accept if the response is
		// bounded and we can extract the exact bytes.
		if response.ContentLength > maxIndexRangeBytes {
			return nil, fmt.Errorf("%w: 200 response too large for index extraction", ErrUnsupportedAddressing)
		}
	default:
		if network.RetryableStatus(response.StatusCode) {
			return nil, fmt.Errorf("index range request: HTTP status %d", response.StatusCode)
		}
		return nil, fmt.Errorf("%w: index range request returned HTTP %d", ErrUnsupportedAddressing, response.StatusCode)
	}

	// Read with a bounded limit.
	limited := io.LimitReader(response.Body, maxIndexRangeBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read index range response: %w", err)
	}
	if int64(len(body)) > maxIndexRangeBytes {
		return nil, fmt.Errorf("%w: index response exceeds %d bytes", ErrUnsupportedAddressing, maxIndexRangeBytes)
	}

	// For a 200 response, extract the requested byte range.
	// Use subtraction-based bounds checks to avoid overflow when rangeStart
	// is near MaxInt64 (e.g., a hostile indexRange like "9223372036854775807-9223372036854775807").
	if response.StatusCode == http.StatusOK {
		bodyLen := int64(len(body))
		if rangeStart > bodyLen {
			return nil, fmt.Errorf("%w: 200 response too short for requested range", ErrUnsupportedAddressing)
		}
		if rangeLength > bodyLen-rangeStart {
			return nil, fmt.Errorf("%w: 200 response too short for requested range", ErrUnsupportedAddressing)
		}
		body = body[rangeStart : rangeStart+rangeLength]
	}

	// Validate we got the expected amount of data for a 206.
	if response.StatusCode == http.StatusPartialContent && int64(len(body)) != rangeLength {
		return nil, fmt.Errorf("%w: index response length %d != requested %d", ErrUnsupportedAddressing, len(body), rangeLength)
	}

	return body, nil
}

// validContentRange checks that a Content-Range header matches the expected
// byte range. Format: "bytes START-END/TOTAL" or "bytes START-END/*".
// Parsing is strict per RFC 9110 §14.4: first-pos, last-pos, and
// complete-length are 1*DIGIT (ASCII 0-9 only; signs are not allowed).
// The total must be either "*" or a decimal integer strictly greater than END.
func validContentRange(header string, expectedStart, expectedLength int64) bool {
	if !strings.HasPrefix(header, "bytes ") {
		return false
	}
	spec := strings.TrimPrefix(header, "bytes ")
	slashIndex := strings.IndexByte(spec, '/')
	if slashIndex < 0 {
		return false
	}
	rangeSpec := spec[:slashIndex]
	totalSpec := spec[slashIndex+1:]
	dashIndex := strings.IndexByte(rangeSpec, '-')
	if dashIndex < 0 {
		return false
	}
	startStr := rangeSpec[:dashIndex]
	endStr := rangeSpec[dashIndex+1:]
	if !isDigits(startStr) || !isDigits(endStr) {
		return false
	}
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		return false
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		return false
	}
	if start != expectedStart || end != expectedStart+expectedLength-1 {
		return false
	}
	// Validate the total field: must be "*" or a decimal integer > end.
	if totalSpec == "*" {
		return true
	}
	if !isDigits(totalSpec) {
		return false
	}
	total, err := strconv.ParseInt(totalSpec, 10, 64)
	if err != nil {
		return false
	}
	return total > end
}

// isDigits reports whether s is non-empty and contains only ASCII digits 0-9.
// RFC 9110 §14.4 defines first-pos, last-pos, and complete-length as 1*DIGIT;
// strconv.ParseInt accepts an optional leading sign which must be rejected.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
