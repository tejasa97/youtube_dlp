// Package ffmpeg supervises ffmpeg and ffprobe without invoking a shell.
package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

var (
	ErrFFmpegUnavailable  = errors.New("ffmpeg is unavailable")
	ErrFFprobeUnavailable = errors.New("ffprobe is unavailable")
	ErrMediaFailure       = errors.New("ffmpeg media processing failed")
	ErrDestinationExists  = errors.New("postprocessor destination exists")
	ErrInvalidOperation   = errors.New("invalid media operation")
	ErrUnsafeHLSHeaders   = errors.New("HLS headers cannot be passed safely to ffmpeg")
)

var sensitiveDiagnosticPattern = regexp.MustCompile(`(?i)(authorization|password|signature|token|sig|key)=([^&[:space:]]+)`)

type Config struct {
	FFmpegPath  string
	FFprobePath string
	MaxOutput   int
	Environment []string
}

type Toolset struct {
	ffmpeg      string
	ffprobe     string
	maxOutput   int
	environment []string
}

type Version struct {
	FFmpeg  string
	FFprobe string
}

type Probe struct {
	Streams  []Stream       `json:"streams"`
	Format   Format         `json:"format"`
	Chapters []ProbeChapter `json:"chapters,omitempty"`
}

type ProbeChapter struct {
	StartTime string   `json:"start_time"`
	EndTime   string   `json:"end_time"`
	Tags      Metadata `json:"tags,omitempty"`
}

type Stream struct {
	Index       int            `json:"index"`
	CodecName   string         `json:"codec_name"`
	CodecType   string         `json:"codec_type"`
	Width       int            `json:"width,omitempty"`
	Height      int            `json:"height,omitempty"`
	Duration    string         `json:"duration,omitempty"`
	Tags        Metadata       `json:"tags,omitempty"`
	Disposition map[string]int `json:"disposition,omitempty"`
}

type Format struct {
	Filename   string   `json:"filename"`
	FormatName string   `json:"format_name"`
	Duration   string   `json:"duration"`
	Size       string   `json:"size"`
	Tags       Metadata `json:"tags,omitempty"`
}

// Metadata is deliberately a string-only map. It maps directly to ffmpeg's
// metadata model and prevents accidental JSON/shell interpolation at the
// process boundary.
type Metadata map[string]string

type AudioOptions struct {
	Codec   string
	Bitrate string
	Quality int
}

type SubtitleOptions struct {
	Format string
}

// SubtitleInput describes one local subtitle stream to embed. Extension is the
// source subtitle format without a leading dot. Language and Name are optional
// stream metadata.
type SubtitleInput struct {
	Path      string
	Language  string
	Name      string
	Extension string
}

type ImageOptions struct {
	Format string
}

type Chapter struct {
	Start time.Duration
	End   time.Duration
	Title string
}

type Fixup string

const (
	maxMetadataFields = 128
	maxMetadataBytes  = 128 << 10
	maxInfoJSONBytes  = 256 << 10
	maxConcatInputs   = 128
	maxChapters       = 1000
	maxSubtitleInputs = 64
)

const (
	FixupNone     Fixup = "none"
	FixupM4AAudio Fixup = "m4a-audio"
	FixupMPEGTS   Fixup = "mpegts"
	FixupWebVTT   Fixup = "webvtt"
)

func Discover(config Config) (*Toolset, error) {
	tools, err := DiscoverFFmpeg(config)
	if err != nil {
		return nil, err
	}
	ffprobePath, err := discover(config.FFprobePath, "ffprobe", ErrFFprobeUnavailable)
	if err != nil {
		return nil, err
	}
	tools.ffprobe = ffprobePath
	return tools, nil
}

// DiscoverFFmpeg locates only ffmpeg for operations, such as delegated HLS
// download, that do not require probing.
func DiscoverFFmpeg(config Config) (*Toolset, error) {
	ffmpegPath, err := discover(config.FFmpegPath, "ffmpeg", ErrFFmpegUnavailable)
	if err != nil {
		return nil, err
	}
	maxOutput := config.MaxOutput
	if maxOutput <= 0 {
		maxOutput = 1 << 20
	}
	environment := append([]string(nil), config.Environment...)
	if environment == nil {
		environment = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C"}
	}
	return &Toolset{ffmpeg: ffmpegPath, maxOutput: maxOutput, environment: environment}, nil
}

func (tools *Toolset) Versions(ctx context.Context) (Version, error) {
	ffmpegOutput, err := tools.execute(ctx, tools.ffmpeg, []string{"-version"}, nil)
	if err != nil {
		return Version{}, err
	}
	ffprobeOutput, err := tools.execute(ctx, tools.ffprobe, []string{"-version"}, nil)
	if err != nil {
		return Version{}, err
	}
	return Version{FFmpeg: firstLine(ffmpegOutput), FFprobe: firstLine(ffprobeOutput)}, nil
}

func (tools *Toolset) Probe(ctx context.Context, path string) (Probe, error) {
	output, err := tools.execute(ctx, tools.ffprobe, []string{
		"-v", "error", "-show_streams", "-show_format", "-show_chapters", "-of", "json", path,
	}, nil)
	if err != nil {
		return Probe{}, err
	}
	var probe Probe
	if err := json.Unmarshal(output, &probe); err != nil {
		return Probe{}, fmt.Errorf("%w: decode ffprobe JSON: %v", ErrMediaFailure, err)
	}
	return probe, nil
}

func (tools *Toolset) Merge(ctx context.Context, videoPath, audioPath, destination string, overwrite bool, sink events.Sink) error {
	return tools.MergeTracks(ctx, []MergeInput{
		{Path: videoPath, HasVideo: true},
		{Path: audioPath, HasAudio: true},
	}, destination, overwrite, sink)
}

// Remux changes the media container without re-encoding streams. Output is
// atomically finalized and removed on cancellation or ffmpeg failure.
func (tools *Toolset) Remux(ctx context.Context, inputPath, destination string, overwrite bool, sink events.Sink) error {
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return []string{
			"-i", inputPath, "-map", "0", "-c", "copy",
			"-map_metadata", "0", "-progress", "pipe:1", "-nostats", temporary,
		}
	})
}

// ExtractAudio transcodes only the selected audio stream. The operation never
// invokes a shell and the destination is atomically finalized.
func (tools *Toolset) ExtractAudio(ctx context.Context, inputPath, destination string, options AudioOptions, overwrite bool, sink events.Sink) error {
	codec := strings.TrimSpace(options.Codec)
	if !safeCodec(codec) || (options.Bitrate != "" && !safeRate(options.Bitrate)) || options.Quality < 0 || options.Quality > 10 {
		return fmt.Errorf("%w: invalid audio codec, bitrate, or quality", ErrInvalidOperation)
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		args := []string{"-i", inputPath, "-map", "0:a:0?", "-vn", "-c:a", codec}
		if options.Bitrate != "" {
			args = append(args, "-b:a", options.Bitrate)
		}
		if options.Quality != 0 {
			args = append(args, "-q:a", strconv.Itoa(options.Quality))
		}
		return append(args, "-map_metadata", "0", "-progress", "pipe:1", "-nostats", temporary)
	})
}

// ConvertSubtitle converts a text subtitle to the requested ffmpeg muxer.
func (tools *Toolset) ConvertSubtitle(ctx context.Context, inputPath, destination string, options SubtitleOptions, overwrite bool, sink events.Sink) error {
	format := strings.ToLower(strings.TrimSpace(options.Format))
	if !safeSubtitleFormat(format) {
		return fmt.Errorf("%w: unsupported subtitle format %q", ErrInvalidOperation, options.Format)
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return []string{"-i", inputPath, "-map", "0:s:0?", "-c:s", format, "-progress", "pipe:1", "-nostats", temporary}
	})
}

// Recode is the pinned FFmpegVideoConvertorPP surface: callers choose a
// target container mapping, the operation lets ffmpeg pick encoders by
// default, and only the documented AVI exception (-c:v libxvid -vtag XVID)
// adds an explicit codec. Unlike remuxing, stream_copy_opts(False) deliberately
// omits "-c copy", so ffmpeg re-encodes streams using its defaults.
//
// The Format value follows the pinned "A>B/C>D/E" mapping syntax. A
// request whose source already matches its target is a typed no-op, and a
// mapping that contains no rule for the source is also a no-op; both
// return nil and report the skip reason to the caller via ResolveRecodeMapping.
func (tools *Toolset) Recode(ctx context.Context, inputPath, destination, sourceFormat, mapping string, overwrite bool, sink events.Sink) error {
	target, skip, err := ResolveRecodeMapping(sourceFormat, mapping)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOperation, err)
	}
	if skip != "" {
		return nil
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return recodeVideoArgs(inputPath, target, temporary)
	})
}

// recodeVideoArgs mirrors FFmpegVideoConvertorPP._options(target_ext) at
// pinned commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8: -map 0 -dn
// -ignore_unknown (-c copy excluded by stream_copy_opts(False)) with the
// documented AVI special case. The destination is fixed by the caller.
func recodeVideoArgs(inputPath, target, temporary string) []string {
	args := []string{"-i", inputPath, "-map", "0", "-dn", "-ignore_unknown"}
	if target == "avi" {
		args = append(args, "-c:v", "libxvid", "-vtag", "XVID")
	}
	return append(args, "-progress", "pipe:1", "-nostats", temporary)
}

// ResolveRecodeMapping implements the same A>B/C>D/E syntax as the pinned
// yt-dlp resolve_mapping. Two outcomes are no-ops and reported via the
// non-empty skip return value:
//
//   - The resolved target equals the source (e.g. mapping "mkv" on a .mkv
//     input, or "mov>mkv" on a .mp4 input whose mapping rule's source
//     does not match). The skip reason echoes the pinned to_screen text:
//     "already is in target format <ext>".
//   - No rule in the mapping applies to the source. The skip reason
//     echoes the pinned text: "could not find a mapping for <ext>".
//
// The only error path is an unsupported target extension; that is a
// precondition the caller must satisfy before scheduling ffmpeg.
func ResolveRecodeMapping(source, mapping string) (target, skip string, err error) {
	source = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(source), "."))
	mapping = strings.ToLower(strings.TrimSpace(mapping))
	if err := ValidateRecodeMapping(mapping); err != nil {
		return "", "", err
	}
	if source == "" {
		return "", "", fmt.Errorf("source extension is required")
	}
	for _, pair := range strings.Split(mapping, "/") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, ">", 2)
		targetExt := strings.TrimSpace(parts[len(parts)-1])
		if !SupportedRecodeTarget(targetExt) {
			return "", "", fmt.Errorf("unsupported target %q", targetExt)
		}
		if len(parts) == 1 {
			if targetExt == source {
				return source, fmt.Sprintf("already is in target format %s", source), nil
			}
			return targetExt, "", nil
		}
		if strings.TrimSpace(parts[0]) == source {
			if targetExt == source {
				return source, fmt.Sprintf("already is in target format %s", source), nil
			}
			return targetExt, "", nil
		}
	}
	return source, fmt.Sprintf("could not find a mapping for %s", source), nil
}

// ValidateRecodeMapping validates the complete pinned A>B/C>D/E mapping
// before extraction. ResolveRecodeMapping may return at the first matching or
// fallback rule, so validation must inspect every rule independently.
func ValidateRecodeMapping(mapping string) error {
	mapping = strings.ToLower(strings.TrimSpace(mapping))
	if mapping == "" {
		return fmt.Errorf("mapping is required")
	}
	if len(mapping) > 4096 {
		return fmt.Errorf("mapping exceeds byte limit")
	}
	pairs := strings.Split(mapping, "/")
	if len(pairs) > 64 {
		return fmt.Errorf("mapping exceeds rule limit")
	}
	for _, raw := range pairs {
		pair := strings.TrimSpace(raw)
		if pair == "" || strings.Count(pair, ">") > 1 {
			return fmt.Errorf("invalid recode mapping rule")
		}
		parts := strings.SplitN(pair, ">", 2)
		if len(parts) == 2 {
			source := strings.TrimSpace(parts[0])
			if strings.HasPrefix(source, ".") || !SupportedRecodeTarget(source) {
				return fmt.Errorf("unsupported source %q", source)
			}
		}
		target := strings.TrimSpace(parts[len(parts)-1])
		if strings.HasPrefix(target, ".") || !SupportedRecodeTarget(target) {
			return fmt.Errorf("unsupported target %q", target)
		}
	}
	return nil
}

// ConvertImage creates a single still image. It is used for deterministic
// thumbnail conversion rather than relying on a platform image utility.
func (tools *Toolset) ConvertImage(ctx context.Context, inputPath, destination string, options ImageOptions, overwrite bool, sink events.Sink) error {
	format := strings.ToLower(strings.TrimSpace(options.Format))
	if format != "jpg" && format != "png" && format != "webp" {
		return fmt.Errorf("%w: unsupported image format %q", ErrInvalidOperation, options.Format)
	}
	codec := map[string]string{"jpg": "mjpeg", "png": "png", "webp": "libwebp"}[format]
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return []string{"-i", inputPath, "-frames:v", "1", "-c:v", codec, "-progress", "pipe:1", "-nostats", temporary}
	})
}

// EmbedMetadata remuxes media with validated metadata fields.
func (tools *Toolset) EmbedMetadata(ctx context.Context, inputPath, destination string, metadata Metadata, overwrite bool, sink events.Sink) error {
	if err := validateMetadata(metadata); err != nil {
		return err
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		args := []string{"-i", inputPath, "-map", "0", "-c", "copy", "-map_metadata", "0"}
		for _, key := range sortedMetadataKeys(metadata) {
			args = append(args, "-metadata", key+"="+metadata[key])
		}
		return append(args, "-progress", "pipe:1", "-nostats", temporary)
	})
}

// EmbedChapters writes a complete ffmetadata document with fixed millisecond
// timebase and remuxes it into the destination. Chapter titles are escaped at
// the metadata-file boundary and never placed in a command string.
func (tools *Toolset) EmbedChapters(ctx context.Context, inputPath, destination string, chapters []Chapter, overwrite bool, sink events.Sink) error {
	metadataPath, err := writeChapterMetadata(destination, chapters)
	if err != nil {
		return err
	}
	defer os.Remove(metadataPath)
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return []string{"-i", inputPath, "-i", metadataPath, "-map", "0", "-map_metadata", "1", "-c", "copy", "-progress", "pipe:1", "-nostats", temporary}
	})
}

// EmbedMetadataAndChapters writes both products in one atomic ffmpeg
// invocation. This avoids publishing chapter-only media if metadata argument
// validation or ffmpeg execution fails later in the same logical stage.
func (tools *Toolset) EmbedMetadataAndChapters(
	ctx context.Context,
	inputPath, destination string,
	metadata Metadata,
	chapters []Chapter,
	overwrite bool,
	sink events.Sink,
) error {
	if err := validateMetadata(metadata); err != nil {
		return err
	}
	metadataPath, err := writeChapterMetadata(destination, chapters)
	if err != nil {
		return err
	}
	defer os.Remove(metadataPath)
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		args := []string{"-i", inputPath, "-i", metadataPath, "-map", "0", "-map_metadata", "1", "-c", "copy"}
		for _, key := range sortedMetadataKeys(metadata) {
			args = append(args, "-metadata", key+"="+metadata[key])
		}
		return append(args, "-progress", "pipe:1", "-nostats", temporary)
	})
}

// EmbedInfoJSON attaches one sanitized info-json payload to Matroska media.
// Existing application/json attachments are removed first, making repeated
// runs idempotent. The payload is written to a private temporary file and is
// never interpolated into an argv string.
func (tools *Toolset) EmbedInfoJSON(ctx context.Context, inputPath, destination string, payload []byte, overwrite bool, sink events.Sink) error {
	if len(payload) == 0 || len(payload) > maxInfoJSONBytes || !json.Valid(payload) {
		return fmt.Errorf("%w: info-json payload is empty, invalid, or too large", ErrInvalidOperation)
	}
	if err := regularMediaInput(inputPath); err != nil {
		return err
	}
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(destination)), ".")
	if container != "mkv" && container != "mka" {
		return fmt.Errorf("%w: info-json attachment requires mkv or mka", ErrInvalidOperation)
	}
	probe, err := tools.Probe(ctx, inputPath)
	if err != nil {
		return err
	}
	attachmentPath, err := writeInfoJSONAttachment(destination, payload)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(attachmentPath))
	remove := make([]int, 0, 1)
	for _, stream := range probe.Streams {
		if stream.CodecType == "attachment" &&
			(strings.EqualFold(stream.Tags["mimetype"], "application/json") || strings.EqualFold(stream.Tags["filename"], "info.json")) {
			remove = append(remove, stream.Index)
		}
	}
	kept := len(probe.Streams) - len(remove)
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		args := []string{"-i", inputPath, "-map", "0"}
		for _, index := range remove {
			args = append(args, "-map", "-0:"+strconv.Itoa(index))
		}
		args = append(args, "-c", "copy", "-attach", attachmentPath,
			"-metadata:s:"+strconv.Itoa(kept), "mimetype=application/json",
			"-metadata:s:"+strconv.Itoa(kept), "filename=info.json")
		return append(args, "-progress", "pipe:1", "-nostats", temporary)
	})
}

func writeInfoJSONAttachment(destination string, payload []byte) (string, error) {
	directory, err := os.MkdirTemp(filepath.Dir(destination), ".ytdlp-infojson-")
	if err != nil {
		return "", fmt.Errorf("%w: create info-json staging directory: %v", ErrMediaFailure, err)
	}
	path := filepath.Join(directory, "info.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		_ = os.RemoveAll(directory)
		return "", fmt.Errorf("%w: write info-json attachment: %v", ErrMediaFailure, err)
	}
	return path, nil
}

// EmbedThumbnail attaches an image as an attached picture without exposing a
// command-string surface.
func (tools *Toolset) EmbedThumbnail(ctx context.Context, inputPath, imagePath, destination string, overwrite bool, sink events.Sink) error {
	if err := regularMediaInput(inputPath); err != nil {
		return err
	}
	if err := regularMediaInput(imagePath); err != nil {
		return err
	}
	inputInfo, err := os.Stat(inputPath)
	if err != nil {
		return fmt.Errorf("%w: inspect thumbnail media input: %v", ErrInvalidOperation, err)
	}
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(destination)), ".")
	imageExtension := strings.TrimPrefix(strings.ToLower(filepath.Ext(imagePath)), ".")
	if imageExtension == "jpeg" {
		imageExtension = "jpg"
	}
	var args []string
	var cleanupPath string
	defer func() {
		if cleanupPath != "" {
			_ = os.Remove(cleanupPath)
		}
	}()
	switch container {
	case "mp3":
		if imageExtension != "jpg" && imageExtension != "png" {
			return fmt.Errorf("%w: mp3 thumbnail must be jpg or png", ErrInvalidOperation)
		}
		args = []string{
			"-i", inputPath, "-i", imagePath,
			"-map", "0:a:0", "-map", "1:v:0", "-c", "copy",
			"-write_id3v1", "1", "-id3v2_version", "3",
			"-metadata:s:v:0", "title=Album cover",
			"-metadata:s:v:0", "comment=Cover (front)",
		}
	case "mkv", "mka":
		mime := thumbnailMIME(imageExtension)
		if mime == "" {
			return fmt.Errorf("%w: unsupported Matroska thumbnail format", ErrInvalidOperation)
		}
		probe, err := tools.Probe(ctx, inputPath)
		if err != nil {
			return err
		}
		newStreamIndex, removed := thumbnailAttachmentPlan(probe.Streams, mime)
		args = []string{"-i", inputPath, "-map", "0"}
		for _, index := range removed {
			args = append(args, "-map", fmt.Sprintf("-0:%d", index))
		}
		args = append(args,
			"-c", "copy", "-attach", imagePath,
			fmt.Sprintf("-metadata:s:%d", newStreamIndex), "mimetype="+mime,
			fmt.Sprintf("-metadata:s:%d", newStreamIndex), "filename=cover."+imageExtension,
		)
	case "m4a", "mp4", "m4v", "mov":
		if imageExtension != "jpg" && imageExtension != "png" {
			return fmt.Errorf("%w: MP4 thumbnail must be jpg or png", ErrInvalidOperation)
		}
		probe, err := tools.Probe(ctx, inputPath)
		if err != nil {
			return err
		}
		videoStreams, removed := thumbnailVideoPlan(probe.Streams)
		args = []string{"-i", inputPath, "-i", imagePath, "-map", "0"}
		for _, index := range removed {
			args = append(args, "-map", fmt.Sprintf("-0:%d", index))
		}
		args = append(args,
			"-map", "1:v:0", "-c", "copy",
			fmt.Sprintf("-disposition:v:%d", videoStreams), "attached_pic",
		)
	case "flac":
		if imageExtension != "jpg" && imageExtension != "png" {
			return fmt.Errorf("%w: FLAC thumbnail must be jpg or png", ErrInvalidOperation)
		}
		args = []string{
			"-i", inputPath, "-i", imagePath,
			"-map", "0:a:0", "-map", "1:v:0", "-map_metadata", "0", "-c", "copy",
			"-disposition:v:0", "attached_pic",
			"-metadata:s:v:0", "comment=Cover (front)",
		}
	case "ogg", "opus":
		metadataPath, err := tools.writeXiphThumbnailMetadata(ctx, inputPath, imagePath, destination)
		if err != nil {
			return err
		}
		cleanupPath = metadataPath
		args = []string{
			"-i", inputPath, "-f", "ffmetadata", "-i", metadataPath,
			"-map", "0:a:0", "-map_metadata", "1", "-c", "copy",
		}
	default:
		return fmt.Errorf("%w: unsupported thumbnail container %q", ErrInvalidOperation, container)
	}
	return tools.runAtomicPrepared(ctx, destination, overwrite, sink, func(temporary string) error {
		return os.Chtimes(temporary, inputInfo.ModTime(), inputInfo.ModTime())
	}, func(temporary string) []string {
		return append(args, "-progress", "pipe:1", "-nostats", temporary)
	})
}

func thumbnailAttachmentPlan(streams []Stream, mime string) (int, []int) {
	removed := make([]int, 0)
	for _, stream := range streams {
		if thumbnailMatroskaCover(stream, mime) {
			removed = append(removed, stream.Index)
		}
	}
	// -attach appends a new global output stream after every mapped input
	// stream. Negative maps remove matching covers, so the new stream's global
	// index is the retained input stream count, not its attachment ordinal.
	return len(streams) - len(removed), removed
}

func thumbnailMatroskaCover(stream Stream, mime string) bool {
	if !strings.EqualFold(metadataValueFold(stream.Tags, "mimetype"), mime) {
		return false
	}
	if stream.CodecType == "attachment" || stream.Disposition["attached_pic"] == 1 {
		return true
	}
	// ffmpeg may expose a retained Matroska image attachment as a video stream
	// without attached_pic after a cross-MIME remux. Recognize only the
	// cover.<ext> attachment name that this boundary itself emits; never infer
	// cover art from a MIME tag on an audio or ordinary video stream.
	filename := strings.ToLower(metadataValueFold(stream.Tags, "filename"))
	return stream.CodecType == "video" && strings.HasPrefix(filename, "cover.") &&
		strings.EqualFold(thumbnailMIME(strings.TrimPrefix(filepath.Ext(filename), ".")), mime)
}

func metadataValueFold(metadata Metadata, key string) string {
	for candidate, value := range metadata {
		if strings.EqualFold(candidate, key) {
			return value
		}
	}
	return ""
}

func thumbnailVideoPlan(streams []Stream) (int, []int) {
	remaining := 0
	removed := make([]int, 0)
	for _, stream := range streams {
		if stream.CodecType != "video" {
			continue
		}
		if stream.Disposition["attached_pic"] == 1 {
			removed = append(removed, stream.Index)
			continue
		}
		remaining++
	}
	return remaining, removed
}

func thumbnailMIME(extension string) string {
	switch extension {
	case "jpg":
		return "image/jpeg"
	case "png", "webp", "avif", "gif", "heic", "heif":
		return "image/" + extension
	default:
		return ""
	}
}

// EmbedSubtitles is the backwards-compatible single-track embedding entry
// point. New callers should use EmbedSubtitleTracks to attach all selected
// tracks in one deterministic invocation.
func (tools *Toolset) EmbedSubtitles(ctx context.Context, inputPath, subtitlePath, destination string, overwrite bool, sink events.Sink) error {
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(subtitlePath)), ".")
	return tools.EmbedSubtitleTracks(ctx, inputPath, []SubtitleInput{{
		Path:      subtitlePath,
		Extension: extension,
	}}, destination, overwrite, sink)
}

// EmbedSubtitleTracks replaces existing subtitle streams with the supplied
// local tracks while preserving every non-subtitle input stream. The bounded
// input model keeps argv and metadata growth predictable.
func (tools *Toolset) EmbedSubtitleTracks(ctx context.Context, inputPath string, subtitles []SubtitleInput, destination string, overwrite bool, sink events.Sink) error {
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(destination)), ".")
	if !supportedSubtitleContainer(container) {
		return fmt.Errorf("%w: unsupported subtitle container %q", ErrInvalidOperation, container)
	}
	if len(subtitles) == 0 || len(subtitles) > maxSubtitleInputs {
		return fmt.Errorf("%w: subtitle embedding requires 1 to %d tracks", ErrInvalidOperation, maxSubtitleInputs)
	}
	if err := regularMediaInput(inputPath); err != nil {
		return err
	}
	seenPaths := make(map[string]struct{}, len(subtitles))
	normalized := make([]SubtitleInput, len(subtitles))
	for index, subtitle := range subtitles {
		subtitle.Path = filepath.Clean(subtitle.Path)
		subtitle.Extension = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(subtitle.Extension)), ".")
		if err := validateSubtitleInput(subtitle, container); err != nil {
			return fmt.Errorf("%w: subtitle %d: %v", ErrInvalidOperation, index, err)
		}
		if _, exists := seenPaths[subtitle.Path]; exists {
			return fmt.Errorf("%w: subtitle %d repeats an input path", ErrInvalidOperation, index)
		}
		seenPaths[subtitle.Path] = struct{}{}
		normalized[index] = subtitle
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return subtitleEmbedArgs(inputPath, normalized, container, temporary)
	})
}

func subtitleEmbedArgs(inputPath string, subtitles []SubtitleInput, container, temporary string) []string {
	args := []string{"-i", inputPath}
	for _, subtitle := range subtitles {
		args = append(args, "-i", subtitle.Path)
	}
	// Match yt-dlp's stream-copy policy: preserve known non-subtitle streams,
	// replace subtitle streams, and deliberately discard data/unknown streams
	// that ffmpeg cannot safely mux into every supported destination.
	args = append(args, "-map", "0", "-map", "-0:s", "-dn", "-ignore_unknown")
	for index := range subtitles {
		args = append(args, "-map", strconv.Itoa(index+1)+":s:0")
	}
	args = append(args, "-c", "copy")
	if container == "mp4" || container == "mov" || container == "m4a" {
		args = append(args, "-c:s", "mov_text")
	}
	for index, subtitle := range subtitles {
		stream := strconv.Itoa(index)
		if subtitle.Language != "" {
			args = append(args, "-metadata:s:s:"+stream, "language="+subtitle.Language)
		}
		if subtitle.Name != "" {
			args = append(args,
				"-metadata:s:s:"+stream, "handler_name="+subtitle.Name,
				"-metadata:s:s:"+stream, "title="+subtitle.Name,
			)
		}
	}
	return append(args, "-progress", "pipe:1", "-nostats", temporary)
}

func supportedSubtitleContainer(container string) bool {
	switch container {
	case "mp4", "mov", "m4a", "webm", "mkv", "mka":
		return true
	default:
		return false
	}
}

func validateSubtitleInput(input SubtitleInput, container string) error {
	if err := regularMediaInput(input.Path); err != nil {
		return err
	}
	switch input.Extension {
	case "vtt", "srt", "ass", "ssa":
	default:
		return fmt.Errorf("unsupported subtitle format %q", input.Extension)
	}
	if container == "webm" && input.Extension != "vtt" {
		return fmt.Errorf("webm requires vtt subtitles")
	}
	if !safeSubtitleMetadata(input.Language, 64, true) {
		return fmt.Errorf("invalid subtitle language")
	}
	if !safeSubtitleMetadata(input.Name, 1024, false) {
		return fmt.Errorf("invalid subtitle name")
	}
	return nil
}

func regularMediaInput(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return fmt.Errorf("%w: invalid local input path", ErrInvalidOperation)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect local input: %v", ErrInvalidOperation, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: local input is not a regular file", ErrInvalidOperation)
	}
	return nil
}

func safeSubtitleMetadata(value string, maximum int, restricted bool) bool {
	if len(value) > maximum || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if !restricted || value == "" {
		return true
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// DownloadHLS delegates an HLS manifest to ffmpeg through a typed, shell-free
// boundary. Sensitive request headers are rejected because ffmpeg accepts
// headers only through process arguments, which may be visible to other local
// processes owned by the same user.
func (tools *Toolset) DownloadHLS(
	ctx context.Context,
	manifestURL, destination string,
	headers http.Header,
	overwrite bool,
	sink events.Sink,
) error {
	parsed, err := url.Parse(manifestURL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w: invalid HLS manifest URL", ErrInvalidOperation)
	}
	headerBlock, err := ffmpegHLSHeaders(headers)
	if err != nil {
		return err
	}
	err = tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		args := []string{
			"-protocol_whitelist", "http,https,tcp,tls,crypto",
		}
		if headerBlock != "" {
			args = append(args, "-headers", headerBlock)
		}
		args = append(args,
			"-i", manifestURL,
			"-c", "copy",
			"-progress", "pipe:1", "-nostats", temporary,
		)
		return args
	})
	if errors.Is(err, ErrMediaFailure) {
		return fmt.Errorf("%w: delegated HLS download failed", ErrMediaFailure)
	}
	return err
}

func ffmpegHLSHeaders(headers http.Header) (string, error) {
	if len(headers) > 64 {
		return "", fmt.Errorf("%w: too many HLS headers", ErrInvalidOperation)
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var block strings.Builder
	for _, key := range keys {
		canonical := http.CanonicalHeaderKey(key)
		if !safeFFmpegHLSHeader(canonical) {
			return "", ErrUnsafeHLSHeaders
		}
		for _, value := range headers[key] {
			if len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
				return "", ErrUnsafeHLSHeaders
			}
			if (canonical == "Origin" || canonical == "Referer") && !safeHTTPOrigin(value) {
				return "", ErrUnsafeHLSHeaders
			}
			block.WriteString(canonical)
			block.WriteString(": ")
			block.WriteString(value)
			block.WriteString("\r\n")
			if block.Len() > 32<<10 {
				return "", fmt.Errorf("%w: HLS headers exceed size limit", ErrInvalidOperation)
			}
		}
	}
	return block.String(), nil
}

func safeFFmpegHLSHeader(name string) bool {
	switch name {
	case "Accept", "Accept-Language", "Origin", "Referer", "User-Agent":
		return true
	default:
		return false
	}
}

func safeHTTPOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.User == nil && parsed.Hostname() != "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

// ApplyFixup performs compatibility-oriented container adjustments.
func (tools *Toolset) ApplyFixup(ctx context.Context, inputPath, destination string, fixup Fixup, overwrite bool, sink events.Sink) error {
	var adjustment []string
	switch fixup {
	case FixupNone:
		adjustment = nil
	case FixupM4AAudio:
		adjustment = []string{"-bsf:a", "aac_adtstoasc"}
	case FixupMPEGTS:
		adjustment = []string{"-mpegts_flags", "+resend_headers"}
	case FixupWebVTT:
		adjustment = []string{"-c:s", "webvtt"}
	default:
		return fmt.Errorf("%w: unknown fixup %q", ErrInvalidOperation, fixup)
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		args := []string{"-i", inputPath, "-map", "0", "-c", "copy"}
		args = append(args, adjustment...)
		return append(args, "-progress", "pipe:1", "-nostats", temporary)
	})
}

// Concat concatenates an explicit list of compatible media inputs. The list is
// written as an ffconcat file, never interpolated into a shell command.
func (tools *Toolset) Concat(ctx context.Context, inputs []string, destination string, overwrite bool, sink events.Sink) error {
	if len(inputs) == 0 || len(inputs) > maxConcatInputs {
		return fmt.Errorf("%w: concat requires at least one input", ErrInvalidOperation)
	}
	list, err := writeConcatList(destination, inputs)
	if err != nil {
		return err
	}
	defer os.Remove(list)
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return []string{"-f", "concat", "-safe", "0", "-i", list, "-c", "copy", "-progress", "pipe:1", "-nostats", temporary}
	})
}

func (tools *Toolset) runAtomic(ctx context.Context, destination string, overwrite bool, sink events.Sink, operation func(string) []string) error {
	return tools.runAtomicPrepared(ctx, destination, overwrite, sink, nil, operation)
}

func (tools *Toolset) runAtomicPrepared(
	ctx context.Context,
	destination string,
	overwrite bool,
	sink events.Sink,
	prepare func(string) error,
	operation func(string) []string,
) error {
	if sink == nil {
		sink = events.Nop()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: destination is not a regular file", ErrInvalidOperation)
		}
		if !overwrite {
			return fmt.Errorf("%w: %s", ErrDestinationExists, destination)
		}
		if runtime.GOOS == "windows" {
			return fmt.Errorf("%w: atomic overwrite unavailable on Windows", ErrDestinationExists)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect destination: %v", ErrMediaFailure, err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("%w: create output directory: %v", ErrMediaFailure, err)
	}
	temporaryDirectory, err := os.MkdirTemp(filepath.Dir(destination), ".ytdlp-postprocess-")
	if err != nil {
		return fmt.Errorf("%w: allocate private temporary directory: %v", ErrMediaFailure, err)
	}
	defer os.RemoveAll(temporaryDirectory)
	extension := filepath.Ext(destination)
	temporaryFile, err := os.CreateTemp(temporaryDirectory, "output-*"+extension)
	if err != nil {
		return fmt.Errorf("%w: allocate temporary output: %v", ErrMediaFailure, err)
	}
	temporary := temporaryFile.Name()
	// The intermediate file is inside a private same-filesystem directory.
	// Close it before handing its pathname to ffmpeg (notably required on
	// Windows), and always use -y only for this already-isolated intermediate.
	if err := temporaryFile.Close(); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("%w: close temporary output: %v", ErrMediaFailure, err)
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}
	args = append(args, operation(temporary)...)
	if err := sink.Emit(ctx, events.Event{Kind: events.KindPostprocessStarting, Path: destination}); err != nil {
		return err
	}
	var totalSize int64
	_, err = tools.execute(ctx, tools.ffmpeg, args, func(line string) error {
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil
		}
		if key == "total_size" {
			totalSize, _ = strconv.ParseInt(value, 10, 64)
		}
		if key == "progress" {
			return sink.Emit(ctx, events.Event{Kind: events.KindPostprocessProgress, Path: destination, Bytes: totalSize, Message: value})
		}
		return nil
	})
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	// Once the destination is replaced, the operation is committed and a
	// completion observer can no longer safely veto it. Honor cancellation at
	// the last reversible point, then make the terminal notification
	// deliberately non-vetoable.
	if err := ctx.Err(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if prepare != nil {
		if err := prepare(temporary); err != nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("%w: prepare output: %v", ErrMediaFailure, err)
		}
	}
	if err := replace(temporary, destination, overwrite); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("%w: finalize output: %v", ErrMediaFailure, err)
	}
	_ = sink.Emit(ctx, events.Event{Kind: events.KindPostprocessCompleted, Path: destination, Bytes: totalSize})
	return nil
}

func (tools *Toolset) execute(ctx context.Context, binary string, args []string, onLine func(string) error) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append([]string(nil), tools.environment...)
	configureCommand(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := newBoundedBuffer(tools.maxOutput)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: start %s: %v", ErrMediaFailure, filepath.Base(binary), err)
	}
	isolation, err := attachCommand(command)
	if err != nil {
		// Child may still be CREATE_SUSPENDED and not yet in a Job. Kill the
		// direct process; attachCommand already TerminateJobObject'd if assign
		// succeeded and only resume failed.
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
		return nil, fmt.Errorf("%w: attach %s: %v", ErrMediaFailure, filepath.Base(binary), err)
	}
	defer closeCommand(isolation)
	done := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			terminateCommand(command, isolation)
		case <-done:
		}
	}()
	var output bytes.Buffer
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), tools.maxOutput)
	var callbackErr error
	for scanner.Scan() {
		line := scanner.Text()
		if output.Len()+len(line)+1 <= tools.maxOutput {
			output.WriteString(line)
			output.WriteByte('\n')
		}
		if onLine != nil && callbackErr == nil {
			callbackErr = onLine(line)
			if callbackErr != nil {
				terminateCommand(command, isolation)
			}
		}
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	close(done)
	<-watcherDone
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if callbackErr != nil {
		return nil, callbackErr
	}
	if scanErr != nil {
		return nil, fmt.Errorf("%w: read %s output: %v", ErrMediaFailure, filepath.Base(binary), scanErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("%w: %s failed: %v: %s", ErrMediaFailure, filepath.Base(binary), waitErr, redactDiagnostic(strings.TrimSpace(stderr.String())))
	}
	return output.Bytes(), nil
}

func redactDiagnostic(input string) string {
	return sensitiveDiagnosticPattern.ReplaceAllString(input, "$1=[redacted]")
}

func safeCodec(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func safeRate(value string) bool {
	if len(value) < 2 || len(value) > 16 {
		return false
	}
	for index, r := range value {
		if r >= '0' && r <= '9' {
			continue
		}
		return index == len(value)-1 && (r == 'k' || r == 'M')
	}
	return false
}

func safeSubtitleFormat(value string) bool {
	switch value {
	case "srt", "ass", "webvtt", "mov_text":
		return true
	default:
		return false
	}
}

// SupportedRecodeTarget reports whether the requested target extension is
// in the closed allowlist derived from MEDIA_EXTENSIONS at pinned
// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8. The list deliberately excludes
// manifest/storyboard/thumbnail extensions: Recode is for media containers
// only and never for an m3u8/mpd manifest or a jpg/png sidecar.
func SupportedRecodeTarget(target string) bool {
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(target), ".")) {
	case "avi", "flv", "mkv", "mov", "mp4", "webm",
		"aiff", "alac", "flac", "m4a", "mka", "mp3", "ogg", "opus", "wav",
		"aac", "vorbis", "gif":
		return true
	default:
		return false
	}
}

func validateMetadata(metadata Metadata) error {
	if len(metadata) > maxMetadataFields {
		return fmt.Errorf("%w: too many metadata fields", ErrInvalidOperation)
	}
	total := 0
	for key, value := range metadata {
		if key == "" || len(key) > 128 || len(value) > 8192 || strings.ContainsAny(key, "=\r\n\x00") || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%w: unsafe metadata field", ErrInvalidOperation)
		}
		total += len(key) + len(value)
		if total > maxMetadataBytes {
			return fmt.Errorf("%w: metadata exceeds size limit", ErrInvalidOperation)
		}
	}
	return nil
}

func sortedMetadataKeys(metadata Metadata) []string {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeConcatList(destination string, inputs []string) (string, error) {
	if len(inputs) == 0 || len(inputs) > maxConcatInputs {
		return "", fmt.Errorf("%w: invalid concat input count", ErrInvalidOperation)
	}
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("%w: create concat directory: %v", ErrMediaFailure, err)
	}
	file, err := os.CreateTemp(directory, ".ytdlp-concat-*.ffconcat")
	if err != nil {
		return "", fmt.Errorf("%w: create concat list: %v", ErrMediaFailure, err)
	}
	name := file.Name()
	for _, input := range inputs {
		if err := validateLocalRegularFile(input); err != nil {
			file.Close()
			os.Remove(name)
			return "", err
		}
		// ffconcat uses single quotes; quotes are escaped using its documented
		// backslash form. The file is an argument to ffmpeg, not shell text.
		line := "file '" + strings.ReplaceAll(input, "'", "'\\\\''") + "'\n"
		if _, err := io.WriteString(file, line); err != nil {
			file.Close()
			os.Remove(name)
			return "", fmt.Errorf("%w: write concat list: %v", ErrMediaFailure, err)
		}
	}
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("%w: finalize concat list: %v", ErrMediaFailure, err)
	}
	return name, nil
}

func writeChapterMetadata(destination string, chapters []Chapter) (string, error) {
	if len(chapters) == 0 || len(chapters) > maxChapters {
		return "", fmt.Errorf("%w: chapter list is empty", ErrInvalidOperation)
	}
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("%w: create chapter directory: %v", ErrMediaFailure, err)
	}
	file, err := os.CreateTemp(directory, ".ytdlp-chapters-*.ffmetadata")
	if err != nil {
		return "", fmt.Errorf("%w: create chapter metadata: %v", ErrMediaFailure, err)
	}
	name := file.Name()
	var previousEnd time.Duration
	if _, err := io.WriteString(file, ";FFMETADATA1\n"); err != nil {
		file.Close()
		os.Remove(name)
		return "", err
	}
	for _, chapter := range chapters {
		if chapter.Start < 0 || chapter.End <= chapter.Start || chapter.Start < previousEnd || strings.ContainsAny(chapter.Title, "\r\n\x00") || len(chapter.Title) > 8192 {
			file.Close()
			os.Remove(name)
			return "", fmt.Errorf("%w: invalid chapter boundaries or title", ErrInvalidOperation)
		}
		start := chapter.Start.Milliseconds()
		end := chapter.End.Milliseconds()
		text := "[CHAPTER]\nTIMEBASE=1/1000\nSTART=" + strconv.FormatInt(start, 10) + "\nEND=" + strconv.FormatInt(end, 10) + "\ntitle=" + escapeFFMetadata(chapter.Title) + "\n"
		if _, err := io.WriteString(file, text); err != nil {
			file.Close()
			os.Remove(name)
			return "", fmt.Errorf("%w: write chapter metadata: %v", ErrMediaFailure, err)
		}
		previousEnd = chapter.End
	}
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("%w: finalize chapter metadata: %v", ErrMediaFailure, err)
	}
	return name, nil
}

func validateLocalRegularFile(path string) error {
	if path == "" || strings.ContainsAny(path, "\r\n\x00") || strings.Contains(path, "://") {
		return fmt.Errorf("%w: unsafe local input", ErrInvalidOperation)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: local input: %v", ErrInvalidOperation, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: input must be a regular non-symlink file", ErrInvalidOperation)
	}
	return nil
}

func escapeFFMetadata(value string) string {
	return strings.NewReplacer("\\", "\\\\", "=", "\\=", ";", "\\;", "#", "\\#").Replace(value)
}

// ResolveConfiguredLocation resolves an explicit ffmpeg location. Empty location
// leaves discovery to PATH. A directory is searched for ffmpeg and ffprobe; a
// file path selects ffmpeg and probes the sibling ffprobe name.
func ResolveConfiguredLocation(location string) (ffmpegPath, ffprobePath string) {
	if location == "" {
		return "", ""
	}
	info, err := os.Stat(location)
	if err != nil {
		return location, ""
	}
	if info.IsDir() {
		return filepath.Join(location, executableName("ffmpeg")), filepath.Join(location, executableName("ffprobe"))
	}
	return location, filepath.Join(filepath.Dir(location), executableName("ffprobe"))
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func discover(configured, name string, unavailable error) (string, error) {
	if configured != "" {
		info, err := os.Stat(configured)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("%w: %s", unavailable, configured)
		}
		return configured, nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", unavailable
	}
	return path, nil
}

func firstLine(input []byte) string {
	line, _, _ := bytes.Cut(input, []byte{'\n'})
	return string(line)
}

func replace(source, destination string, overwrite bool) error {
	err := os.Rename(source, destination)
	if err == nil || !overwrite {
		return err
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("%w: atomic overwrite unavailable on Windows", ErrDestinationExists)
	}
	if removeErr := os.Remove(destination); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return errors.Join(err, removeErr)
	}
	return os.Rename(source, destination)
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer { return &boundedBuffer{remaining: limit} }

func (buffer *boundedBuffer) Write(input []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(input)
	if len(input) > buffer.remaining {
		input = input[:max(0, buffer.remaining)]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(input)
	buffer.remaining -= len(input)
	return original, nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.truncated {
		return buffer.buffer.String() + " [truncated]"
	}
	return buffer.buffer.String()
}

var _ io.Writer = (*boundedBuffer)(nil)
