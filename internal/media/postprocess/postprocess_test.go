package postprocess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
)

func TestGraphValidationAndSafeMove(t *testing.T) {
	if err := (Graph{}).Validate(); !errors.Is(err, ErrInvalidGraph) {
		t.Fatalf("empty graph: %v", err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	destination := filepath.Join(root, "nested", "destination.mp4")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := Move{Input: Artifact{Path: source, Kind: ArtifactMedia, Owned: true}, Output: Artifact{Path: destination, Kind: ArtifactMedia}}
	if err := operation.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "content" {
		t.Fatalf("moved content=%q err=%v", got, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	if err := SafeMove(destination, destination, false); err != nil {
		t.Fatalf("identity move: %v", err)
	}
	predictable := filepath.Join(root, "final.mp4.move.part")
	if err := os.WriteFile(predictable, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Graph{Operations: []Operation{Move{Input: Artifact{Path: destination, Kind: ArtifactMedia}, Output: Artifact{Path: filepath.Join(root, "final.mp4"), Kind: ArtifactMedia}}}}).Run(context.Background(), nil, nil); err != nil {
		t.Fatalf("move-only graph should not need ffmpeg: %v", err)
	}
	if body, err := os.ReadFile(predictable); err != nil || string(body) != "must survive" {
		t.Fatalf("predictable temporary was changed: %q %v", body, err)
	}
}

func TestArtifactAndDestinationFailures(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.mp4")
	if err := SafeMove(missing, filepath.Join(root, "target.mp4"), false); !errors.Is(err, ErrMissingArtifact) {
		t.Fatalf("missing source: %v", err)
	}
	if err := (AudioExtract{Input: Artifact{Path: "", Kind: ArtifactMedia}, Output: Artifact{Path: "x.mp3", Kind: ArtifactMedia}}).Run(context.Background(), nil, nil); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("empty path: %v", err)
	}
	media := filepath.Join(root, "input.mp4")
	if err := os.WriteFile(media, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (SubtitleEmbed{Input: Artifact{Path: media, Kind: ArtifactMedia}, Subtitle: Artifact{Path: "caption.vtt", Kind: ArtifactMedia}, Output: Artifact{Path: "out.mp4", Kind: ArtifactMedia}}).Run(context.Background(), nil, nil); !errors.Is(err, ErrInvalidGraph) {
		t.Fatalf("kind mismatch: %v", err)
	}
	subtitle := filepath.Join(root, "caption.vtt")
	if err := os.WriteFile(subtitle, []byte("WEBVTT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (SubtitleEmbed{
		Input:     Artifact{Path: media, Kind: ArtifactMedia},
		Subtitle:  Artifact{Path: subtitle, Kind: ArtifactSubtitle},
		Subtitles: []SubtitleEmbedInput{{Artifact: Artifact{Path: subtitle, Kind: ArtifactSubtitle}, Extension: "vtt"}},
		Output:    Artifact{Path: filepath.Join(root, "both.mp4"), Kind: ArtifactMedia},
	}).Run(context.Background(), nil, nil); !errors.Is(err, ErrInvalidGraph) {
		t.Fatalf("single and multiple subtitle inputs: %v", err)
	}
	tooMany := make([]SubtitleEmbedInput, 65)
	if err := (SubtitleEmbed{
		Input:     Artifact{Path: media, Kind: ArtifactMedia},
		Subtitles: tooMany,
		Output:    Artifact{Path: filepath.Join(root, "too-many.mp4"), Kind: ArtifactMedia},
	}).Run(context.Background(), nil, nil); !errors.Is(err, ErrInvalidGraph) {
		t.Fatalf("subtitle input limit: %v", err)
	}
	if err := SafeMove("nul\x00path", "output", false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("nul path: %v", err)
	}
	regular := filepath.Join(root, "regular.mp4")
	if err := os.WriteFile(regular, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "link.mp4")
	if err := os.Symlink(regular, symlink); err == nil {
		if err := SafeMove(symlink, filepath.Join(root, "result.mp4"), false); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("symlink source: %v", err)
		}
		if err := SafeMove(regular, symlink, true); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("symlink destination: %v", err)
		}
	}
	cancelled := filepath.Join(root, "cancelled.mp4")
	if err := os.WriteFile(cancelled, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SafeMoveContext(ctx, cancelled, filepath.Join(root, "not-moved.mp4"), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled move: %v", err)
	}
	_ = ffmpeg.FixupNone
}

func TestSubtitleEmbedFailurePreservesOwnedInputs(t *testing.T) {
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	root := t.TempDir()
	media := filepath.Join(root, "invalid.mp4")
	subtitle := filepath.Join(root, "caption.vtt")
	output := filepath.Join(root, "output.mp4")
	if err := os.WriteFile(media, []byte("not media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitle, []byte("WEBVTT\n\n00:00.000 --> 00:00.100\nhello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := SubtitleEmbed{
		Input: Artifact{Path: media, Kind: ArtifactMedia, Owned: true},
		Subtitles: []SubtitleEmbedInput{{
			Artifact: Artifact{Path: subtitle, Kind: ArtifactSubtitle, Owned: true},
			Language: "eng", Extension: "vtt",
		}},
		Output: Artifact{Path: output, Kind: ArtifactMedia},
	}
	if err := operation.Run(context.Background(), tools, nil); !errors.Is(err, ffmpeg.ErrMediaFailure) {
		t.Fatalf("error = %v", err)
	}
	for _, path := range []string{media, subtitle} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("owned input %q changed: %v", path, statErr)
		}
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed output exists: %v", statErr)
	}
}

func FuzzArtifactPaths(f *testing.F) {
	f.Add("input.mp4", "output.mp4")
	f.Fuzz(func(t *testing.T, input, output string) {
		if len(input)+len(output) > 4096 {
			t.Skip()
		}
		_ = validateTransform(Artifact{Path: input, Kind: ArtifactMedia}, Artifact{Path: output, Kind: ArtifactMedia}, ArtifactMedia, ArtifactMedia)
	})
}
