package ffmpeg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/events"
)

func TestEmbedThumbnailSupportedContainers(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	image := filepath.Join(root, "cover.png")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=0.1",
		"-frames:v", "1", image,
	}, nil); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, extension string
		generate        []string
		wantKind        string
	}{
		{
			name: "mp4", extension: "mp4", wantKind: "video",
			generate: []string{
				"-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.3",
				"-f", "lavfi", "-i", "sine=frequency=440:duration=0.3",
				"-shortest", "-c:v", "mpeg4", "-c:a", "aac",
			},
		},
		{
			name: "mp3", extension: "mp3", wantKind: "video",
			generate: []string{
				"-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "libmp3lame",
			},
		},
		{
			name: "mkv", extension: "mkv", wantKind: "video",
			generate: []string{
				"-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "flac",
			},
		},
		{
			name: "flac", extension: "flac", wantKind: "video",
			generate: []string{
				"-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "flac",
			},
		},
		{
			name: "ogg", extension: "ogg", wantKind: "video",
			generate: []string{
				"-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "libopus",
			},
		},
		{
			name: "opus", extension: "opus", wantKind: "video",
			generate: []string{
				"-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "libopus",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := filepath.Join(root, "input."+test.extension)
			args := append([]string{"-nostdin", "-y"}, test.generate...)
			args = append(args, "-metadata", "title=keep me", input)
			if _, err := tools.execute(ctx, tools.ffmpeg, args, nil); err != nil {
				t.Fatalf("generate %s: %v", test.name, err)
			}
			wantTime := time.Unix(1_700_000_000, 0)
			if err := os.Chtimes(input, wantTime, wantTime); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "output."+test.extension)
			if err := tools.EmbedThumbnail(ctx, input, image, output, false, nil); err != nil {
				t.Fatal(err)
			}
			replaced := filepath.Join(root, "replaced."+test.extension)
			if err := tools.EmbedThumbnail(ctx, output, image, replaced, false, nil); err != nil {
				t.Fatalf("replace existing cover: %v", err)
			}
			output = replaced
			probe, err := tools.Probe(ctx, output)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, stream := range probe.Streams {
				if stream.CodecType != test.wantKind {
					continue
				}
				if stream.Disposition["attached_pic"] == 1 {
					found++
				}
			}
			if found != 1 {
				t.Fatalf("embedded thumbnails=%d streams=%#v", found, probe.Streams)
			}
			title := probe.Format.Tags["title"]
			if title == "" {
				title = probe.Format.Tags["TITLE"]
			}
			for _, stream := range probe.Streams {
				if title == "" {
					title = stream.Tags["title"]
				}
				if title == "" {
					title = stream.Tags["TITLE"]
				}
			}
			if title != "keep me" {
				t.Fatalf("title=%q tags=%#v", title, probe.Format.Tags)
			}
			info, err := os.Stat(output)
			if err != nil {
				t.Fatal(err)
			}
			if !info.ModTime().Equal(wantTime) {
				t.Fatalf("mtime=%s want=%s", info.ModTime(), wantTime)
			}
		})
	}
}

func FuzzEncodeFLACPicture(f *testing.F) {
	png, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
	)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(png)
	f.Add([]byte("not an image"))
	f.Fuzz(func(t *testing.T, input []byte) {
		picture, err := encodeFLACPictureData(input)
		if err != nil {
			return
		}
		if len(picture) < 32 || binary.BigEndian.Uint32(picture[:4]) != 3 {
			t.Fatalf("invalid picture block=%x", picture)
		}
		dataLengthOffset := len(picture) - len(input) - 4
		if dataLengthOffset < 0 ||
			int(binary.BigEndian.Uint32(picture[dataLengthOffset:dataLengthOffset+4])) != len(input) {
			t.Fatalf("picture data length mismatch")
		}
	})
}

func TestEmbedThumbnailValidatesInputsAndContainers(t *testing.T) {
	tools := requireToolset(t)
	root := t.TempDir()
	media := filepath.Join(root, "media.webm")
	image := filepath.Join(root, "cover.png")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tools.EmbedThumbnail(context.Background(), media, image, media, true, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("unsupported container = %v", err)
	}
	symlink := filepath.Join(root, "cover-link.png")
	if err := os.Symlink(image, symlink); err == nil {
		if err := tools.EmbedThumbnail(context.Background(), media, symlink, filepath.Join(root, "output.mp4"), false, nil); !errors.Is(err, ErrInvalidOperation) {
			t.Fatalf("symlink input = %v", err)
		}
	}
}

func TestEmbedThumbnailMatroskaCrossMIMEAndUnrelatedAttachment(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	audio := filepath.Join(root, "audio.mka")
	notes := filepath.Join(root, "notes.txt")
	input := filepath.Join(root, "input.mka")
	png := filepath.Join(root, "cover.png")
	jpg := filepath.Join(root, "cover.jpg")
	if err := os.WriteFile(notes, []byte("keep this attachment"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3",
			"-c:a", "flac", "-metadata:s:a:0", "MIMETYPE=image/png", audio},
		{"-nostdin", "-y", "-i", audio, "-map", "0", "-c", "copy", "-attach", notes,
			"-metadata:s:t:0", "mimetype=text/plain", "-metadata:s:t:0", "filename=notes.txt", input},
		{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=0.1", "-frames:v", "1", png},
		{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=red:s=32x32:d=0.1", "-frames:v", "1", jpg},
	}
	for _, args := range commands {
		if _, err := tools.execute(ctx, tools.ffmpeg, args, nil); err != nil {
			t.Fatal(err)
		}
	}
	inputProbe, err := tools.Probe(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	audioMIME := false
	for _, stream := range inputProbe.Streams {
		if stream.CodecType == "audio" &&
			strings.EqualFold(metadataValueFold(stream.Tags, "mimetype"), "image/png") {
			audioMIME = true
		}
	}
	if !audioMIME {
		t.Fatalf("fixture lacks hostile audio MIME tag: %#v", inputProbe.Streams)
	}
	withPNG := filepath.Join(root, "with-png.mka")
	if err := tools.EmbedThumbnail(ctx, input, png, withPNG, false, nil); err != nil {
		t.Fatal(err)
	}
	withBoth := filepath.Join(root, "with-both.mka")
	if err := tools.EmbedThumbnail(ctx, withPNG, jpg, withBoth, false, nil); err != nil {
		t.Fatal(err)
	}
	replacedJPG := filepath.Join(root, "replaced-jpg.mka")
	if err := tools.EmbedThumbnail(ctx, withBoth, jpg, replacedJPG, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, replacedJPG)
	if err != nil {
		t.Fatal(err)
	}
	var pictures, textAttachments, audioStreams int
	for _, stream := range probe.Streams {
		if stream.CodecType == "audio" {
			audioStreams++
		}
		if stream.CodecType != "audio" &&
			strings.HasPrefix(strings.ToLower(metadataValueFold(stream.Tags, "mimetype")), "image/") {
			pictures++
		}
		if stream.CodecType == "attachment" &&
			strings.EqualFold(metadataValueFold(stream.Tags, "mimetype"), "text/plain") {
			textAttachments++
		}
	}
	if pictures != 2 || textAttachments != 1 || audioStreams != 1 {
		t.Fatalf("pictures=%d text=%d audio=%d streams=%#v", pictures, textAttachments, audioStreams, probe.Streams)
	}
}

func TestEncodeFLACPictureBoundsAndType(t *testing.T) {
	tools := requireToolset(t)
	root := t.TempDir()
	image := filepath.Join(root, "cover.png")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=red:s=17x19:d=0.1",
		"-frames:v", "1", image,
	}, nil); err != nil {
		t.Fatal(err)
	}
	picture, err := encodeFLACPicture(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(picture)
	if len(picture) < 32 || !strings.Contains(encoded, "image/png") {
		t.Fatalf("picture block=%x", picture)
	}
	invalid := filepath.Join(root, "cover.txt")
	if err := os.WriteFile(invalid, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := encodeFLACPicture(context.Background(), invalid); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("invalid image=%v", err)
	}
}

func TestXiphThumbnailCancellationAndTemporaryCleanup(t *testing.T) {
	tools := requireToolset(t)
	root := t.TempDir()
	input := filepath.Join(root, "input.opus")
	image := filepath.Join(root, "cover.png")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, args := range [][]string{
		{"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "libopus", input},
		{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=0.1", "-frames:v", "1", image},
	} {
		if _, err := tools.execute(ctx, tools.ffmpeg, args, nil); err != nil {
			t.Fatal(err)
		}
	}
	picture, err := encodeFLACPicture(ctx, image)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := writeThumbnailMetadata(
		cancelled, filepath.Join(root, "cancelled.opus"), Metadata{"title": "keep"}, picture,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled metadata write=%v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, ".ytdlp-thumbnail-*.ffmetadata")); len(matches) != 0 {
		t.Fatalf("cancelled metadata files=%#v", matches)
	}
	original, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "output.opus")
	sinkFailure := errors.New("sink rejected start")
	err = tools.EmbedThumbnail(context.Background(), input, image, destination, false, events.SinkFunc(
		func(_ context.Context, event events.Event) error {
			if event.Kind == events.KindPostprocessStarting {
				return sinkFailure
			}
			return nil
		},
	))
	if !errors.Is(err, sinkFailure) {
		t.Fatalf("sink failure=%v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after failure: %v", err)
	}
	after, err := os.ReadFile(input)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("source changed error=%v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, ".ytdlp-thumbnail-*.ffmetadata")); len(matches) != 0 {
		t.Fatalf("failed metadata files=%#v", matches)
	}
}
