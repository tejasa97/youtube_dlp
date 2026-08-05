package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestEmbedSelectedThumbnailConversionAndOwnership(t *testing.T) {
	root := t.TempDir()
	media := writeEmbedFixture(t, root, "media.mp4", "media")
	small := writeEmbedFixture(t, root, "small.jpg", "small")
	best := writeEmbedFixture(t, root, "best.webp", "best")
	info := thumbnailEmbedInfo(small, best)
	artifacts := []Artifact{
		{Path: small, Kind: "thumbnail"},
		{Path: best, Kind: "thumbnail"},
		{Path: media, Kind: "media"},
	}
	var converted, embedded string
	operation := operation{
		client: NewClient(),
		request: Request{
			OutputDir: root,
			Thumbnails: ThumbnailOptions{
				Embed: true,
			},
		},
		thumbnailConvert: func(
			_ context.Context, source, destination, format string, overwrite bool, _ events.Sink,
		) error {
			converted = filepath.Base(source) + ">" + filepath.Base(destination) + ":" + format
			if overwrite {
				t.Fatal("temporary conversion allowed overwrite")
			}
			return os.WriteFile(destination, []byte("png"), 0o600)
		},
		thumbnailEmbed: func(_ context.Context, gotMedia, image string, _ events.Sink) error {
			if gotMedia != media {
				t.Fatalf("media = %q", gotMedia)
			}
			embedded = filepath.Base(image)
			return os.WriteFile(gotMedia, []byte("embedded"), 0o600)
		},
	}
	result, changed, err := operation.embedSelectedThumbnail(
		context.Background(), &info, media, artifacts, nil,
	)
	if err != nil || !changed {
		t.Fatalf("changed=%t error=%v", changed, err)
	}
	if converted != "best.webp>best.embed.png:png" || embedded != "best.embed.png" {
		t.Fatalf("converted=%q embedded=%q", converted, embedded)
	}
	if _, err := os.Stat(best); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("implicit source remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "best.embed.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary image remains: %v", err)
	}
	if len(result) != 2 || result[0].Path != small || result[1].Path != media {
		t.Fatalf("artifacts = %#v", result)
	}
	thumbnails, _ := info.Lookup("thumbnails").ListValue()
	bestMetadata, _ := thumbnails[1].Object()
	if embedded, _ := bestMetadata.Lookup("embedded").Bool(); !embedded {
		t.Fatal("best thumbnail metadata was not marked embedded")
	}
}

func TestEmbedSelectedThumbnailExplicitRetentionAndCleanupFailure(t *testing.T) {
	for _, test := range []struct {
		name        string
		keep        bool
		cleanupFail bool
	}{
		{"explicit retention", true, false},
		{"cleanup failure", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			media := writeEmbedFixture(t, root, "media.mp3", "media")
			thumbnail := writeEmbedFixture(t, root, "cover.jpg", "image")
			info := thumbnailEmbedInfo(thumbnail)
			var warnings int
			operation := operation{
				client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
					if event.Kind == EventMetadataWarning {
						warnings++
						return errors.New("observer failure")
					}
					return nil
				})),
				request: Request{
					OutputDir: root,
					Thumbnails: ThumbnailOptions{
						Embed: true, KeepFiles: test.keep,
					},
				},
				thumbnailEmbed: func(context.Context, string, string, events.Sink) error { return nil },
			}
			if test.cleanupFail {
				operation.removeFile = func(string) error { return errors.New("cleanup failure") }
			}
			artifacts, changed, err := operation.embedSelectedThumbnail(
				context.Background(), &info, media,
				[]Artifact{{Path: thumbnail, Kind: "thumbnail"}, {Path: media, Kind: "media"}}, nil,
			)
			if err != nil || !changed {
				t.Fatalf("changed=%t error=%v", changed, err)
			}
			if len(artifacts) != 2 || warnings != boolInt(test.cleanupFail) {
				t.Fatalf("artifacts=%#v warnings=%d", artifacts, warnings)
			}
			if _, err := os.Stat(thumbnail); err != nil {
				t.Fatalf("retained thumbnail: %v", err)
			}
		})
	}
}

func TestEmbedSelectedThumbnailFailureCancellationAndWarnings(t *testing.T) {
	root := t.TempDir()
	media := writeEmbedFixture(t, root, "media.mp4", "media")
	thumbnail := writeEmbedFixture(t, root, "cover.png", "image")
	info := thumbnailEmbedInfo(thumbnail)
	operation := operation{
		client: NewClient(),
		request: Request{
			OutputDir: root, Thumbnails: ThumbnailOptions{Embed: true},
		},
		thumbnailEmbed: func(context.Context, string, string, events.Sink) error {
			return context.Canceled
		},
	}
	artifacts := []Artifact{{Path: thumbnail, Kind: "thumbnail"}, {Path: media, Kind: "media"}}
	got, changed, err := operation.embedSelectedThumbnail(context.Background(), &info, media, artifacts, nil)
	if !errors.Is(err, context.Canceled) || changed || len(got) != 2 ||
		!IsCategory(categorized("embed thumbnail", err), ErrorCancelled) {
		t.Fatalf("artifacts=%#v changed=%t error=%v", got, changed, err)
	}
	if _, err := os.Stat(media); err != nil {
		t.Fatalf("media was not preserved: %v", err)
	}
	if _, err := os.Stat(thumbnail); err != nil {
		t.Fatalf("thumbnail was not preserved: %v", err)
	}

	var warnings int
	operation.client = NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warnings++
		}
		return nil
	}))
	empty := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String("empty")}))
	if got, changed, err := operation.embedSelectedThumbnail(context.Background(), &empty, media, artifacts, nil); err != nil || changed || len(got) != 2 {
		t.Fatalf("empty artifacts=%#v changed=%t error=%v", got, changed, err)
	}
	if warnings != 1 {
		t.Fatalf("warnings = %d", warnings)
	}

	operation.request.Thumbnails.Embed = true
	unsupported := strings.TrimSuffix(media, ".mp4") + ".webm"
	if err := os.Rename(media, unsupported); err != nil {
		t.Fatal(err)
	}
	if _, _, err := operation.embedSelectedThumbnail(context.Background(), &info, unsupported, artifacts, nil); !errors.Is(err, ffmpeg.ErrInvalidOperation) {
		t.Fatalf("unsupported container = %v", err)
	}
}

func TestValidateMultiOutputAllowsThumbnailEmbedding(t *testing.T) {
	err := validateMultiOutputProduct(Request{
		Thumbnails: ThumbnailOptions{Embed: true},
	}, 2)
	if err != nil {
		t.Fatalf("thumbnail embedding with multi-output = %v", err)
	}
}

func TestThumbnailEmbeddingPromotesOnlyMergedWebM(t *testing.T) {
	pair := []mediaformat.Selection{
		{Ext: "webm", VCodec: "vp9", ACodec: "none"},
		{Ext: "webm", VCodec: "none", ACodec: "opus"},
	}
	if got := thumbnailEmbeddingOutputExtension(Request{
		Thumbnails: ThumbnailOptions{Embed: true},
	}, pair, "webm"); got != "mkv" {
		t.Fatalf("embedded WebM pair extension=%q", got)
	}
	if got := thumbnailEmbeddingOutputExtension(Request{
		Thumbnails: ThumbnailOptions{Embed: true}, MergeOutputFormat: "webm",
	}, pair, "webm"); got != "webm" {
		t.Fatalf("explicit webm extension=%q", got)
	}
	if got := thumbnailEmbeddingOutputExtension(Request{
		Thumbnails: ThumbnailOptions{Embed: true}, MergeOutputFormat: "mkv",
	}, pair, "webm"); got != "webm" {
		t.Fatalf("explicit mkv does not auto-promote from %q", got)
	}
	if got := thumbnailEmbeddingOutputExtension(Request{}, pair, "webm"); got != "webm" {
		t.Fatalf("plain WebM pair extension=%q", got)
	}
	if got := thumbnailEmbeddingOutputExtension(Request{
		Thumbnails: ThumbnailOptions{Embed: true},
	}, pair[:1], "webm"); got != "webm" {
		t.Fatalf("single WebM extension=%q", got)
	}
	request := Request{Thumbnails: ThumbnailOptions{Embed: true}}
	withThumbnail := value.NewInfo(value.NewObject(value.Field{Key: "thumbnail", Value: value.String("https://example.com/t.jpg")}))
	for input, want := range map[string]string{
		"/tmp/fixed.webm":  "/tmp/fixed.mkv",
		"/tmp/fixed.mkv":   "/tmp/fixed.mkv",
		"/tmp/fixed":       "/tmp/fixed.mkv",
		"/tmp/fixed.other": "/tmp/fixed.other.mkv",
	} {
		if got := thumbnailEmbeddingDestination(request, pair, input, withThumbnail); got != want {
			t.Fatalf("destination(%q)=%q want=%q", input, got, want)
		}
	}
	if got := thumbnailEmbeddingDestination(Request{}, pair, "/tmp/fixed.webm", withThumbnail); got != "/tmp/fixed.webm" {
		t.Fatalf("plain destination=%q", got)
	}
	if got := thumbnailEmbeddingDestination(request, pair, "/tmp/fixed.webm", value.NewInfo(value.NewObject())); got != "/tmp/fixed.webm" {
		t.Fatalf("no-thumbnail destination=%q", got)
	}
}

func thumbnailEmbedInfo(paths ...string) value.Info {
	thumbnails := make([]value.Value, 0, len(paths))
	for index, path := range paths {
		thumbnails = append(thumbnails, value.ObjectValue(value.NewObject(
			value.Field{Key: "id", Value: value.String(string(rune('a' + index)))},
			value.Field{Key: "filepath", Value: value.String(path)},
			value.Field{Key: "ext", Value: value.String(strings.TrimPrefix(filepath.Ext(path), "."))},
		)))
	}
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "thumbnails", Value: value.List(thumbnails...)},
	))
}

func writeEmbedFixture(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
