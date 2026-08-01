package ytdlp

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

var errInvalidRequestOptions = errors.New("invalid request options")

// maxVideoPasswordBytes bounds the public Request.VideoPassword. The value is
// a generous upper bound; extractors that consume the password enforce their
// own narrower limits.
const maxVideoPasswordBytes = 4096

// validateVideoPassword enforces the public video-password invariants. Empty
// is valid; non-empty must be valid UTF-8, at most maxVideoPasswordBytes
// bytes, and must not contain NUL. Other bytes are preserved exactly so
// spaces and punctuation reach the extractor untouched. The password itself
// is never included in the returned error.
func validateVideoPassword(password string) error {
	if password == "" {
		return nil
	}
	if len(password) > maxVideoPasswordBytes {
		return fmt.Errorf("%w: video password too large", errInvalidRequestOptions)
	}
	if strings.ContainsRune(password, 0) {
		return fmt.Errorf("%w: video password contains NUL", errInvalidRequestOptions)
	}
	if !utf8.ValidString(password) {
		return fmt.Errorf("%w: video password not valid UTF-8", errInvalidRequestOptions)
	}
	return nil
}

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
	// MinFilesize and MaxFilesize abort a direct HTTP transfer before it
	// starts when the advertised Content-Length (plus the resume offset)
	// falls outside the bounds, mirroring yt-dlp's downloader/http.py.
	// Zero disables the corresponding bound. They apply only to direct
	// media downloads, never to subtitle or thumbnail payload writes.
	MinFilesize int64
	MaxFilesize int64
	External    *ExternalDownloader
}

// ExternalDownloader explicitly selects a shell-free executable boundary.
// Arguments are passed as an argv vector; interpreter executables are rejected.
type ExternalDownloader struct {
	Executable string
	Arguments  []string
}

// SimpleFilterOptions mirrors yt-dlp's simple video selection filters:
// --match-title/--reject-title, --date/--dateafter/--datebefore,
// --min-views/--max-views, and --age-limit. Zero values disable the
// corresponding filter. MinViews, MaxViews, and AgeLimit are pointers so an
// explicit zero remains observable. These are metadata filters evaluated
// before the generic --match-filters; file size bounds live in
// DownloaderOptions because yt-dlp enforces them in the direct downloader.
type SimpleFilterOptions struct {
	MatchTitle  string
	RejectTitle string
	// Date selects a single upload day and takes precedence over DateAfter
	// and DateBefore (the reference CLI resolves the conflict with a warning).
	Date       string
	DateAfter  string
	DateBefore string
	MinViews   *int64
	MaxViews   *int64
	AgeLimit   *int64
}

// hasSimpleFilters reports whether any simple filter is configured.
func (options SimpleFilterOptions) hasSimpleFilters() bool {
	return options.MatchTitle != "" || options.RejectTitle != "" ||
		options.Date != "" || options.DateAfter != "" || options.DateBefore != "" ||
		options.MinViews != nil || options.MaxViews != nil || options.AgeLimit != nil
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
	WriteInfoJSON bool
	// CleanInfoJSON defaults to true when nil. A non-nil false value preserves
	// the full normalized metadata envelope for --no-clean-info-json.
	CleanInfoJSON    *bool
	WriteDescription bool
	WriteLink        bool
	WriteURLLink     bool
	WriteWeblocLink  bool
	WriteDesktopLink bool
	NoPlaylist       bool
}

// FilesystemOptions controls filename sanitization, partial-download behavior,
// output mtimes, and ffmpeg discovery. Zero values preserve yt-dlp defaults:
// continue and .part files are enabled, mtimes are written, and filenames use
// the standard template sanitization path.
type FilesystemOptions struct {
	RestrictFilenames bool
	WindowsFilenames  bool
	TrimFilenames     int
	NoContinue        bool
	NoPart            bool
	NoMtime           bool
	FfmpegLocation    string
	// OutputNaPlaceholder replaces unavailable fields in filename templates.
	// Empty selects the pinned "NA" default.
	OutputNaPlaceholder string
}

// ThumbnailOptions controls image sidecars. WriteAll takes precedence over
// Write and retains every valid thumbnail; Write keeps only the best one.
type ThumbnailOptions struct {
	Write    bool
	WriteAll bool
	List     bool
	// Embed adds the best downloaded thumbnail to a supported media container.
	// KeepFiles retains the source thumbnail after successful embedding.
	Embed     bool
	KeepFiles bool
	// ConvertFormat accepts jpg, png, webp, none, or ordered mappings such
	// as webp>png/jpg.
	ConvertFormat string
}

// PrintStage identifies a metadata lifecycle point for a print rule.
type PrintStage string

const (
	PrintPreProcess  PrintStage = "pre_process"
	PrintAfterFilter PrintStage = "after_filter"
	PrintVideo       PrintStage = "video"
	PrintBeforeDL    PrintStage = "before_dl"
	PrintPostProcess PrintStage = "post_process"
	PrintAfterMove   PrintStage = "after_move"
	PrintAfterVideo  PrintStage = "after_video"
	PrintPlaylist    PrintStage = "playlist"
)

// PrintRule captures a bounded output template at one lifecycle stage.
type PrintRule struct {
	Stage         PrintStage
	Template      string
	FileTemplate  string
	OmitIfMissing string
}

// PrintOutput is one rendered print rule in deterministic rule order.
type PrintOutput struct {
	Stage PrintStage `json:"stage"`
	Text  string     `json:"text"`
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

// SoundCloudCommentOptions controls opt-in public track-comment retrieval.
// Sort accepts newest, oldest, or track-timestamp. Zero MaxComments selects
// the bounded default.
type SoundCloudCommentOptions struct {
	Enabled     bool
	Sort        string
	MaxComments int
}

// SponsorBlockOptions controls the optional SponsorBlock metadata and
// media-cutting stages. When Enabled is false, both stages are skipped and
// no network requests are issued. When Enabled is true, the configured
// categories are requested from the API and the pinned normalization
// rules produce a deterministic sponsorblock_chapters list on the
// result Info JSON.
//
// APIBase defaults to https://sponsor.ajay.app; an empty value is
// resolved to the default by the implementation. Custom bases are
// only honored for deterministic tests and self-hosted deployments
// that implement the same API.
//
// Categories is treated as caller-owned and is never mutated. An
// empty slice is invalid when enabled. Unknown identifiers, oversized strings, and
// empty enabled category sets are rejected by Request validation.
// NHKOptions controls narrowly scoped extractor behaviour for the NHK
// extractor family. The Radiru area is the only currently supported knob; it
// is the documented product-level equivalent of yt-dlp's
// `nhkradirulive:area` extractor argument. Empty Area defaults to `tokyo` at
// extraction time without touching this struct.
type NHKOptions struct {
	// RadiruArea selects the NHK Radiru Live broadcast area. It must be a
	// short ASCII identifier bounded by validateRequestOptions before any
	// network call. The Go CLI exposes this knob through `--nhk-area`.
	RadiruArea string
}

type SponsorBlockOptions struct {
	Enabled bool
	// Mark overlays fetched SponsorBlock ranges onto ordinary chapters without
	// cutting media. It requires Enabled.
	Mark bool
	// Remove enables FFmpeg-driven cutting of matching SponsorBlock ranges after
	// download (yt-dlp --sponsorblock-remove). It requires Enabled.
	// Simulate and SkipDownload never invent media cuts.
	Remove bool
	// Categories is the fetch set and the default Mark/Remove category source.
	Categories []string
	// RemoveCategories optionally selects which fetched categories to cut.
	// When empty and Remove is true, Categories is used after dropping
	// non-removable poi_highlight/chapter entries (pinned yt-dlp behavior).
	// Explicit non-removable entries are rejected at validation.
	RemoveCategories []string
	// ForceKeyframes re-encodes around cut boundaries before concat
	// (yt-dlp --force-keyframes-at-cuts). It requires Remove.
	ForceKeyframes bool
	APIBase        string
	// ChapterTitle is an optional bounded output template for marked
	// SponsorBlock chapter titles. Nil selects the pinned default; a pointer to
	// an empty string intentionally produces empty titles.
	ChapterTitle *string
}

// PlaylistOptions selects an inclusive, one-based playlist range. Start zero
// means the first entry; End zero or the legacy yt-dlp value -1 means no
// explicit end. A non-empty Items expression takes precedence over Start and
// End. Reverse is applied after selection while playlist_index continues to
// identify the source entry. Flat retains the selected URL-result metadata
// without recursively extracting or downloading child entries.
//
// Random applies a deterministic shuffle after selection and takes precedence
// over Reverse with a warning. Lazy streams selected entries directly from the
// source iterator and ignores both Reverse and Random with distinct warnings.
// ErrorPolicy selects whether ordinary per-entry failures continue or abort.
// Categorized errors that must
// never be swallowed — cancellation, deadlines, resource limits, unsafe
// paths, configuration errors, and global playlist failures — propagate
// under every policy value. MaxFailures bounds the number of
// suppressed failures tolerated during a single playlist iteration
// (zero means unlimited) and corresponds to pinned
// --skip-playlist-after-errors. RandomSource is the deterministic
// injection seam used by Random; nil selects a time-seeded source.
type PlaylistOptions struct {
	Start   int
	End     int
	Reverse bool
	Random  bool
	Lazy    bool
	Items   string
	Flat    bool
	// Disabled, when true, tells the operation to treat a URL that can resolve
	// to either a single video or a playlist as a single video. This is the
	// --no-playlist / --yes-playlist toggle. False means the default heuristic
	// applies. This field is distinct from RelatedFiles.NoPlaylist, which only
	// controls playlist metadata sidecars.
	Disabled     bool
	ErrorPolicy  PlaylistErrorPolicy
	MaxFailures  int
	RandomSource PlaylistRandomSource
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
	RecodeVideo      *RecodeVideoPostprocessor
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

// RecodeVideoPostprocessor mirrors yt-dlp's FFmpegVideoConvertorPP surface:
// the only caller-visible knob is the target container mapping string
// ("mp4", "mkv", "mov>mp4/webm>mp4", ...). Codec selection is left to
// ffmpeg per the pinned stream_copy_opts(False) baseline; only the
// documented AVI exception is hard-coded.
type RecodeVideoPostprocessor struct {
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
	if request.CheckFormats > FormatCheckAll {
		return fmt.Errorf("%w: format availability mode", errInvalidRequestOptions)
	}
	if request.ExtractorRetries < 0 || request.ExtractorRetries > 100 {
		return fmt.Errorf("%w: extractor retry count", errInvalidRequestOptions)
	}
	if request.ForceIPv4 && request.ForceIPv6 || request.SourceAddress != "" && (request.ForceIPv4 || request.ForceIPv6) {
		return fmt.Errorf("%w: conflicting network address policy", errInvalidRequestOptions)
	}
	if err := validateVideoPassword(request.VideoPassword); err != nil {
		return err
	}
	if err := validateOutputTemplates(request); err != nil {
		return fmt.Errorf("%w: %v", errInvalidRequestOptions, err)
	}
	if err := validateOutputPaths(request); err != nil {
		return fmt.Errorf("%w: %v", errInvalidRequestOptions, err)
	}
	if len(request.Filesystem.OutputNaPlaceholder) > 256 || strings.ContainsAny(request.Filesystem.OutputNaPlaceholder, "\x00\r\n") ||
		!utf8.ValidString(request.Filesystem.OutputNaPlaceholder) {
		return fmt.Errorf("%w: output NA placeholder", errInvalidRequestOptions)
	}
	if request.AutonumberStart < 0 || request.AutonumberStart > 1_000_000_000 ||
		request.AutonumberSize < 0 || request.AutonumberSize > 64 || request.AutonumberIndex < 0 || request.AutonumberIndex > maxPlaylistEntries {
		return fmt.Errorf("%w: autonumber options", errInvalidRequestOptions)
	}
	if request.LoadInfoJSON != "" {
		if len(request.LoadInfoJSON) > 4096 || strings.ContainsAny(request.LoadInfoJSON, "\x00\r\n") {
			return fmt.Errorf("%w: load-info-json path", errInvalidRequestOptions)
		}
		if request.CookieFile != "" || request.CookiesFromBrowser != "" || request.UseNetRC || request.NetRCLocation != "" || request.VideoPassword != "" {
			return fmt.Errorf("%w: load-info-json cannot reuse ambient credentials", errInvalidRequestOptions)
		}
	}
	if request.RemoveCacheDir && request.CacheDir == "" {
		return fmt.Errorf("%w: rm-cache-dir requires cache-dir", errInvalidRequestOptions)
	}
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
		options.LiveMaxNoProgressPolls < 0 || options.LiveMaxNoProgressPolls > 10_000 ||
		options.MinFilesize < 0 || options.MaxFilesize < 0 {
		return fmt.Errorf("%w: downloader resource limits", errInvalidRequestOptions)
	}
	if request.MaxDownloads < 0 {
		return fmt.Errorf("%w: max downloads", errInvalidRequestOptions)
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
	if !request.Playlist.ErrorPolicy.Valid() {
		return fmt.Errorf("%w: playlist error policy", errInvalidRequestOptions)
	}
	if request.Playlist.MaxFailures < 0 || request.Playlist.MaxFailures > maxPlaylistEntries {
		return fmt.Errorf("%w: playlist max failures", errInvalidRequestOptions)
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
	if len(request.PrintRules) > 64 {
		return fmt.Errorf("%w: more than 64 print rules", errInvalidRequestOptions)
	}
	for index, rule := range request.PrintRules {
		if !validPrintStage(rule.Stage) || rule.Template == "" ||
			len(rule.FileTemplate) > 64<<10 || strings.ContainsRune(rule.FileTemplate, 0) ||
			len(rule.OmitIfMissing) > 256 || strings.ContainsAny(rule.OmitIfMissing, "\x00\r\n") {
			return fmt.Errorf("%w: print rule %d", errInvalidRequestOptions, index)
		}
	}
	if err := validateSubtitleOptions(request.Subtitles); err != nil {
		return fmt.Errorf("%w: %v", errInvalidRequestOptions, err)
	}
	if _, err := parseThumbnailConversionMapping(request.Thumbnails.ConvertFormat); err != nil {
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
	soundCloudComments := request.SoundCloudComments
	if soundCloudComments.MaxComments < 0 || soundCloudComments.MaxComments > 10_000 ||
		(soundCloudComments.Sort != "" && soundCloudComments.Sort != "newest" &&
			soundCloudComments.Sort != "oldest" && soundCloudComments.Sort != "track-timestamp") {
		return fmt.Errorf("%w: SoundCloud comment options", errInvalidRequestOptions)
	}
	if _, err := ParseMergeOutputFormat(request.MergeOutputFormat); err != nil {
		return fmt.Errorf("%w: merge output format", errInvalidRequestOptions)
	}
	if err := validateSponsorBlockOptions(request.SponsorBlock); err != nil {
		return fmt.Errorf("%w: %v", errInvalidRequestOptions, err)
	}
	if err := validateNHKOptions(request.NHK); err != nil {
		return fmt.Errorf("%w: %v", errInvalidRequestOptions, err)
	}
	if request.Filesystem.TrimFilenames < 0 || request.Filesystem.TrimFilenames > 4096 {
		return fmt.Errorf("%w: trim filenames", errInvalidRequestOptions)
	}
	if request.Filesystem.FfmpegLocation != "" && len(request.Filesystem.FfmpegLocation) > 4096 {
		return fmt.Errorf("%w: ffmpeg location", errInvalidRequestOptions)
	}
	if request.ForceKeyframesAtCuts &&
		len(request.RemoveChapters) == 0 &&
		!(request.SponsorBlock.Enabled && request.SponsorBlock.Remove) {
		return fmt.Errorf("%w: force keyframes requires chapter or SponsorBlock removal", errInvalidRequestOptions)
	}
	for index, postprocessor := range request.Postprocessors {
		if countPostprocessorChoices(postprocessor) != 1 {
			return fmt.Errorf("%w: postprocessors[%d] must select exactly one operation", errInvalidRequestOptions, index)
		}
		if request.KeepVideo && postprocessor.Move != nil {
			return fmt.Errorf("%w: keep-video is incompatible with move postprocessors", errInvalidRequestOptions)
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

// validateSponsorBlockOptions is the public boundary check for the
// SponsorBlock stage. A disabled option is a no-op. Enabled callers must supply a
// bounded set of categories and a syntactically valid API base.
func validateSponsorBlockOptions(options SponsorBlockOptions) error {
	if err := validateSponsorBlockChapterTitle(options.ChapterTitle); err != nil {
		return err
	}
	if !options.Enabled {
		if options.Mark {
			return fmt.Errorf("SponsorBlock marking requires enabled metadata")
		}
		if options.Remove {
			return fmt.Errorf("SponsorBlock remove requires enabled metadata")
		}
		if options.ForceKeyframes {
			return fmt.Errorf("SponsorBlock force keyframes requires remove")
		}
		if len(options.RemoveCategories) != 0 {
			return fmt.Errorf("SponsorBlock remove categories require enabled metadata")
		}
		return nil
	}
	if options.ForceKeyframes && !options.Remove {
		return fmt.Errorf("SponsorBlock force keyframes requires remove")
	}
	if len(options.RemoveCategories) != 0 && !options.Remove {
		return fmt.Errorf("SponsorBlock remove categories require remove")
	}
	if len(options.Categories) == 0 {
		return fmt.Errorf("SponsorBlock categories empty")
	}
	if len(options.Categories) > 64 {
		return fmt.Errorf("too many SponsorBlock categories")
	}
	seen := make(map[string]struct{}, len(options.Categories))
	for index, raw := range options.Categories {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return fmt.Errorf("SponsorBlock category[%d] empty", index)
		}
		if len(trimmed) > 64 {
			return fmt.Errorf("SponsorBlock category[%d] too long", index)
		}
		if !validSponsorBlockCategory(trimmed) {
			return fmt.Errorf("SponsorBlock category[%d] unknown", index)
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
	}
	if len(options.RemoveCategories) > 64 {
		return fmt.Errorf("too many SponsorBlock remove categories")
	}
	removeSeen := make(map[string]struct{}, len(options.RemoveCategories))
	for index, raw := range options.RemoveCategories {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return fmt.Errorf("SponsorBlock remove category[%d] empty", index)
		}
		if len(trimmed) > 64 {
			return fmt.Errorf("SponsorBlock remove category[%d] too long", index)
		}
		if !validSponsorBlockCategory(trimmed) {
			return fmt.Errorf("SponsorBlock remove category[%d] unknown", index)
		}
		if !validSponsorBlockRemoveCategory(trimmed) {
			return fmt.Errorf("SponsorBlock remove category[%d] not removable", index)
		}
		if _, dup := removeSeen[trimmed]; dup {
			continue
		}
		removeSeen[trimmed] = struct{}{}
	}
	if options.APIBase != "" {
		if len(options.APIBase) > 4096 {
			return fmt.Errorf("SponsorBlock API base too long")
		}
		parsed, err := url.Parse(options.APIBase)
		if err != nil {
			return fmt.Errorf("SponsorBlock API base invalid")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("SponsorBlock API base scheme")
		}
		if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("SponsorBlock API base host")
		}
		escaped := strings.ToLower(parsed.EscapedPath())
		if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
			return fmt.Errorf("SponsorBlock API base path")
		}
	}
	return nil
}

func validSponsorBlockCategory(category string) bool {
	switch category {
	case "sponsor", "intro", "outro", "selfpromo", "preview", "filler",
		"interaction", "music_offtopic", "hook", "poi_highlight", "chapter":
		return true
	default:
		return false
	}
}

func validSponsorBlockRemoveCategory(category string) bool {
	switch category {
	case "poi_highlight", "chapter":
		return false
	default:
		return validSponsorBlockCategory(category)
	}
}

// validateNHKOptions enforces the public NHK request invariants before any
// extractor or network call is performed. Empty values are valid (extractors
// fall back to their declared defaults); non-empty values must be bounded
// ASCII identifiers that cannot smuggle hostnames, paths, or fragments into
// later URL construction.
func validateNHKOptions(options NHKOptions) error {
	if options.RadiruArea == "" {
		return nil
	}
	if len(options.RadiruArea) > 32 {
		return fmt.Errorf("NHK Radiru area too long")
	}
	for _, r := range options.RadiruArea {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '_', '-':
			continue
		default:
			return fmt.Errorf("NHK Radiru area contains invalid characters")
		}
	}
	return nil
}
