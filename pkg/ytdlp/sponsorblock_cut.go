package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/compat/chapterremove"
	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/postprocess"
	"github.com/ytdlp-go/ytdlp/internal/sponsorblock"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type sponsorBlockCutJob struct {
	kind     postprocess.ArtifactKind
	original string
	staged   string
	backup   string
}

// applySponsorBlockRemove retains the historical internal test seam. Product
// execution calls applyChapterCuts, which combines SponsorBlock, ordinary
// chapter-title, and manual range removals into one transaction.
func (operation *operation) applySponsorBlockRemove(ctx context.Context, info *value.Info, mediaPath string, artifacts []Artifact, sink events.Sink) (string, []Artifact, bool, error) {
	return operation.applyChapterCuts(ctx, info, mediaPath, artifacts, sink)
}

// applyChapterCuts cuts downloaded media with ffmpeg and remaps supported
// subtitle sidecars with deterministic cue editing. It is a no-op for media
// mutation under Simulate/SkipDownload.
//
// When Mark+Remove are both enabled, chapters are arranged once via the pinned
// ModifyChapters heap algorithm. Remove-only retimes ordinary chapters through
// the same arrangement. Mark+Remove with no resulting cuts still commits the
// arranged marked chapters (pinned ModifyChapters assigns chapters before its
// no-cuts return). Under Simulate/SkipDownload, media is never cut, but Mark
// metadata is still applied when requested so deferred marking is not lost.
// When media is cut, metadata commits only after a successful media/subtitle
// cut commit.
func (operation *operation) applyChapterCuts(ctx context.Context, info *value.Info, mediaPath string, artifacts []Artifact, sink events.Sink) (string, []Artifact, bool, error) {
	sponsorRemove := operation.request.SponsorBlock.Enabled && operation.request.SponsorBlock.Remove
	chapterRemoval := operation.compatibility.chapterRemoval
	if !sponsorRemove && chapterRemoval.Empty() {
		return mediaPath, artifacts, false, nil
	}
	cutOp := "sponsorblock remove"
	if !chapterRemoval.Empty() {
		cutOp = "remove chapters"
	}
	if info == nil {
		return mediaPath, artifacts, false, &Error{Category: ErrorInternal, Op: cutOp, Err: errors.New("missing metadata")}
	}
	skipMediaCut := operation.request.Simulate || operation.request.SkipDownload
	if skipMediaCut && !operation.request.SponsorBlock.Mark {
		return mediaPath, artifacts, false, nil
	}
	metadataDuration := 0.0
	if raw := info.Lookup("duration"); !raw.IsMissing() {
		if d, ok := sponsorblockDuration(raw); ok {
			metadataDuration = d
		}
	}
	normal, originals, err := ordinarySponsorBlockChapters(info, metadataDuration, true)
	if err != nil {
		return mediaPath, artifacts, false, mapChapterCutError(cutOp, err)
	}
	ordinaryRemove := make(map[int]struct{})
	if chapterRemoval.HasPatterns() {
		if len(normal) == 0 {
			if err := operation.emitChapterRemovalWarning(ctx, "Chapter information is unavailable"); err != nil {
				return mediaPath, artifacts, false, err
			}
		} else {
			for _, chapter := range normal {
				match, matchErr := chapterRemoval.MatchTitle(ctx, chapter.Title)
				if matchErr != nil {
					return mediaPath, artifacts, false, mapChapterCutError(cutOp, matchErr)
				}
				if match {
					ordinaryRemove[chapter.Source] = struct{}{}
				}
			}
			if len(ordinaryRemove) == 0 {
				if err := operation.emitChapterRemovalWarning(ctx, "There are no chapters matching the regex"); err != nil {
					return mediaPath, artifacts, false, err
				}
			}
		}
	}
	deferredMark := operation.request.SponsorBlock.Enabled && operation.request.SponsorBlock.Mark
	duration := metadataDuration
	var tools *ffmpeg.Toolset
	var chapters []sponsorblock.Chapter
	if sponsorRemove || deferredMark {
		chapters, err = sponsorblockChaptersFromInfo(info)
		if err != nil {
			return mediaPath, artifacts, false, mapChapterCutError(cutOp, err)
		}
	}
	var removeCategories []string
	if sponsorRemove {
		removeCategories = operation.request.SponsorBlock.RemoveCategories
		if len(removeCategories) == 0 {
			removeCategories = sponsorblock.FilterRemovableCategories(operation.request.SponsorBlock.Categories)
		} else {
			removeCategories = sponsorblock.FilterRemovableCategories(removeCategories)
		}
	}
	sponsorRemoval := false
	if sponsorRemove {
		removeSet := make(map[string]struct{}, len(removeCategories))
		for _, category := range removeCategories {
			removeSet[category] = struct{}{}
		}
		for _, chapter := range chapters {
			if _, ok := removeSet[chapter.Category]; ok {
				sponsorRemoval = true
				break
			}
		}
	}
	needsPostprocess := len(ordinaryRemove) > 0 || chapterRemoval.HasRanges() || sponsorRemoval || deferredMark
	if !skipMediaCut && !needsPostprocess {
		return mediaPath, artifacts, false, nil
	}
	if !skipMediaCut {
		if strings.TrimSpace(mediaPath) == "" {
			return mediaPath, artifacts, false, &Error{Category: ErrorInternal, Op: cutOp, Err: errors.New("missing media")}
		}
		tools, duration, err = probeChapterCutDuration(ctx, mediaPath)
		if err != nil {
			return mediaPath, artifacts, false, mapChapterCutMediaError(cutOp, err)
		}
		if metadataDuration > 0 && math.Abs(duration-metadataDuration) > 1 {
			return mediaPath, artifacts, false, mapChapterCutError(cutOp, fmt.Errorf("%w: media duration mismatch", sponsorblock.ErrInvalidInput))
		}
		normal, originals, err = ordinarySponsorBlockChapters(info, duration, false)
		if err != nil {
			return mediaPath, artifacts, false, mapChapterCutError(cutOp, err)
		}
	}
	if duration <= 0 {
		return mediaPath, artifacts, false, mapChapterCutError(cutOp, fmt.Errorf("%w: remove duration", sponsorblock.ErrInvalidInput))
	}
	manualRanges, err := chapterRemoval.ResolveRanges(duration)
	if err != nil && chapterRemoval.HasRanges() {
		return mediaPath, artifacts, false, mapChapterCutError(cutOp, err)
	}
	if !chapterRemoval.HasRanges() {
		manualRanges = nil
	}
	title, _ := info.Lookup("title").StringValue()
	// Under Simulate/SkipDownload the media stays uncut, so arrange with mark
	// overlays only (no remove flags) to avoid post-cut timestamps on uncut media.
	mark := operation.request.SponsorBlock.Mark
	arrangeRemove := removeCategories
	if skipMediaCut {
		arrangeRemove = nil
		ordinaryRemove = nil
		manualRanges = nil
	}
	arranged, err := arrangeChapterRemove(normal, chapters, arrangeRemove, ordinaryRemove, manualRanges, duration, title, mark)
	if err != nil {
		return mediaPath, artifacts, false, mapChapterCutError(cutOp, err)
	}
	if skipMediaCut {
		if mark {
			info.Set("chapters", renderArrangedChapters(arranged.Chapters, originals))
		}
		return mediaPath, artifacts, false, nil
	}
	if len(arranged.Cuts) == 0 {
		// Pinned ModifyChapters assigns arranged chapters before returning when
		// there are no cuts; keep mark overlays without inventing a media cut.
		if mark {
			info.Set("chapters", renderArrangedChapters(arranged.Chapters, originals))
		}
		return mediaPath, artifacts, false, nil
	}
	jobs, err := operation.prepareSponsorBlockCutJobs(cutOp, mediaPath, artifacts)
	if err != nil {
		return mediaPath, artifacts, false, err
	}

	if tools == nil {
		return mediaPath, artifacts, false, &Error{Category: ErrorInternal, Op: cutOp, Err: errors.New("internal failure")}
	}
	// Empty post-arrange chapters means the keep timeline vanished (pinned
	// ModifyChapters refuses to remove the entire video).
	if len(arranged.Chapters) == 0 {
		return mediaPath, artifacts, false, mapChapterCutError(cutOp, fmt.Errorf("%w: entire media removed", sponsorblock.ErrInvalidInput))
	}
	plan, err := cutPlanFromArrange(arranged.Cuts, duration)
	if err != nil {
		return mediaPath, artifacts, false, mapChapterCutError(cutOp, err)
	}
	if len(plan.Cuts) == 0 {
		if mark {
			info.Set("chapters", renderArrangedChapters(arranged.Chapters, originals))
		}
		return mediaPath, artifacts, false, nil
	}

	preparedChapters := renderArrangedChapters(arranged.Chapters, originals)
	hasChapters := len(arranged.Chapters) > 0 || len(originals) > 0 || mark

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

	stagingDir, err := os.MkdirTemp(filepath.Dir(mediaPath), ".ytdlp-chapter-cut-tx-")
	if err != nil {
		return mediaPath, artifacts, false, &Error{Category: ErrorInternal, Op: cutOp, Err: errors.New("internal failure")}
	}
	defer os.RemoveAll(stagingDir)

	for index := range jobs {
		jobs[index].staged = filepath.Join(stagingDir, fmt.Sprintf("%d%s", index, filepath.Ext(jobs[index].original)))
		jobs[index].backup = filepath.Join(stagingDir, fmt.Sprintf("backup-%d%s", index, filepath.Ext(jobs[index].original)))
		switch jobs[index].kind {
		case postprocess.ArtifactMedia:
			forceKeyframes := operation.request.ForceKeyframesAtCuts || operation.request.SponsorBlock.ForceKeyframes
			if err := tools.CutOutRanges(ctx, jobs[index].original, jobs[index].staged, ranges, boundaries, forceKeyframes, false, sink); err != nil {
				return mediaPath, artifacts, false, mapChapterCutMediaError(cutOp, err)
			}
		case postprocess.ArtifactSubtitle:
			if err := stageSponsorBlockSubtitle(jobs[index].original, jobs[index].staged, plan.Cuts); err != nil {
				return mediaPath, artifacts, false, mapChapterCutMediaError(cutOp, err)
			}
		default:
			return mediaPath, artifacts, false, &Error{Category: ErrorInternal, Op: cutOp, Err: errors.New("internal failure")}
		}
	}

	if err := commitSponsorBlockCutJobs(ctx, jobs); err != nil {
		return mediaPath, artifacts, false, mapChapterCutMediaError(cutOp, err)
	}

	// Apply prepared metadata only after file commit succeeds.
	info.Set("duration", value.Float(plan.Duration))
	if hasChapters {
		info.Set("chapters", preparedChapters)
	}
	return mediaPath, artifacts, true, nil
}

func probeChapterCutDuration(ctx context.Context, mediaPath string) (*ffmpeg.Toolset, float64, error) {
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		return nil, 0, err
	}
	probe, err := tools.Probe(ctx, mediaPath)
	if err != nil {
		return nil, 0, err
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return nil, 0, fmt.Errorf("%w: invalid probed duration", ffmpeg.ErrMediaFailure)
	}
	return tools, duration, nil
}

func arrangeSponsorBlockRemove(normal []sponsorblock.NormalChapter, sponsors []sponsorblock.Chapter, removeCategories []string, duration float64, videoTitle string, mark bool) (sponsorblock.ArrangeResult, error) {
	return arrangeChapterRemove(normal, sponsors, removeCategories, nil, nil, duration, videoTitle, mark)
}

func arrangeChapterRemove(normal []sponsorblock.NormalChapter, sponsors []sponsorblock.Chapter, removeCategories []string, ordinaryRemove map[int]struct{}, manualRanges []chapterremove.Range, duration float64, videoTitle string, mark bool) (sponsorblock.ArrangeResult, error) {
	removeSet := make(map[string]struct{}, len(removeCategories))
	for _, category := range removeCategories {
		removeSet[category] = struct{}{}
	}
	input := make([]sponsorblock.ArrangeChapter, 0, len(normal)+len(sponsors)+len(manualRanges)+1)
	if len(normal) == 0 {
		if len(sponsors) > 0 || len(manualRanges) > 0 {
			input = append(input, sponsorblock.ArrangeChapter{
				StartTime: 0, EndTime: duration, Title: videoTitle, Source: -1,
			})
		}
	} else {
		for _, chapter := range normal {
			_, remove := ordinaryRemove[chapter.Source]
			input = append(input, sponsorblock.ArrangeChapter{
				StartTime: chapter.StartTime, EndTime: chapter.EndTime,
				Title: chapter.Title, Source: chapter.Source, Remove: remove,
			})
		}
	}
	for _, chapter := range sponsors {
		_, remove := removeSet[chapter.Category]
		if chapter.Type == string(sponsorblock.ActionPOI) || chapter.Type == string(sponsorblock.ActionChapter) {
			remove = false
		}
		if !sponsorblock.IsRemovableCategory(chapter.Category) {
			remove = false
		}
		if !mark && !remove {
			continue
		}
		input = append(input, sponsorblock.ArrangeChapter{
			StartTime: chapter.StartTime, EndTime: chapter.EndTime,
			Title: chapter.Title, Remove: remove, Source: -1,
			Categories: []sponsorblock.CategorySpan{{
				Category: chapter.Category, Start: chapter.StartTime, End: chapter.EndTime, Title: chapter.Title,
			}},
		})
	}
	for _, interval := range manualRanges {
		if interval.End == nil {
			return sponsorblock.ArrangeResult{}, fmt.Errorf("%w: unresolved manual range", sponsorblock.ErrInvalidInput)
		}
		input = append(input, sponsorblock.ArrangeChapter{
			StartTime: interval.Start,
			EndTime:   *interval.End,
			Remove:    true,
			Source:    -1,
		})
	}
	return sponsorblock.Arrange(input)
}

func (operation *operation) emitChapterRemovalWarning(ctx context.Context, message string) error {
	if err := operation.client.emit(ctx, Event{Kind: EventMetadataWarning, Message: message}); err != nil {
		return &Error{Category: ErrorInternal, Op: "emit chapter removal warning", Err: err}
	}
	return nil
}

func cutPlanFromArrange(cuts []sponsorblock.Range, duration float64) (sponsorblock.CutPlan, error) {
	if len(cuts) == 0 {
		return sponsorblock.CutPlan{Keep: []sponsorblock.ConcatSegment{{}}, Duration: duration}, nil
	}
	// Reuse PlanCuts validation bounds by synthesizing removable chapters.
	synthetic := make([]sponsorblock.Chapter, 0, len(cuts))
	for _, cut := range cuts {
		synthetic = append(synthetic, sponsorblock.Chapter{
			StartTime: cut.Start, EndTime: cut.End, Category: "sponsor", Type: "skip",
		})
	}
	return sponsorblock.PlanCuts(synthetic, []string{"sponsor"}, duration)
}

func renderArrangedChapters(chapters []sponsorblock.ArrangeChapter, originals []value.Value) value.Value {
	marked := make([]sponsorblock.MarkedChapter, 0, len(chapters))
	for _, chapter := range chapters {
		item := sponsorblock.MarkedChapter{
			StartTime: chapter.StartTime, EndTime: chapter.EndTime,
			Title: chapter.Title, Source: chapter.Source,
			Sponsor:  chapter.Sponsor || len(chapter.Categories) > 0 || chapter.Category != "",
			Category: chapter.Category, Categories: chapter.CategoryList,
			Name: chapter.Name, CategoryNames: chapter.CategoryNames,
		}
		if item.Sponsor && item.Category == "" && len(chapter.Categories) > 0 {
			item.Category = chapter.Categories[0].Category
		}
		if item.Sponsor && len(item.Categories) == 0 && len(chapter.Categories) > 0 {
			cats := make([]string, 0, len(chapter.Categories))
			names := make([]string, 0, len(chapter.Categories))
			for _, span := range chapter.Categories {
				cats = append(cats, span.Category)
				names = append(names, span.Title)
			}
			item.Categories = cats
			item.CategoryNames = names
		}
		marked = append(marked, item)
	}
	return value.List(renderMarkedChapters(marked, originals)...)
}

func stageSponsorBlockSubtitle(original, staged string, cuts []sponsorblock.Range) error {
	data, err := os.ReadFile(original)
	if err != nil {
		return err
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(original)), ".")
	rewritten, err := sponsorblock.CutSubtitle(ext, data, cuts)
	if err != nil {
		return err
	}
	return os.WriteFile(staged, rewritten, 0o600)
}

func (operation *operation) prepareSponsorBlockCutJobs(op, mediaPath string, artifacts []Artifact) ([]sponsorBlockCutJob, error) {
	if err := validateSponsorBlockCutPath(mediaPath); err != nil {
		return nil, &Error{Category: ErrorInternal, Op: op, Err: errors.New("internal failure")}
	}
	jobs := []sponsorBlockCutJob{{kind: postprocess.ArtifactMedia, original: mediaPath}}
	for _, artifact := range artifacts {
		if artifact.Kind != "subtitle" {
			continue
		}
		extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(artifact.Path)), ".")
		if !sponsorblock.SupportedSubtitleExt(extension) {
			return nil, &Error{
				Category: ErrorUnsupported,
				Op:       op + " subtitle",
				Err:      errors.New("unsupported"),
			}
		}
		if err := validateSponsorBlockCutPath(artifact.Path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, &Error{Category: ErrorInternal, Op: op + " subtitle", Err: errors.New("internal failure")}
		}
		jobs = append(jobs, sponsorBlockCutJob{kind: postprocess.ArtifactSubtitle, original: artifact.Path})
	}
	return jobs, nil
}

func validateSponsorBlockCutPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", os.ErrInvalid)
	}
	return nil
}

func commitSponsorBlockCutJobs(ctx context.Context, jobs []sponsorBlockCutJob) error {
	for index := range jobs {
		if err := validateSponsorBlockCutPath(jobs[index].staged); err != nil {
			return err
		}
	}
	committed := 0
	rollback := func(primary error) error {
		var rollbackErrs []error
		for index := 0; index < committed; index++ {
			if err := restoreSponsorBlockBackup(jobs[index]); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("restore %s: %w", jobs[index].original, err))
			}
		}
		if len(rollbackErrs) == 0 {
			return primary
		}
		return fmt.Errorf("%w (rollback failed: %v)", primary, errors.Join(rollbackErrs...))
	}
	for index := range jobs {
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		if err := os.Rename(jobs[index].original, jobs[index].backup); err != nil {
			return rollback(err)
		}
		if err := replaceSponsorBlockPath(jobs[index].staged, jobs[index].original); err != nil {
			if restoreErr := restoreSponsorBlockBackup(sponsorBlockCutJob{
				original: jobs[index].original, backup: jobs[index].backup,
			}); restoreErr != nil {
				return fmt.Errorf("%w (restore current: %v)", err, restoreErr)
			}
			return rollback(err)
		}
		committed++
	}
	for index := range jobs {
		_ = os.Remove(jobs[index].backup)
	}
	return nil
}

func replaceSponsorBlockPath(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	// Windows cannot rename onto an existing path; destination should be free
	// after the backup rename, but fall back to copy when rename still fails.
	return copySponsorBlockFile(source, destination)
}

func restoreSponsorBlockBackup(job sponsorBlockCutJob) error {
	if _, err := os.Lstat(job.original); err == nil {
		if runtime.GOOS == "windows" {
			if err := os.Remove(job.original); err != nil {
				return err
			}
		} else {
			// Prefer rename onto free path; if original reappeared, remove first.
			_ = os.Remove(job.original)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(job.backup, job.original); err == nil {
		return nil
	}
	return copySponsorBlockFile(job.backup, job.original)
}

func copySponsorBlockFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	return errors.Join(copyErr, syncErr, closeErr)
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

// prepareRewrittenChapters validates and builds the post-cut chapters value
// without mutating info. ok is false when chapters were absent.
func prepareRewrittenChapters(info *value.Info, cuts []sponsorblock.Range) (value.Value, bool, error) {
	raw := info.Lookup("chapters")
	if raw.IsMissing() || raw.IsNull() {
		return value.Value{}, false, nil
	}
	list, ok := raw.ListValue()
	if !ok {
		return value.Value{}, false, fmt.Errorf("%w: chapters", sponsorblock.ErrInvalidInput)
	}
	marked := make([]sponsorblock.MarkedChapter, 0, len(list))
	originals := make([]value.Value, 0, len(list))
	for index, item := range list {
		object, ok := item.Object()
		if !ok {
			return value.Value{}, false, fmt.Errorf("%w: chapter object", sponsorblock.ErrInvalidInput)
		}
		start, ok := sponsorblockNumber(object.Lookup("start_time"))
		if !ok {
			return value.Value{}, false, fmt.Errorf("%w: chapter start", sponsorblock.ErrInvalidInput)
		}
		end, ok := sponsorblockNumber(object.Lookup("end_time"))
		if !ok {
			return value.Value{}, false, fmt.Errorf("%w: chapter end", sponsorblock.ErrInvalidInput)
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
	return value.List(renderMarkedChapters(rewritten, originals)...), true, nil
}

func rewriteChaptersAfterCuts(info *value.Info, cuts []sponsorblock.Range) error {
	prepared, present, err := prepareRewrittenChapters(info, cuts)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	info.Set("chapters", prepared)
	return nil
}

func mapSponsorBlockMediaError(err error) error {
	return mapChapterCutMediaError("sponsorblock remove", err)
}

func mapChapterCutError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Category: ErrorCancelled, Op: op, Err: err}
	}
	if errors.Is(err, sponsorblock.ErrInvalidInput) ||
		errors.Is(err, chapterremove.ErrInvalidSpecification) ||
		errors.Is(err, chapterremove.ErrLimit) {
		return &Error{Category: ErrorInvalidInput, Op: op, Err: errors.New("invalid input")}
	}
	return &Error{Category: ErrorInternal, Op: op, Err: errors.New("internal failure")}
}

func mapChapterCutMediaError(op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &Error{Category: ErrorCancelled, Op: op, Err: err}
	case errors.Is(err, ffmpeg.ErrFFmpegUnavailable), errors.Is(err, ffmpeg.ErrFFprobeUnavailable):
		return &Error{Category: ErrorInternal, Op: op, Err: errors.New("internal failure")}
	case errors.Is(err, sponsorblock.ErrUnsupported):
		return &Error{Category: ErrorUnsupported, Op: op + " subtitle", Err: errors.New("unsupported")}
	case errors.Is(err, sponsorblock.ErrInvalidInput), errors.Is(err, ffmpeg.ErrInvalidOperation), errors.Is(err, postprocess.ErrInvalidGraph), errors.Is(err, postprocess.ErrUnsafePath):
		return &Error{Category: ErrorInvalidInput, Op: op, Err: errors.New("invalid input")}
	case errors.Is(err, ffmpeg.ErrDestinationExists):
		return &Error{Category: ErrorInvalidInput, Op: op, Err: errors.New("invalid input")}
	default:
		return &Error{Category: ErrorInternal, Op: op, Err: errors.New("internal failure")}
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
