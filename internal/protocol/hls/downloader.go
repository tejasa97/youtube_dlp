package hls

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

type Config struct {
	PollInterval        time.Duration
	MaxPolls            int
	FragmentConcurrency int
}

type Downloader struct {
	transport Transport
	config    Config
}

func NewDownloader(transport Transport, config Config) *Downloader {
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.MaxPolls <= 0 {
		config.MaxPolls = 120
	}
	return &Downloader{transport: transport, config: config}
}

func (downloader *Downloader) Download(ctx context.Context, manifestURL, outputRoot, destination string, overwrite bool, sink events.Sink) (fragment.Result, error) {
	mediaURL, media, err := downloader.loadMedia(ctx, manifestURL)
	if err != nil {
		return fragment.Result{}, err
	}
	segmentsBySequence := make(map[int64]Segment)
	polls := 0
	for {
		polls++
		for _, segment := range media.Segments {
			segmentsBySequence[segment.Sequence] = segment
		}
		if media.EndList {
			break
		}
		if polls >= downloader.config.MaxPolls {
			return fragment.Result{}, ErrLivePollLimit
		}
		timer := time.NewTimer(downloader.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fragment.Result{}, ctx.Err()
		case <-timer.C:
		}
		body, _, err := downloader.transport.ReadPage(ctx, mediaURL)
		if err != nil {
			return fragment.Result{}, err
		}
		parsed, err := Parse(mediaURL, body)
		if err != nil || parsed.Media == nil {
			return fragment.Result{}, errors.Join(err, ErrInvalidPlaylist)
		}
		media = parsed.Media
	}

	sequences := make([]int64, 0, len(segmentsBySequence))
	for sequence := range segmentsBySequence {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(left, right int) bool { return sequences[left] < sequences[right] })
	keyCache := make(map[string][]byte)
	seenMaps := make(map[string]struct{})
	var plan []fragment.Segment
	for _, sequence := range sequences {
		segment := segmentsBySequence[sequence]
		if segment.Map != nil {
			mapKey := fmt.Sprintf("%s:%d:%d", segment.Map.URL, segment.Map.RangeStart, segment.Map.RangeLength)
			if _, exists := seenMaps[mapKey]; !exists {
				plan = append(plan, fragment.Segment{URL: segment.Map.URL, RangeStart: segment.Map.RangeStart, RangeLength: segment.Map.RangeLength})
				seenMaps[mapKey] = struct{}{}
			}
		}
		planned := fragment.Segment{URL: segment.URL, RangeStart: segment.RangeStart, RangeLength: segment.RangeLength}
		if segment.Key != nil {
			key := keyCache[segment.Key.URL]
			if key == nil {
				body, _, err := downloader.transport.ReadPage(ctx, segment.Key.URL)
				if err != nil {
					return fragment.Result{}, err
				}
				if len(body) != 16 {
					return fragment.Result{}, fmt.Errorf("AES-128 key length = %d, want 16", len(body))
				}
				key = append([]byte(nil), body...)
				keyCache[segment.Key.URL] = key
			}
			iv := append([]byte(nil), segment.Key.IV...)
			if len(iv) == 0 {
				iv = make([]byte, 16)
				binary.BigEndian.PutUint64(iv[8:], uint64(segment.Sequence))
			}
			planned.AES128 = &fragment.AES128{Key: key, IV: iv}
		}
		plan = append(plan, planned)
	}
	return fragment.New(downloader.transport).Download(ctx, fragment.Job{
		Segments: plan, OutputRoot: outputRoot, Destination: destination,
		Concurrency: downloader.config.FragmentConcurrency, Overwrite: overwrite,
	}, sink)
}

func (downloader *Downloader) loadMedia(ctx context.Context, manifestURL string) (string, *MediaPlaylist, error) {
	body, _, err := downloader.transport.ReadPage(ctx, manifestURL)
	if err != nil {
		return "", nil, err
	}
	playlist, err := Parse(manifestURL, body)
	if err != nil {
		return "", nil, err
	}
	if playlist.Media != nil {
		return manifestURL, playlist.Media, nil
	}
	if len(playlist.Variants) == 0 {
		return "", nil, ErrInvalidPlaylist
	}
	selected := playlist.Variants[0]
	for _, variant := range playlist.Variants[1:] {
		if variant.Bandwidth > selected.Bandwidth {
			selected = variant
		}
	}
	body, _, err = downloader.transport.ReadPage(ctx, selected.URL)
	if err != nil {
		return "", nil, err
	}
	playlist, err = Parse(selected.URL, body)
	if err != nil || playlist.Media == nil {
		return "", nil, errors.Join(err, ErrInvalidPlaylist)
	}
	return selected.URL, playlist.Media, nil
}
