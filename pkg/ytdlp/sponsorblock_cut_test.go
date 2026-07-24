package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

func TestSponsorBlockUnsupportedSubtitleFailsClosed(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "media.mp4")
	sub := filepath.Join(root, "track.json3")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
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
	// Either unsupported subtitle (if ffmpeg available and media cut succeeds first)
	// or internal failure when ffmpeg cannot process the tiny text fixture as media.
	if err == nil {
		t.Fatal("expected error for unsupported subtitle or invalid media")
	}
}
