package dash

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

type Config struct {
	DynamicPolls        int
	PollInterval        time.Duration
	FragmentConcurrency int
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
			Segments: segments, OutputRoot: outputRoot, Destination: trackDestination,
			Concurrency: downloader.config.FragmentConcurrency, Overwrite: overwrite,
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
	body, _, err := downloader.transport.ReadPage(ctx, manifestURL)
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
