package hls

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

type Config struct {
	Headers             http.Header
	PollInterval        time.Duration
	MaxPolls            int
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

func NewDownloader(transport Transport, config Config) *Downloader {
	config.Headers = config.Headers.Clone()
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
	type segmentKey struct {
		sequence int64
		part     int
		partial  bool
	}
	segments := make(map[segmentKey]Segment)
	complete := make(map[int64]bool)
	polls := 0
	for {
		polls++
		for _, segment := range media.Segments {
			if segment.Partial {
				if !complete[segment.Sequence] {
					segments[segmentKey{sequence: segment.Sequence, part: segment.PartIndex, partial: true}] = segment
				}
				continue
			}
			complete[segment.Sequence] = true
			for key := range segments {
				if key.sequence == segment.Sequence && key.partial {
					delete(segments, key)
				}
			}
			segments[segmentKey{sequence: segment.Sequence}] = segment
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
		body, _, err := downloader.readPage(ctx, mediaURL)
		if err != nil {
			return fragment.Result{}, err
		}
		parsed, err := Parse(mediaURL, body)
		if err != nil || parsed.Media == nil {
			return fragment.Result{}, errors.Join(err, ErrInvalidPlaylist)
		}
		media = parsed.Media
	}

	keys := make([]segmentKey, 0, len(segments))
	for key := range segments {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].sequence != keys[right].sequence {
			return keys[left].sequence < keys[right].sequence
		}
		if keys[left].partial != keys[right].partial {
			return !keys[left].partial
		}
		return keys[left].part < keys[right].part
	})
	keyCache := make(map[string][]byte)
	type mapIdentity struct {
		url, keyURL, iv         string
		rangeStart, rangeLength int64
	}
	var lastMap *mapIdentity
	loadEncryption := func(key *Key, sequence int64) (*fragment.AES128, error) {
		if key == nil {
			return nil, nil
		}
		keyBytes := keyCache[key.URL]
		if keyBytes == nil {
			body, _, err := downloader.readPage(ctx, key.URL)
			if err != nil {
				return nil, err
			}
			if len(body) != 16 {
				return nil, fmt.Errorf("AES-128 key length = %d, want 16", len(body))
			}
			keyBytes = append([]byte(nil), body...)
			keyCache[key.URL] = keyBytes
		}
		iv := append([]byte(nil), key.IV...)
		if len(iv) == 0 {
			iv = make([]byte, 16)
			binary.BigEndian.PutUint64(iv[8:], uint64(sequence))
		}
		return &fragment.AES128{Key: keyBytes, IV: iv}, nil
	}
	var plan []fragment.Segment
	for _, key := range keys {
		segment := segments[key]
		if segment.Advertisement {
			continue
		}
		if segment.Map == nil {
			lastMap = nil
		} else {
			var mapIV []byte
			if segment.Map.Key != nil {
				mapIV = segment.Map.Key.IV
			}
			identity := mapIdentity{
				url: segment.Map.URL, rangeStart: segment.Map.RangeStart, rangeLength: segment.Map.RangeLength,
				iv: hex.EncodeToString(mapIV),
			}
			if segment.Map.Key != nil {
				identity.keyURL = segment.Map.Key.URL
			}
			if segment.Discontinuity || lastMap == nil || *lastMap != identity {
				encryption, err := loadEncryption(segment.Map.Key, segment.Sequence)
				if err != nil {
					return fragment.Result{}, err
				}
				plan = append(plan, fragment.Segment{
					URL: segment.Map.URL, RangeStart: segment.Map.RangeStart,
					RangeLength: segment.Map.RangeLength, AES128: encryption,
				})
				identityCopy := identity
				lastMap = &identityCopy
			}
		}
		planned := fragment.Segment{URL: segment.URL, RangeStart: segment.RangeStart, RangeLength: segment.RangeLength}
		planned.AES128, err = loadEncryption(segment.Key, segment.Sequence)
		if err != nil {
			return fragment.Result{}, err
		}
		plan = append(plan, planned)
	}
	return fragment.New(downloader.transport).Download(ctx, fragment.Job{
		Segments: plan, Headers: downloader.config.Headers, OutputRoot: outputRoot, Destination: destination,
		Concurrency: downloader.config.FragmentConcurrency, PerHostConcurrency: downloader.config.PerHostConcurrency,
		MaxSegments: downloader.config.MaxSegments, MaxSegmentSize: downloader.config.MaxSegmentSize,
		Attempts: downloader.config.Attempts, RetryBaseDelay: downloader.config.RetryBaseDelay,
		RetryMaxDelay: downloader.config.RetryMaxDelay, Overwrite: overwrite,
	}, sink)
}

func (downloader *Downloader) loadMedia(ctx context.Context, manifestURL string) (string, *MediaPlaylist, error) {
	body, _, err := downloader.readPage(ctx, manifestURL)
	if err != nil {
		return "", nil, err
	}
	playlist, err := Parse(manifestURL, body)
	if err != nil {
		annotateEncryptionMediaURL(err, manifestURL)
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
	body, _, err = downloader.readPage(ctx, selected.URL)
	if err != nil {
		return "", nil, err
	}
	playlist, err = Parse(selected.URL, body)
	if err != nil || playlist.Media == nil {
		annotateEncryptionMediaURL(err, selected.URL)
		return "", nil, errors.Join(err, ErrInvalidPlaylist)
	}
	return selected.URL, playlist.Media, nil
}

func annotateEncryptionMediaURL(err error, mediaURL string) {
	var encryption *EncryptionError
	if errors.As(err, &encryption) {
		encryption.MediaURL = mediaURL
	}
}

func (downloader *Downloader) readPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if len(downloader.config.Headers) == 0 {
		return downloader.transport.ReadPage(ctx, rawURL)
	}
	return network.ReadPageWithHeaders(ctx, downloader.transport, rawURL, downloader.config.Headers, maxPlaylistBytes)
}
