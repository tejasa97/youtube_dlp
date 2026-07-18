package ytdlp

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var errInvalidRequestOptions = errors.New("invalid request options")

// DownloaderOptions controls bounded native transfer behavior. Zero values
// select conservative defaults in the relevant downloader.
type DownloaderOptions struct {
	Attempts                   int
	RetryBaseDelay             time.Duration
	RetryMaxDelay              time.Duration
	RateLimit                  int64
	MaxBytes                   int64
	ThrottleRate               int64
	ThrottleWindow             time.Duration
	ThrottleRestarts           int
	FileAttempts               int
	FragmentConcurrency        int
	PerHostFragmentConcurrency int
	MaxSegments                int
	MaxSegmentBytes            int64
	External                   *ExternalDownloader
}

// ExternalDownloader explicitly selects a shell-free executable boundary.
// Arguments are passed as an argv vector; interpreter executables are rejected.
type ExternalDownloader struct {
	Executable string
	Arguments  []string
}

// Artifact describes a file produced by the requested media pipeline.
type Artifact struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// Postprocessor is a tagged union. Exactly one operation must be non-nil.
type Postprocessor struct {
	ExtractAudio     *ExtractAudioPostprocessor
	Remux            *RemuxPostprocessor
	ConvertSubtitle  *ConvertSubtitlePostprocessor
	ConvertThumbnail *ConvertThumbnailPostprocessor
	EmbedMetadata    *EmbedMetadataPostprocessor
	EmbedChapters    *EmbedChaptersPostprocessor
	EmbedThumbnail   *EmbedThumbnailPostprocessor
	EmbedSubtitle    *EmbedSubtitlePostprocessor
	Fixup            *FixupPostprocessor
	Concat           *ConcatPostprocessor
	Move             *MovePostprocessor
}

type ExtractAudioPostprocessor struct {
	Destination string
	Codec       string
	Bitrate     string
	Quality     int
}

type RemuxPostprocessor struct {
	Destination string
	Format      string
}

type ConvertSubtitlePostprocessor struct {
	Source, Destination, Format string
}

type ConvertThumbnailPostprocessor struct {
	Source, Destination, Format string
}

type EmbedMetadataPostprocessor struct {
	Destination string
	Metadata    map[string]string
}

type Chapter struct {
	Start time.Duration
	End   time.Duration
	Title string
}

type EmbedChaptersPostprocessor struct {
	Destination string
	Chapters    []Chapter
}

type EmbedThumbnailPostprocessor struct{ Source, Destination string }
type EmbedSubtitlePostprocessor struct{ Source, Destination string }
type FixupPostprocessor struct{ Destination, Kind string }
type ConcatPostprocessor struct {
	Sources     []string
	Destination string
}
type MovePostprocessor struct{ Destination string }

func validateRequestOptions(request Request) error {
	options := request.Downloader
	if options.Attempts < 0 || options.Attempts > 100 ||
		options.RetryBaseDelay < 0 || options.RetryMaxDelay < 0 ||
		options.RetryBaseDelay > time.Minute || options.RetryMaxDelay > time.Minute ||
		(options.RetryBaseDelay > 0 && options.RetryMaxDelay > 0 && options.RetryBaseDelay > options.RetryMaxDelay) ||
		options.RateLimit < 0 || options.MaxBytes < 0 || options.MaxBytes > 8<<30 ||
		options.ThrottleRate < 0 || options.ThrottleWindow < 0 || options.ThrottleWindow > time.Minute ||
		options.ThrottleRestarts < 0 || options.ThrottleRestarts > 10 ||
		options.FileAttempts < 0 || options.FileAttempts > 10 ||
		options.FragmentConcurrency < 0 || options.FragmentConcurrency > 128 ||
		options.PerHostFragmentConcurrency < 0 || options.PerHostFragmentConcurrency > 128 ||
		options.MaxSegments < 0 || options.MaxSegments > 10_000 ||
		options.MaxSegmentBytes < 0 || options.MaxSegmentBytes > 512<<20 {
		return fmt.Errorf("%w: downloader resource limits", errInvalidRequestOptions)
	}
	if external := options.External; external != nil {
		if external.Executable == "" || strings.ContainsRune(external.Executable, 0) || len(external.Arguments) > 128 {
			return fmt.Errorf("%w: external downloader", errInvalidRequestOptions)
		}
		total := 0
		for _, argument := range external.Arguments {
			total += len(argument)
			if strings.ContainsRune(argument, 0) || strings.ContainsAny(argument, "\r\n") {
				return fmt.Errorf("%w: external downloader argument", errInvalidRequestOptions)
			}
		}
		if total > 32<<10 {
			return fmt.Errorf("%w: external downloader argument bytes", errInvalidRequestOptions)
		}
	}
	if len(request.Postprocessors) > 64 {
		return fmt.Errorf("%w: more than 64 postprocessors", errInvalidRequestOptions)
	}
	for index, postprocessor := range request.Postprocessors {
		if countPostprocessorChoices(postprocessor) != 1 {
			return fmt.Errorf("%w: postprocessors[%d] must select exactly one operation", errInvalidRequestOptions, index)
		}
	}
	if err := validatePostprocessorPaths(request); err != nil {
		return fmt.Errorf("%w: %v", errInvalidRequestOptions, err)
	}
	return nil
}
