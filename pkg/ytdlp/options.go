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
	LivePollInterval           time.Duration
	LiveRefreshInterval        time.Duration
	LiveMaxPolls               int
	LiveMaxNoProgressPolls     int
	External                   *ExternalDownloader
}

// ExternalDownloader explicitly selects a shell-free executable boundary.
// Arguments are passed as an argv vector; interpreter executables are rejected.
type ExternalDownloader struct {
	Executable string
	Arguments  []string
}

// SubtitleOptions selects subtitle tracks exposed by an extractor. Embed
// attaches compatible selected tracks to supported media containers; when no
// write mode is selected it implicitly selects manual subtitles. KeepFiles
// retains downloaded sidecars after a successful embed.
type SubtitleOptions struct {
	WriteManual    bool
	WriteAutomatic bool
	Embed          bool
	KeepFiles      bool
	ConvertFormat  string
	Languages      []string
	Format         string
}

// RelatedFileOptions writes metadata files beside the rendered media path.
// Simulate suppresses all related files. SkipDownload does not.
type RelatedFileOptions struct {
	WriteInfoJSON    bool
	WriteDescription bool
	WriteLink        bool
	WriteURLLink     bool
	WriteWeblocLink  bool
	WriteDesktopLink bool
	NoPlaylist       bool
}

// CommentOptions controls opt-in comment metadata retrieval. The initial
// native implementation applies these settings to YouTube videos.
type YouTubeCommentOptions struct {
	Enabled             bool
	Sort                string
	MaxComments         int
	MaxParents          int
	MaxReplies          int
	MaxRepliesPerThread int
	MaxDepth            int
}

// PlaylistOptions selects an inclusive, one-based playlist range. Start zero
// means the first entry; End zero or the legacy yt-dlp value -1 means no
// explicit end. A non-empty Items expression takes precedence over Start and
// End. Reverse is applied after selection while playlist_index continues to
// identify the source entry. Flat retains the selected URL-result metadata
// without recursively extracting or downloading child entries.
type PlaylistOptions struct {
	Start   int
	End     int
	Reverse bool
	Items   string
	Flat    bool
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
		options.MaxSegmentBytes < 0 || options.MaxSegmentBytes > 512<<20 ||
		options.LivePollInterval < 0 || options.LivePollInterval > time.Hour ||
		options.LiveRefreshInterval < 0 || options.LiveRefreshInterval > 24*time.Hour ||
		options.LiveMaxPolls < 0 || options.LiveMaxPolls > 100_000 ||
		options.LiveMaxNoProgressPolls < 0 || options.LiveMaxNoProgressPolls > 10_000 {
		return fmt.Errorf("%w: downloader resource limits", errInvalidRequestOptions)
	}
	playlistStart, playlistEnd := normalizedPlaylistRange(request.Playlist)
	if playlistStart < 1 || playlistStart > maxPlaylistEntries || request.Playlist.End < -1 || playlistEnd > maxPlaylistEntries ||
		(playlistEnd != 0 && playlistEnd < playlistStart) {
		return fmt.Errorf("%w: playlist range", errInvalidRequestOptions)
	}
	if request.Playlist.Items != "" {
		if _, err := parsePlaylistItems(request.Playlist.Items); err != nil {
			return fmt.Errorf("%w: %w", errInvalidRequestOptions, err)
		}
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
	if err := validateSubtitleOptions(request.Subtitles); err != nil {
		return fmt.Errorf("%w: %v", errInvalidRequestOptions, err)
	}
	comments := request.YouTubeComments
	if comments.MaxComments < 0 || comments.MaxComments > 10_000 ||
		comments.MaxParents < 0 || comments.MaxParents > 10_000 ||
		comments.MaxReplies < 0 || comments.MaxReplies > 10_000 ||
		comments.MaxRepliesPerThread < 0 || comments.MaxRepliesPerThread > 10_000 ||
		comments.MaxDepth < 0 || comments.MaxDepth > 8 ||
		(comments.Sort != "" && comments.Sort != "top" && comments.Sort != "new") {
		return fmt.Errorf("%w: comment options", errInvalidRequestOptions)
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

func normalizedPlaylistRange(options PlaylistOptions) (start, end int) {
	start, end = options.Start, options.End
	if start == 0 {
		start = 1
	}
	if end == -1 {
		end = 0
	}
	return start, end
}
