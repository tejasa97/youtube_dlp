package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/postprocess"
	"github.com/ytdlp-go/ytdlp/internal/sponsorblock"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// applySponsorBlockRemove cuts downloaded media (and supported subtitle
// sidecars) using the already-fetched sponsorblock_chapters. It is a no-op
// under Simulate/SkipDownload and when Remove is unset. Missing ffmpeg fails
// closed. Unsupported subtitle sidecar formats fail closed to avoid silent
// desync (a documented deviation from yt-dlp's warn-and-continue policy).
func (operation *operation) applySponsorBlockRemove(ctx context.Context, info *value.Info, mediaPath string, artifacts []Artifact, sink events.Sink) (string, []Artifact, error) {
	if !operation.request.SponsorBlock.Enabled || !operation.request.SponsorBlock.Remove {
		return mediaPath, artifacts, nil
	}
	if operation.request.Simulate || operation.request.SkipDownload {
		return mediaPath, artifacts, nil
	}
	if info == nil {
		return mediaPath, artifacts, &Error{Category: ErrorInternal, Op: "sponsorblock remove", Err: errors.New("missing metadata")}
	}
	if strings.TrimSpace(mediaPath) == "" {
		return mediaPath, artifacts, &Error{Category: ErrorInternal, Op: "sponsorblock remove", Err: errors.New("missing media")}
	}
	duration := 0.0
	if raw := info.Lookup("duration"); !raw.IsMissing() {
		if d, ok := sponsorblockDuration(raw); ok {
			duration = d
		}
	}
	if duration <= 0 {
		return mediaPath, artifacts, mapSponsorBlockError(fmt.Errorf("%w: remove duration", sponsorblock.ErrInvalidInput))
	}
	chapters, err := sponsorblockChaptersFromInfo(info)
	if err != nil {
		return mediaPath, artifacts, mapSponsorBlockError(err)
	}
	removeCategories := operation.request.SponsorBlock.RemoveCategories
	if len(removeCategories) == 0 {
		removeCategories = sponsorblock.FilterRemovableCategories(operation.request.SponsorBlock.Categories)
	} else {
		removeCategories = sponsorblock.FilterRemovableCategories(removeCategories)
	}
	plan, err := sponsorblock.PlanCuts(chapters, removeCategories, duration)
	if err != nil {
		return mediaPath, artifacts, mapSponsorBlockError(err)
	}
	if len(plan.Cuts) == 0 {
		return mediaPath, artifacts, nil
	}
	ranges := make([]ffmpeg.ConcatRange, 0, len(plan.Keep))
	for _, segment := range plan.Keep {
		item := ffmpeg.ConcatRange{}
		if segment.InPoint != nil {
			item.InPoint = postprocess.FormatConcatTimestamp(*segment.InPoint)
		}
		if segment.OutPoint != nil {
			item.OutPoint = postprocess.FormatConcatTimestamp(*segment.OutPoint)
		}
		ranges = append(ranges, item)
	}
	boundaries := make([]float64, 0, len(plan.Cuts)*2)
	for _, cut := range plan.Cuts {
		boundaries = append(boundaries, cut.Start, cut.End)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		return mediaPath, artifacts, &Error{Category: ErrorInternal, Op: "sponsorblock remove", Err: errors.New("internal failure")}
	}
	if err := (postprocess.SponsorBlockCut{
		Input:          postprocess.Artifact{Path: mediaPath, Kind: postprocess.ArtifactMedia},
		Output:         postprocess.Artifact{Path: mediaPath, Kind: postprocess.ArtifactMedia},
		Ranges:         ranges,
		Boundaries:     boundaries,
		ForceKeyframes: operation.request.SponsorBlock.ForceKeyframes,
		Overwrite:      true,
	}).Run(ctx, tools, sink); err != nil {
		return mediaPath, artifacts, mapSponsorBlockMediaError(err)
	}

	artifacts, err = operation.cutSponsorBlockSubtitles(ctx, tools, artifacts, ranges, sink)
	if err != nil {
		return mediaPath, artifacts, err
	}

	info.Set("duration", value.Float(plan.Duration))
	if operation.request.SponsorBlock.Mark {
		if err := rewriteMarkedChaptersAfterCuts(info, plan.Cuts); err != nil {
			return mediaPath, artifacts, mapSponsorBlockError(err)
		}
	}
	return mediaPath, artifacts, nil
}

func (operation *operation) cutSponsorBlockSubtitles(ctx context.Context, tools *ffmpeg.Toolset, artifacts []Artifact, ranges []ffmpeg.ConcatRange, sink events.Sink) ([]Artifact, error) {
	for _, artifact := range artifacts {
		if artifact.Kind != "subtitle" {
			continue
		}
		extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(artifact.Path)), ".")
		if !postprocess.SupportedSponsorBlockSubtitleExt(extension) {
			return artifacts, &Error{
				Category: ErrorUnsupported,
				Op:       "sponsorblock remove subtitle",
				Err:      errors.New("unsupported"),
			}
		}
		info, err := os.Lstat(artifact.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return artifacts, &Error{Category: ErrorInternal, Op: "sponsorblock remove subtitle", Err: errors.New("internal failure")}
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return artifacts, &Error{Category: ErrorInternal, Op: "sponsorblock remove subtitle", Err: errors.New("internal failure")}
		}
		if err := (postprocess.SponsorBlockSubtitleCut{
			Input:     postprocess.Artifact{Path: artifact.Path, Kind: postprocess.ArtifactSubtitle},
			Output:    postprocess.Artifact{Path: artifact.Path, Kind: postprocess.ArtifactSubtitle},
			Ranges:    ranges,
			Overwrite: true,
		}).Run(ctx, tools, sink); err != nil {
			return artifacts, mapSponsorBlockMediaError(err)
		}
	}
	return artifacts, nil
}

func sponsorblockChaptersFromInfo(info *value.Info) ([]sponsorblock.Chapter, error) {
	raw := info.Lookup("sponsorblock_chapters")
	if raw.IsMissing() || raw.IsNull() {
		return nil, nil
	}
	list, ok := raw.ListValue()
	if !ok {
		return nil, fmt.Errorf("%w: sponsorblock chapters", sponsorblock.ErrInvalidInput)
	}
	if len(list) > sponsorblock.MaxSegmentCount {
		return nil, fmt.Errorf("%w: sponsorblock chapter limit", sponsorblock.ErrInvalidInput)
	}
	chapters := make([]sponsorblock.Chapter, 0, len(list))
	for _, item := range list {
		object, ok := item.Object()
		if !ok {
			return nil, fmt.Errorf("%w: sponsorblock chapter object", sponsorblock.ErrInvalidInput)
		}
		start, ok := sponsorblockNumber(object.Lookup("start_time"))
		if !ok {
			return nil, fmt.Errorf("%w: sponsorblock chapter start", sponsorblock.ErrInvalidInput)
		}
		end, ok := sponsorblockNumber(object.Lookup("end_time"))
		if !ok {
			return nil, fmt.Errorf("%w: sponsorblock chapter end", sponsorblock.ErrInvalidInput)
		}
		category, _ := object.Lookup("category").StringValue()
		title, _ := object.Lookup("title").StringValue()
		action, _ := object.Lookup("type").StringValue()
		chapters = append(chapters, sponsorblock.Chapter{
			StartTime: start, EndTime: end, Category: category, Title: title, Type: action,
		})
	}
	return chapters, nil
}

func rewriteMarkedChaptersAfterCuts(info *value.Info, cuts []sponsorblock.Range) error {
	raw := info.Lookup("chapters")
	if raw.IsMissing() || raw.IsNull() {
		return nil
	}
	list, ok := raw.ListValue()
	if !ok {
		return fmt.Errorf("%w: chapters", sponsorblock.ErrInvalidInput)
	}
	marked := make([]sponsorblock.MarkedChapter, 0, len(list))
	originals := make([]value.Value, 0, len(list))
	for index, item := range list {
		object, ok := item.Object()
		if !ok {
			return fmt.Errorf("%w: chapter object", sponsorblock.ErrInvalidInput)
		}
		start, ok := sponsorblockNumber(object.Lookup("start_time"))
		if !ok {
			return fmt.Errorf("%w: chapter start", sponsorblock.ErrInvalidInput)
		}
		end, ok := sponsorblockNumber(object.Lookup("end_time"))
		if !ok {
			return fmt.Errorf("%w: chapter end", sponsorblock.ErrInvalidInput)
		}
		title, _ := object.Lookup("title").StringValue()
		chapter := sponsorblock.MarkedChapter{StartTime: start, EndTime: end, Title: title, Source: index}
		if category, ok := object.Lookup("category").StringValue(); ok && category != "" {
			chapter.Sponsor = true
			chapter.Category = category
		}
		marked = append(marked, chapter)
		originals = append(originals, item)
	}
	rewritten := sponsorblock.RewriteChapterTimes(marked, cuts)
	info.Set("chapters", value.List(renderMarkedChapters(rewritten, originals)...))
	return nil
}

func mapSponsorBlockMediaError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &Error{Category: ErrorCancelled, Op: "sponsorblock remove", Err: err}
	case errors.Is(err, ffmpeg.ErrFFmpegUnavailable), errors.Is(err, ffmpeg.ErrFFprobeUnavailable):
		return &Error{Category: ErrorInternal, Op: "sponsorblock remove", Err: errors.New("internal failure")}
	case errors.Is(err, ffmpeg.ErrInvalidOperation), errors.Is(err, postprocess.ErrInvalidGraph), errors.Is(err, postprocess.ErrUnsafePath):
		return &Error{Category: ErrorInvalidInput, Op: "sponsorblock remove", Err: errors.New("invalid input")}
	case errors.Is(err, ffmpeg.ErrDestinationExists):
		return &Error{Category: ErrorInvalidInput, Op: "sponsorblock remove", Err: errors.New("invalid input")}
	default:
		return &Error{Category: ErrorInternal, Op: "sponsorblock remove", Err: errors.New("internal failure")}
	}
}

func sponsorBlockFetchCategories(options SponsorBlockOptions) []string {
	seen := make(map[string]struct{}, len(options.Categories)+len(options.RemoveCategories))
	out := make([]string, 0, len(options.Categories)+len(options.RemoveCategories))
	appendUnique := func(values []string) {
		for _, raw := range values {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			if _, dup := seen[trimmed]; dup {
				continue
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}
	appendUnique(options.Categories)
	appendUnique(options.RemoveCategories)
	return out
}
