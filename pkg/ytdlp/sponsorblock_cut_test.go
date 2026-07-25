package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/postprocess"
	"github.com/ytdlp-go/ytdlp/internal/sponsorblock"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestSponsorBlockRemoveSkippedUnderSimulateAndSkipDownload(t *testing.T) {
	info := value.Info{}
	info.Set("duration", value.Float(100))
	info.Set("sponsorblock_chapters", value.List(chapterValue(sponsorblock.Chapter{
		StartTime: 10, EndTime: 20, Category: "sponsor", Title: "Sponsor", Type: "skip",
	})))
	for _, request := range []Request{
		{Simulate: true, SponsorBlock: SponsorBlockOptions{Enabled: true, Remove: true, Categories: []string{"sponsor"}}},
		{SkipDownload: true, SponsorBlock: SponsorBlockOptions{Enabled: true, Remove: true, Categories: []string{"sponsor"}}},
	} {
		operation := &operation{request: request}
		path, artifacts, cut, err := operation.applySponsorBlockRemove(context.Background(), &info, "missing.mp4", nil, nil)
		if err != nil {
			t.Fatalf("request=%#v err=%v", request, err)
		}
		if cut || path != "missing.mp4" || artifacts != nil {
			t.Fatalf("unexpected mutation path=%q artifacts=%v cut=%v", path, artifacts, cut)
		}
	}
}

func TestSponsorBlockMarkPlusRemoveNoCutsCommitsMarkedChapters(t *testing.T) {
	media := generateChapterRemovalMedia(t, 6)
	beforeMedia, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	original := value.ObjectValue(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(0)},
		value.Field{Key: "end_time", Value: value.Float(6)},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "custom", Value: value.String("preserved")},
	))
	info := value.Info{}
	info.Set("duration", value.Float(6))
	info.Set("title", value.String("Video"))
	info.Set("chapters", value.List(original))
	info.Set("sponsorblock_chapters", value.List(
		chapterValue(sponsorblock.Chapter{StartTime: 1, EndTime: 2, Category: "sponsor", Title: "Sponsor", Type: "skip"}),
		chapterValue(sponsorblock.Chapter{StartTime: 3, EndTime: 4, Category: "selfpromo", Title: "Unpaid/Self Promotion", Type: "skip"}),
	))
	operation := &operation{request: Request{SponsorBlock: SponsorBlockOptions{
		Enabled: true, Mark: true, Remove: true,
		Categories: []string{"sponsor", "selfpromo"}, RemoveCategories: []string{"intro"},
	}}}
	path, _, cut, err := operation.applySponsorBlockRemove(context.Background(), &info, media, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cut || path != media {
		t.Fatalf("path=%q cut=%v", path, cut)
	}
	body, err := os.ReadFile(media)
	if err != nil || string(body) != string(beforeMedia) {
		t.Fatalf("media mutated: %v", err)
	}
	if duration, _ := sponsorblockNumber(info.Lookup("duration")); duration != 6 {
		t.Fatalf("duration changed to %v", duration)
	}
	chapters, ok := info.Lookup("chapters").ListValue()
	if !ok || len(chapters) < 2 {
		t.Fatalf("chapters = %#v", info.Lookup("chapters"))
	}
	sponsors := 0
	for _, item := range chapters {
		object, ok := item.Object()
		if !ok {
			t.Fatalf("chapter = %#v", item)
		}
		if category, ok := object.Lookup("category").StringValue(); ok && category != "" {
			sponsors++
		}
	}
	if sponsors == 0 {
		t.Fatalf("expected marked sponsors in chapters: %#v", chapters)
	}
	first, _ := chapters[0].Object()
	if custom, _ := first.Lookup("custom").StringValue(); custom != "preserved" {
		t.Fatalf("ordinary fields not preserved: %#v", first)
	}
}

func TestSponsorBlockMarkPlusRemoveSimulateAppliesMarksWithoutCutting(t *testing.T) {
	info := value.Info{}
	info.Set("duration", value.Float(100))
	info.Set("title", value.String("Video"))
	info.Set("chapters", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(0)},
		value.Field{Key: "end_time", Value: value.Float(100)},
		value.Field{Key: "title", Value: value.String("Video")},
		value.Field{Key: "custom", Value: value.String("preserved")},
	))))
	info.Set("sponsorblock_chapters", value.List(chapterValue(sponsorblock.Chapter{
		StartTime: 10, EndTime: 20, Category: "sponsor", Title: "Sponsor", Type: "skip",
	})))
	operation := &operation{request: Request{
		Simulate: true,
		SponsorBlock: SponsorBlockOptions{
			Enabled: true, Mark: true, Remove: true, Categories: []string{"sponsor"},
		},
	}}
	path, _, cut, err := operation.applySponsorBlockRemove(context.Background(), &info, "missing.mp4", nil, nil)
	if err != nil || cut || path != "missing.mp4" {
		t.Fatalf("path=%q cut=%v err=%v", path, cut, err)
	}
	chapters, _ := info.Lookup("chapters").ListValue()
	sponsors := 0
	for _, item := range chapters {
		object, _ := item.Object()
		if category, ok := object.Lookup("category").StringValue(); ok && category != "" {
			sponsors++
		}
	}
	if sponsors != 1 {
		t.Fatalf("simulate mark+remove chapters = %#v", chapters)
	}
	first, _ := chapters[0].Object()
	if custom, _ := first.Lookup("custom").StringValue(); custom != "preserved" {
		t.Fatalf("ordinary fields not preserved: %#v", first)
	}
}

func TestSponsorBlockMarkPlusRemoveSkipDownloadAppliesMarksWithoutCutting(t *testing.T) {
	info := value.Info{}
	info.Set("duration", value.Float(100))
	info.Set("title", value.String("Video"))
	info.Set("chapters", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(0)},
		value.Field{Key: "end_time", Value: value.Float(100)},
		value.Field{Key: "title", Value: value.String("Video")},
	))))
	info.Set("sponsorblock_chapters", value.List(chapterValue(sponsorblock.Chapter{
		StartTime: 10, EndTime: 20, Category: "sponsor", Title: "Sponsor", Type: "skip",
	})))
	operation := &operation{request: Request{
		SkipDownload: true,
		SponsorBlock: SponsorBlockOptions{
			Enabled: true, Mark: true, Remove: true, Categories: []string{"sponsor"},
		},
	}}
	path, _, cut, err := operation.applySponsorBlockRemove(context.Background(), &info, "missing.mp4", nil, nil)
	if err != nil || cut || path != "missing.mp4" {
		t.Fatalf("path=%q cut=%v err=%v", path, cut, err)
	}
	chapters, _ := info.Lookup("chapters").ListValue()
	sponsors := 0
	for _, item := range chapters {
		object, _ := item.Object()
		if category, ok := object.Lookup("category").StringValue(); ok && category != "" {
			sponsors++
		}
	}
	if sponsors != 1 {
		t.Fatalf("skip-download mark+remove chapters = %#v", chapters)
	}
}

func TestSponsorBlockRemoveNoopWithoutCuts(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media.mp4")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := value.Info{}
	info.Set("duration", value.Float(100))
	info.Set("sponsorblock_chapters", value.List())
	operation := &operation{request: Request{SponsorBlock: SponsorBlockOptions{
		Enabled: true, Remove: true, Categories: []string{"sponsor"},
	}}}
	path, _, cut, err := operation.applySponsorBlockRemove(context.Background(), &info, media, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cut || path != media {
		t.Fatalf("path = %q cut=%v", path, cut)
	}
	body, err := os.ReadFile(media)
	if err != nil || string(body) != "media" {
		t.Fatalf("media mutated: %v %q", err, body)
	}
}

func TestSponsorBlockFetchCategoriesUnionsRemoveSet(t *testing.T) {
	got := sponsorBlockFetchCategories(SponsorBlockOptions{
		Categories:       []string{"sponsor", "intro"},
		RemoveCategories: []string{"outro", "sponsor"},
	})
	want := []string{"sponsor", "intro", "outro"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestSponsorBlockUnsupportedSubtitleFailsClosedWithoutMutatingMedia(t *testing.T) {
	root := t.TempDir()
	media := generateChapterRemovalMedia(t, 4)
	sub := filepath.Join(root, "track.json3")
	beforeMedia, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := value.Info{}
	info.Set("duration", value.Float(4))
	info.Set("sponsorblock_chapters", value.List(chapterValue(sponsorblock.Chapter{
		StartTime: 1, EndTime: 2, Category: "sponsor", Title: "Sponsor", Type: "skip",
	})))
	operation := &operation{request: Request{SponsorBlock: SponsorBlockOptions{
		Enabled: true, Remove: true, Categories: []string{"sponsor"},
	}}}
	_, _, cut, err := operation.applySponsorBlockRemove(context.Background(), &info, media, []Artifact{{Path: sub, Kind: "subtitle"}}, nil)
	if cut || err == nil || !IsCategory(err, ErrorUnsupported) {
		t.Fatalf("error = %v cut=%v", err, cut)
	}
	body, readErr := os.ReadFile(media)
	if readErr != nil || string(body) != string(beforeMedia) {
		t.Fatalf("media mutated despite prevalidation failure: %v", readErr)
	}
	subBody, readErr := os.ReadFile(sub)
	if readErr != nil || string(subBody) != "{}" {
		t.Fatalf("subtitle mutated: %v %q", readErr, subBody)
	}
}

func TestSponsorBlockRemoveRemapsOrdinaryChaptersWithoutMark(t *testing.T) {
	info := value.Info{}
	info.Set("chapters", value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "start_time", Value: value.Float(0)},
			value.Field{Key: "end_time", Value: value.Float(30)},
			value.Field{Key: "title", Value: value.String("Intro")},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "start_time", Value: value.Float(30)},
			value.Field{Key: "end_time", Value: value.Float(80)},
			value.Field{Key: "title", Value: value.String("Main")},
		)),
	))
	cuts := []sponsorblock.Range{{Start: 10, End: 20}}
	if err := rewriteChaptersAfterCuts(&info, cuts); err != nil {
		t.Fatal(err)
	}
	list, ok := info.Lookup("chapters").ListValue()
	if !ok || len(list) != 2 {
		t.Fatalf("chapters = %#v", info.Lookup("chapters"))
	}
	first, _ := list[0].Object()
	second, _ := list[1].Object()
	start, _ := sponsorblockNumber(first.Lookup("start_time"))
	end, _ := sponsorblockNumber(first.Lookup("end_time"))
	if start != 0 || end != 20 {
		t.Fatalf("first remapped = %v-%v", start, end)
	}
	start, _ = sponsorblockNumber(second.Lookup("start_time"))
	end, _ = sponsorblockNumber(second.Lookup("end_time"))
	if start != 20 || end != 70 {
		t.Fatalf("second remapped = %v-%v", start, end)
	}
}

func TestSponsorBlockPrepareChaptersBeforeCommit(t *testing.T) {
	info := value.Info{}
	info.Set("duration", value.Float(100))
	info.Set("chapters", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "start_time", Value: value.Float(0)},
		value.Field{Key: "end_time", Value: value.Float(50)},
		value.Field{Key: "title", Value: value.String("A")},
	))))
	before, _ := info.Lookup("chapters").ListValue()
	beforeEnd, _ := sponsorblockNumber(mustObject(t, before[0]).Lookup("end_time"))
	prepared, present, err := prepareRewrittenChapters(&info, []sponsorblock.Range{{Start: 10, End: 20}})
	if err != nil || !present {
		t.Fatalf("prepare = present=%v err=%v", present, err)
	}
	after, _ := info.Lookup("chapters").ListValue()
	afterEnd, _ := sponsorblockNumber(mustObject(t, after[0]).Lookup("end_time"))
	if afterEnd != beforeEnd {
		t.Fatal("prepare must not mutate info before commit")
	}
	list, ok := prepared.ListValue()
	if !ok || len(list) != 1 {
		t.Fatalf("prepared = %#v", prepared)
	}
	end, _ := sponsorblockNumber(mustObject(t, list[0]).Lookup("end_time"))
	if end != 40 {
		t.Fatalf("prepared end = %v", end)
	}
}

func mustObject(t *testing.T, item value.Value) *value.Object {
	t.Helper()
	object, ok := item.Object()
	if !ok {
		t.Fatal("expected object")
	}
	return object
}

func TestSponsorBlockCutCommitIsTransactional(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media.mp4")
	sub := filepath.Join(root, "track.vtt")
	if err := os.WriteFile(media, []byte("media-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("sub-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	stagedMedia := filepath.Join(staging, "0.mp4")
	stagedSub := filepath.Join(staging, "1.vtt")
	if err := os.WriteFile(stagedMedia, []byte("media-staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := []sponsorBlockCutJob{
		{kind: postprocess.ArtifactMedia, original: media, staged: stagedMedia, backup: filepath.Join(staging, "backup-0.mp4")},
		{kind: postprocess.ArtifactSubtitle, original: sub, staged: stagedSub, backup: filepath.Join(staging, "backup-1.vtt")},
	}
	if err := commitSponsorBlockCutJobs(context.Background(), jobs); err == nil {
		t.Fatal("expected commit failure")
	}
	mediaBody, err := os.ReadFile(media)
	if err != nil || string(mediaBody) != "media-original" {
		t.Fatalf("media should roll back to original: %v %q", err, mediaBody)
	}
	subBody, err := os.ReadFile(sub)
	if err != nil || string(subBody) != "sub-original" {
		t.Fatalf("subtitle should remain original: %v %q", err, subBody)
	}
}

func TestSponsorBlockCutCommitReportsRollbackFailure(t *testing.T) {
	root := t.TempDir()
	missingBackup := filepath.Join(root, "missing-backup.mp4")
	target := filepath.Join(root, "target.mp4")
	err := restoreSponsorBlockBackup(sponsorBlockCutJob{original: target, backup: missingBackup})
	if err == nil {
		t.Fatal("expected restore failure for missing backup")
	}

	blocked := filepath.Join(root, "blocked-original")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "nested"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup.mp4")
	if err := os.WriteFile(backup, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreSponsorBlockBackup(sponsorBlockCutJob{original: blocked, backup: backup}); err == nil {
		t.Fatal("expected restore failure when original is a non-empty directory")
	}

	media := filepath.Join(root, "media.mp4")
	sub := filepath.Join(root, "track.vtt")
	if err := os.WriteFile(media, []byte("media-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("sub-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	jobs := []sponsorBlockCutJob{
		{
			kind: postprocess.ArtifactMedia, original: media,
			staged: filepath.Join(staging, "0.mp4"), backup: filepath.Join(staging, "backup-0.mp4"),
		},
		{
			kind: postprocess.ArtifactSubtitle, original: sub,
			staged: filepath.Join(staging, "1.vtt"), backup: filepath.Join(staging, "backup-1.vtt"),
		},
	}
	if err := os.WriteFile(jobs[0].staged, []byte("media-staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = commitSponsorBlockCutJobs(context.Background(), jobs)
	if err == nil {
		t.Fatal("expected commit failure")
	}
	if !errors.Is(err, os.ErrNotExist) && !os.IsNotExist(err) {
		if !strings.Contains(err.Error(), "no such file") && !strings.Contains(err.Error(), "cannot find") {
			t.Fatalf("commit err = %v", err)
		}
	}
	body, readErr := os.ReadFile(media)
	if readErr != nil || string(body) != "media-original" {
		t.Fatalf("media after rollback = %v %q", readErr, body)
	}
}

func TestSponsorBlockCutCommitReplacesAfterAllStaging(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media.mp4")
	sub := filepath.Join(root, "track.vtt")
	if err := os.WriteFile(media, []byte("media-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("sub-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	jobs := []sponsorBlockCutJob{
		{
			kind: postprocess.ArtifactMedia, original: media,
			staged: filepath.Join(staging, "0.mp4"), backup: filepath.Join(staging, "backup-0.mp4"),
		},
		{
			kind: postprocess.ArtifactSubtitle, original: sub,
			staged: filepath.Join(staging, "1.vtt"), backup: filepath.Join(staging, "backup-1.vtt"),
		},
	}
	if err := os.WriteFile(jobs[0].staged, []byte("media-staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobs[1].staged, []byte("sub-staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitSponsorBlockCutJobs(context.Background(), jobs); err != nil {
		t.Fatal(err)
	}
	mediaBody, err := os.ReadFile(media)
	if err != nil || string(mediaBody) != "media-staged" {
		t.Fatalf("media = %v %q", err, mediaBody)
	}
	subBody, err := os.ReadFile(sub)
	if err != nil || string(subBody) != "sub-staged" {
		t.Fatalf("subtitle = %v %q", err, subBody)
	}
}

func TestSponsorBlockCutCommitCancellationLeavesFilesUnchanged(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media.mp4")
	sub := filepath.Join(root, "track.vtt")
	if err := os.WriteFile(media, []byte("media-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("sub-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	jobs := []sponsorBlockCutJob{
		{kind: postprocess.ArtifactMedia, original: media, staged: filepath.Join(staging, "0.mp4"), backup: filepath.Join(staging, "backup-0.mp4")},
		{kind: postprocess.ArtifactSubtitle, original: sub, staged: filepath.Join(staging, "1.vtt"), backup: filepath.Join(staging, "backup-1.vtt")},
	}
	if err := os.WriteFile(jobs[0].staged, []byte("media-staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobs[1].staged, []byte("sub-staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := commitSponsorBlockCutJobs(ctx, jobs); err == nil {
		t.Fatal("expected cancellation")
	}
	mediaBody, err := os.ReadFile(media)
	if err != nil || string(mediaBody) != "media-original" {
		t.Fatalf("media = %v %q", err, mediaBody)
	}
	subBody, err := os.ReadFile(sub)
	if err != nil || string(subBody) != "sub-original" {
		t.Fatalf("subtitle = %v %q", err, subBody)
	}
}

func TestSponsorBlockStageSubtitleDeterministic(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "track.srt")
	staged := filepath.Join(root, "staged.srt")
	input := "1\n00:00:05,000 --> 00:00:08,000\nbefore\n\n2\n00:00:12,000 --> 00:00:18,000\ninside\n"
	if err := os.WriteFile(original, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stageSponsorBlockSubtitle(original, staged, []sponsorblock.Range{{Start: 10, End: 20}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "inside") || !strings.Contains(text, "before") {
		t.Fatalf("staged = %q", text)
	}
}

func TestSponsorBlockPlanningLimitsAlignWithFFmpeg(t *testing.T) {
	if sponsorblock.MaxKeepSegments != ffmpeg.MaxConcatRanges {
		t.Fatalf("keep limit %d != ffmpeg concat %d", sponsorblock.MaxKeepSegments, ffmpeg.MaxConcatRanges)
	}
	if sponsorblock.MaxForceKeyframeTimestamps != ffmpeg.MaxForceKeyframes {
		t.Fatalf("keyframe limit %d != ffmpeg %d", sponsorblock.MaxForceKeyframeTimestamps, ffmpeg.MaxForceKeyframes)
	}
}

func TestArtifactBytesRecountsAfterCut(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media.mp4")
	sub := filepath.Join(root, "track.vtt")
	if err := os.WriteFile(media, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	total, err := artifactBytes([]Artifact{
		{Path: media, Kind: "media"},
		{Path: sub, Kind: "subtitle"},
	})
	if err != nil || total != 8 {
		t.Fatalf("bytes = %d err=%v", total, err)
	}
	if err := os.WriteFile(media, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("xy"), 0o600); err != nil {
		t.Fatal(err)
	}
	total, err = artifactBytes([]Artifact{
		{Path: media, Kind: "media"},
		{Path: sub, Kind: "subtitle"},
	})
	if err != nil || total != 3 {
		t.Fatalf("recounted bytes = %d err=%v", total, err)
	}
}
