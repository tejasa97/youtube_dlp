package ytdlp

import (
	"context"
	"os"
	"path/filepath"
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
		path, artifacts, err := operation.applySponsorBlockRemove(context.Background(), &info, "missing.mp4", nil, nil)
		if err != nil {
			t.Fatalf("request=%#v err=%v", request, err)
		}
		if path != "missing.mp4" || artifacts != nil {
			t.Fatalf("unexpected mutation path=%q artifacts=%v", path, artifacts)
		}
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
	path, _, err := operation.applySponsorBlockRemove(context.Background(), &info, media, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != media {
		t.Fatalf("path = %q", path)
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
	media := filepath.Join(root, "media.mp4")
	sub := filepath.Join(root, "track.json3")
	if err := os.WriteFile(media, []byte("media-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := value.Info{}
	info.Set("duration", value.Float(100))
	info.Set("sponsorblock_chapters", value.List(chapterValue(sponsorblock.Chapter{
		StartTime: 10, EndTime: 20, Category: "sponsor", Title: "Sponsor", Type: "skip",
	})))
	operation := &operation{request: Request{SponsorBlock: SponsorBlockOptions{
		Enabled: true, Remove: true, Categories: []string{"sponsor"},
	}}}
	_, _, err := operation.applySponsorBlockRemove(context.Background(), &info, media, []Artifact{{Path: sub, Kind: "subtitle"}}, nil)
	if err == nil || !IsCategory(err, ErrorUnsupported) {
		t.Fatalf("error = %v", err)
	}
	body, readErr := os.ReadFile(media)
	if readErr != nil || string(body) != "media-original" {
		t.Fatalf("media mutated despite prevalidation failure: %v %q", readErr, body)
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
	// Leave the second staged path missing so the first rename commits, then the
	// second commit step fails and must roll media back to the original bytes.
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

func TestSponsorBlockPlanningLimitsAlignWithFFmpeg(t *testing.T) {
	if sponsorblock.MaxKeepSegments != ffmpeg.MaxConcatRanges {
		t.Fatalf("keep limit %d != ffmpeg concat %d", sponsorblock.MaxKeepSegments, ffmpeg.MaxConcatRanges)
	}
	if sponsorblock.MaxForceKeyframeTimestamps != ffmpeg.MaxForceKeyframes {
		t.Fatalf("keyframe limit %d != ffmpeg %d", sponsorblock.MaxForceKeyframeTimestamps, ffmpeg.MaxForceKeyframes)
	}
}
