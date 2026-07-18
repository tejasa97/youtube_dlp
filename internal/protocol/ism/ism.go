// Package ism parses and downloads Microsoft Smooth Streaming manifests.
package ism

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
)

var (
	ErrInvalidManifest = errors.New("invalid ISM manifest")
	ErrTimelineBound   = errors.New("ISM timeline exceeds segment limit")
)

const (
	maxManifestBytes = 8 << 20
	maxStreams       = 32
	maxQualities     = 64
	maxChunks        = 100000
	maxSegments      = 100000
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}
type Config struct{ FragmentConcurrency, PerHostConcurrency, MaxSegments int }
type Manifest struct {
	Timescale int64
	Duration  int64
	Streams   []Stream
}
type Stream struct {
	Type      string
	URL       string
	Qualities []Quality
	Chunks    []Chunk
}
type Quality struct {
	Bitrate int64
	FourCC  string
}
type Chunk struct {
	Time, Duration int64
	Repeat         int
}
type Segment struct {
	URL  string
	Time int64
}

type Downloader struct {
	transport Transport
	config    Config
}

func NewDownloader(transport Transport, config Config) *Downloader {
	if config.MaxSegments <= 0 {
		config.MaxSegments = 10000
	}
	if config.MaxSegments > maxSegments {
		config.MaxSegments = maxSegments
	}
	if config.PerHostConcurrency > 128 {
		config.PerHostConcurrency = 128
	}
	return &Downloader{transport: transport, config: config}
}

type TrackResult struct {
	Stream   Stream
	Download fragment.Result
}
type Result struct {
	Tracks        []TrackResult
	MergeRequired bool
}

func Parse(manifestURL string, body []byte) (Manifest, error) {
	if len(body) == 0 || len(body) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest size", ErrInvalidManifest)
	}
	var source xmlManifest
	if err := xml.Unmarshal(body, &source); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if source.Timescale <= 0 || source.Duration <= 0 || len(source.Streams) == 0 || len(source.Streams) > maxStreams {
		return Manifest{}, fmt.Errorf("%w: no stream indexes", ErrInvalidManifest)
	}
	result := Manifest{Timescale: source.Timescale, Duration: source.Duration}
	for _, stream := range source.Streams {
		streamType := strings.ToLower(stream.Type)
		if (streamType != "audio" && streamType != "video" && streamType != "text") || stream.URL == "" || len(stream.Qualities) == 0 || len(stream.Qualities) > maxQualities || len(stream.Chunks) == 0 || len(stream.Chunks) > maxChunks {
			return Manifest{}, fmt.Errorf("%w: incomplete stream index", ErrInvalidManifest)
		}
		if unknownPlaceholder(stream.URL) {
			return Manifest{}, fmt.Errorf("%w: unknown URL placeholder", ErrInvalidManifest)
		}
		chunks := make([]Chunk, 0, len(stream.Chunks))
		current := int64(0)
		for index, chunk := range stream.Chunks {
			if chunk.Time != nil {
				current = *chunk.Time
			}
			if current < 0 || chunk.Duration <= 0 || chunk.Repeat < -1 {
				return Manifest{}, fmt.Errorf("%w: non-positive chunk duration", ErrInvalidManifest)
			}
			chunks = append(chunks, Chunk{Time: current, Duration: chunk.Duration, Repeat: chunk.Repeat})
			if chunk.Repeat < 0 {
				if index+1 < len(stream.Chunks) && stream.Chunks[index+1].Time == nil {
					return Manifest{}, fmt.Errorf("%w: implicit time after unbounded repeat", ErrInvalidManifest)
				}
				continue
			}
			next, ok := addDuration(current, chunk.Duration, int64(chunk.Repeat+1))
			if !ok {
				return Manifest{}, fmt.Errorf("%w: timeline overflow", ErrInvalidManifest)
			}
			current = next
		}
		qualities := make([]Quality, len(stream.Qualities))
		for index, quality := range stream.Qualities {
			if quality.Bitrate <= 0 {
				return Manifest{}, fmt.Errorf("%w: non-positive bitrate", ErrInvalidManifest)
			}
			qualities[index] = Quality{Bitrate: quality.Bitrate, FourCC: quality.FourCC}
		}
		result.Streams = append(result.Streams, Stream{Type: streamType, URL: stream.URL, Qualities: qualities, Chunks: chunks})
	}
	return result, nil
}

func (downloader *Downloader) Download(ctx context.Context, manifestURL, outputRoot, destination string, overwrite bool, sink events.Sink) (Result, error) {
	body, _, err := downloader.transport.ReadPage(ctx, manifestURL)
	if err != nil {
		return Result{}, err
	}
	manifest, err := Parse(manifestURL, body)
	if err != nil {
		return Result{}, err
	}
	selected := selectStreams(manifest.Streams)
	result := Result{MergeRequired: len(selected) > 1}
	for _, stream := range selected {
		segments, err := Address(manifestURL, manifest, stream, downloader.config.MaxSegments)
		if err != nil {
			return Result{}, err
		}
		plan := make([]fragment.Segment, len(segments))
		for index, segment := range segments {
			plan[index] = fragment.Segment{URL: segment.URL}
		}
		trackDestination := destination
		if len(selected) > 1 {
			trackDestination += "." + filepath.Clean(stream.Type)
		}
		downloaded, err := fragment.New(downloader.transport).Download(ctx, fragment.Job{Segments: plan, OutputRoot: outputRoot, Destination: trackDestination, Concurrency: downloader.config.FragmentConcurrency, PerHostConcurrency: downloader.config.PerHostConcurrency, MaxSegments: downloader.config.MaxSegments, Overwrite: overwrite}, sink)
		if err != nil {
			return Result{}, fmt.Errorf("ISM %s: %w", stream.Type, err)
		}
		result.Tracks = append(result.Tracks, TrackResult{Stream: stream, Download: downloaded})
	}
	return result, nil
}

func Address(manifestURL string, manifest Manifest, stream Stream, limit int) ([]Segment, error) {
	if limit <= 0 {
		limit = 10000
	}
	if limit > maxSegments {
		limit = maxSegments
	}
	if len(stream.Qualities) == 0 || unknownPlaceholder(stream.URL) {
		return nil, ErrInvalidManifest
	}
	quality := stream.Qualities[0]
	for _, candidate := range stream.Qualities[1:] {
		if candidate.Bitrate > quality.Bitrate {
			quality = candidate
		}
	}
	base, err := url.Parse(manifestURL)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest URL", ErrInvalidManifest)
	}
	var result []Segment
	for chunkIndex, chunk := range stream.Chunks {
		repeat := chunk.Repeat
		if chunk.Duration <= 0 || chunk.Time < 0 || repeat < -1 {
			return nil, ErrInvalidManifest
		}
		if repeat < 0 { // Smooth manifests use r=-1 through the next explicit t or presentation duration.
			end := manifest.Duration
			if chunkIndex+1 < len(stream.Chunks) {
				end = stream.Chunks[chunkIndex+1].Time
			}
			if end <= chunk.Time {
				return nil, fmt.Errorf("%w: unbounded repeat", ErrInvalidManifest)
			}
			count := (end - chunk.Time) / chunk.Duration
			if count <= 0 || count > int64(limit-len(result)) {
				return nil, ErrTimelineBound
			}
			repeat = int(count - 1)
		}
		if repeat < 0 || repeat >= limit-len(result) {
			return nil, ErrTimelineBound
		}
		for iteration := 0; iteration <= repeat; iteration++ {
			time, ok := addDuration(chunk.Time, chunk.Duration, int64(iteration))
			if !ok {
				return nil, ErrInvalidManifest
			}
			reference := strings.NewReplacer("{bitrate}", strconv.FormatInt(quality.Bitrate, 10), "{Bitrate}", strconv.FormatInt(quality.Bitrate, 10), "{start time}", strconv.FormatInt(time, 10), "{start_time}", strconv.FormatInt(time, 10)).Replace(stream.URL)
			resolved, resolveErr := base.Parse(reference)
			if resolveErr != nil {
				return nil, fmt.Errorf("%w: segment URL", ErrInvalidManifest)
			}
			result = append(result, Segment{URL: resolved.String(), Time: time})
		}
	}
	return result, nil
}
func unknownPlaceholder(template string) bool {
	clean := strings.NewReplacer("{bitrate}", "", "{Bitrate}", "", "{start time}", "", "{start_time}", "").Replace(template)
	return strings.ContainsAny(clean, "{}")
}
func addDuration(start, duration, count int64) (int64, bool) {
	if count < 0 || duration <= 0 || count > (math.MaxInt64-start)/duration {
		return 0, false
	}
	return start + duration*count, true
}
func selectStreams(streams []Stream) []Stream {
	best := map[string]Stream{}
	for _, stream := range streams {
		current, ok := best[stream.Type]
		if !ok || maxBitrate(stream) > maxBitrate(current) {
			best[stream.Type] = stream
		}
	}
	result := make([]Stream, 0, len(best))
	for _, stream := range best {
		result = append(result, stream)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Type < result[j].Type })
	return result
}
func maxBitrate(stream Stream) int64 {
	var max int64
	for _, quality := range stream.Qualities {
		if quality.Bitrate > max {
			max = quality.Bitrate
		}
	}
	return max
}

type xmlManifest struct {
	XMLName   xml.Name    `xml:"SmoothStreamingMedia"`
	Timescale int64       `xml:"TimeScale,attr"`
	Duration  int64       `xml:"Duration,attr"`
	Streams   []xmlStream `xml:"StreamIndex"`
}
type xmlStream struct {
	Type      string       `xml:"Type,attr"`
	URL       string       `xml:"Url,attr"`
	Qualities []xmlQuality `xml:"QualityLevel"`
	Chunks    []xmlChunk   `xml:"c"`
}
type xmlQuality struct {
	Bitrate int64  `xml:"Bitrate,attr"`
	FourCC  string `xml:"FourCC,attr"`
}
type xmlChunk struct {
	Time     *int64 `xml:"t,attr"`
	Duration int64  `xml:"d,attr"`
	Repeat   int    `xml:"r,attr"`
}
