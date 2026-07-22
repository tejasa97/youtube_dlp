package dash

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
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
	Representation Representation
	Download       fragment.Result
}

type Result struct {
	Tracks        []TrackResult
	MergeRequired bool
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
	selected := selectRepresentations(mpd.Representations)
	if len(selected) == 0 {
		return Result{}, fmt.Errorf("%w: no selectable representation", ErrInvalidMPD)
	}

	// Expand any SIDX-based segments before dynamic polling or download.
	for index := range selected {
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

	result := Result{MergeRequired: len(selected) > 1}
	for _, representation := range selected {
		trackDestination := destination
		if len(selected) > 1 {
			trackDestination += "." + trackSuffix(representation)
		}
		segments := make([]fragment.Segment, len(representation.Segments))
		for index, segment := range representation.Segments {
			segments[index] = fragment.Segment{URL: segment.URL, RangeStart: segment.RangeStart, RangeLength: segment.RangeLength}
		}
		downloaded, err := fragment.New(downloader.transport).Download(ctx, fragment.Job{
			Segments: segments, Headers: downloader.config.Headers, OutputRoot: outputRoot, Destination: trackDestination,
			Concurrency: downloader.config.FragmentConcurrency, PerHostConcurrency: downloader.config.PerHostConcurrency,
			MaxSegments: downloader.config.MaxSegments, MaxSegmentSize: downloader.config.MaxSegmentSize,
			Attempts: downloader.config.Attempts, RetryBaseDelay: downloader.config.RetryBaseDelay,
			RetryMaxDelay: downloader.config.RetryMaxDelay, Overwrite: overwrite,
		}, sink)
		if err != nil {
			return Result{}, fmt.Errorf("representation %s: %w", representation.ID, err)
		}
		result.Tracks = append(result.Tracks, TrackResult{Representation: representation, Download: downloaded})
	}
	return result, nil
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

func selectRepresentations(representations []Representation) []Representation {
	best := make(map[string]Representation)
	for _, representation := range representations {
		kind := representation.ContentType
		if kind == "" {
			kind = strings.SplitN(representation.MimeType, "/", 2)[0]
		}
		if kind != "audio" && kind != "video" {
			kind = "media"
		}
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
		// Avoid duplicating init if it overlaps with the first media range.
		if len(mediaRanges) == 0 || initEnd < mediaRanges[0].Start || initStart > mediaRanges[0].Start+mediaRanges[0].Length-1 {
			result = append(result, Segment{
				URL:         marker.URL,
				RangeStart:  initStart,
				RangeLength: initEnd - initStart + 1,
				Initialize:  true,
			})
		}
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
		// Validate Content-Range if present.
		if contentRange := response.Header.Get("Content-Range"); contentRange != "" {
			if !validContentRange(contentRange, rangeStart, rangeLength) {
				return nil, fmt.Errorf("%w: Content-Range mismatch", ErrUnsupportedAddressing)
			}
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
	if response.StatusCode == http.StatusOK {
		end := rangeStart + rangeLength
		if int64(len(body)) < end {
			return nil, fmt.Errorf("%w: 200 response too short for requested range", ErrUnsupportedAddressing)
		}
		body = body[rangeStart:end]
	}

	// Validate we got the expected amount of data for a 206.
	if response.StatusCode == http.StatusPartialContent && int64(len(body)) != rangeLength {
		return nil, fmt.Errorf("%w: index response length %d != requested %d", ErrUnsupportedAddressing, len(body), rangeLength)
	}

	return body, nil
}

// validContentRange checks that a Content-Range header matches the expected
// byte range. Format: "bytes START-END/TOTAL" or "bytes START-END/*".
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
	dashIndex := strings.IndexByte(rangeSpec, '-')
	if dashIndex < 0 {
		return false
	}
	var start, end int64
	if _, err := fmt.Sscanf(rangeSpec[:dashIndex], "%d", &start); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(rangeSpec[dashIndex+1:], "%d", &end); err != nil {
		return false
	}
	return start == expectedStart && end == expectedStart+expectedLength-1
}
