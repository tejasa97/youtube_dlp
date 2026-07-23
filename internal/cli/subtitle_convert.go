package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

const maxSubtitleConversions = 128
const maxSubtitleInfoJSONBytes = 8 << 20

// parseSubtitleConvertFormat deliberately exposes the bounded subset of the
// ffmpeg toolset that has unambiguous sidecar extensions.  mov_text is a codec
// normally carried in a media container, so it is not a sidecar target here.
func parseSubtitleConvertFormat(value string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(value))
	switch format {
	case "", "none":
		return "", nil
	case "srt", "ass":
		return format, nil
	case "vtt":
		return "webvtt", nil
	default:
		return "", &ytdlp.Error{Category: ytdlp.ErrorInvalidInput, Op: "convert subtitles", Err: fmt.Errorf("unsupported subtitle format %q (supported: srt, ass, vtt, none)", value)}
	}
}

func convertResultSubtitles(ctx context.Context, result *ytdlp.Result, outputRoot, requestedFormat string, overwrite bool) error {
	format, err := parseSubtitleConvertFormat(requestedFormat)
	if err != nil || format == "" {
		return err
	}
	root, err := filepath.Abs(outputRoot)
	if err != nil {
		return subtitleConvertError(ytdlp.ErrorInvalidInput, "resolve output root", err)
	}
	var tools *ffmpeg.Toolset
	converted := 0
	var walk func(*ytdlp.Result) error
	walk = func(current *ytdlp.Result) error {
		if current == nil {
			return nil
		}
		for index := range current.Artifacts {
			if err := ctx.Err(); err != nil {
				return subtitleConvertError(ytdlp.ErrorCancelled, "convert subtitles", err)
			}
			artifact := &current.Artifacts[index]
			if artifact.Kind != "subtitle" {
				continue
			}
			converted++
			if converted > maxSubtitleConversions {
				return subtitleConvertError(ytdlp.ErrorInvalidInput, "convert subtitles", fmt.Errorf("more than %d subtitle artifacts", maxSubtitleConversions))
			}
			source, destination, resultPath, same, planErr := subtitleConversionPlan(root, artifact.Path, format)
			if planErr != nil {
				return planErr
			}
			if same {
				continue
			}
			metadata, metadataErr := planSubtitleInfoJSON(current.InfoJSON, root, artifact.Path, source, destination, resultPath, format)
			if metadataErr != nil {
				return metadataErr
			}
			if tools == nil {
				tools, err = ffmpeg.Discover(ffmpeg.Config{})
				if err != nil {
					return subtitleConvertError(ytdlp.ErrorUnsupported, "convert subtitles", err)
				}
			}
			if err := tools.ConvertSubtitle(ctx, source, destination, ffmpeg.SubtitleOptions{Format: format}, overwrite, nil); err != nil {
				category := ytdlp.ErrorInternal
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					category = ytdlp.ErrorCancelled
				} else if errors.Is(err, ffmpeg.ErrDestinationExists) {
					category = ytdlp.ErrorInvalidInput
				}
				return subtitleConvertError(category, "convert subtitles", err)
			}
			current.InfoJSON = metadata
			if err := os.Remove(source); err != nil {
				return subtitleConvertError(ytdlp.ErrorInternal, "remove converted subtitle", err)
			}
			artifact.Path = resultPath
		}
		for index := range current.Entries {
			if err := walk(&current.Entries[index]); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(result)
}

// planSubtitleInfoJSON validates and builds a prospective metadata update before
// ffmpeg is invoked. The returned value is assigned only after conversion has
// atomically completed, avoiding a metadata failure that leaves a destination.
// Only requested_subtitles entries whose filepath exactly identifies the source
// are changed; other extractor-provided metadata is left untouched.
func planSubtitleInfoJSON(raw json.RawMessage, root, originalPath, source, destination, resultPath, ffmpegFormat string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	if len(raw) > maxSubtitleInfoJSONBytes {
		return nil, subtitleConvertError(ytdlp.ErrorInternal, "update subtitle metadata", errors.New("subtitle metadata exceeds size limit"))
	}
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, subtitleConvertError(ytdlp.ErrorInternal, "update subtitle metadata", errors.New("invalid client metadata"))
	}
	requested, ok := info["requested_subtitles"].(map[string]any)
	if !ok {
		return raw, nil
	}
	if len(requested) > maxSubtitleConversions {
		return nil, subtitleConvertError(ytdlp.ErrorInternal, "update subtitle metadata", errors.New("too many requested subtitles"))
	}
	extension := map[string]string{"webvtt": "vtt"}[ffmpegFormat]
	if extension == "" {
		extension = ffmpegFormat
	}
	changed := false
	for _, entry := range requested {
		subtitle, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		path, _ := subtitle["filepath"].(string)
		if path != originalPath && !sameSubtitleInfoPath(root, path, source) {
			continue
		}
		subtitle["filepath"] = resultPath
		subtitle["ext"] = extension
		changed = true
	}
	if !changed {
		return raw, nil
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return nil, subtitleConvertError(ytdlp.ErrorInternal, "update subtitle metadata", errors.New("encode subtitle metadata"))
	}
	return encoded, nil
}

func sameSubtitleInfoPath(root, candidate, source string) bool {
	if candidate == "" || strings.ContainsRune(candidate, 0) {
		return false
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	normalized, err := filepath.Abs(candidate)
	return err == nil && normalized == source && isWithin(root, normalized)
}

func subtitleConversionPlan(root, sourcePath, ffmpegFormat string) (source, destination, resultPath string, same bool, err error) {
	if sourcePath == "" || strings.ContainsRune(sourcePath, 0) {
		return "", "", "", false, subtitleConvertError(ytdlp.ErrorSecurity, "convert subtitles", errors.New("unsafe subtitle path"))
	}
	candidate := sourcePath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	source, err = filepath.Abs(candidate)
	if err != nil {
		return "", "", "", false, subtitleConvertError(ytdlp.ErrorSecurity, "convert subtitles", err)
	}
	if !isWithin(root, source) {
		return "", "", "", false, subtitleConvertError(ytdlp.ErrorSecurity, "convert subtitles", errors.New("subtitle path is outside output root"))
	}
	info, statErr := os.Lstat(source)
	if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		if statErr != nil {
			return "", "", "", false, subtitleConvertError(ytdlp.ErrorInvalidInput, "convert subtitles", errors.New("subtitle source cannot be inspected"))
		}
		return "", "", "", false, subtitleConvertError(ytdlp.ErrorSecurity, "convert subtitles", errors.New("subtitle source is not a regular file"))
	}
	extension := map[string]string{"webvtt": "vtt"}[ffmpegFormat]
	if extension == "" {
		extension = ffmpegFormat
	}
	if strings.EqualFold(strings.TrimPrefix(filepath.Ext(source), "."), extension) {
		return source, source, sourcePath, true, nil
	}
	destination = strings.TrimSuffix(source, filepath.Ext(source)) + "." + extension
	if !isWithin(root, destination) || !safeDestinationParents(root, destination) {
		return "", "", "", false, subtitleConvertError(ytdlp.ErrorSecurity, "convert subtitles", errors.New("unsafe subtitle destination"))
	}
	resultPath = destination
	if !filepath.IsAbs(sourcePath) {
		resultPath, err = filepath.Rel(root, destination)
		if err != nil {
			return "", "", "", false, subtitleConvertError(ytdlp.ErrorSecurity, "convert subtitles", errors.New("unsafe subtitle destination"))
		}
	}
	return source, destination, resultPath, false, nil
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func safeDestinationParents(root, destination string) bool {
	rel, err := filepath.Rel(root, filepath.Dir(destination))
	if err != nil {
		return false
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func subtitleConvertError(category ytdlp.ErrorCategory, op string, err error) error {
	return &ytdlp.Error{Category: category, Op: op, Err: err}
}
