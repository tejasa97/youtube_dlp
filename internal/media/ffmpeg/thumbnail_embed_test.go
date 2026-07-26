package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := filepath.Join(root, "input."+test.extension)
			args := append([]string{"-nostdin", "-y"}, test.generate...)
			args = append(args, input)
			if _, err := tools.execute(ctx, tools.ffmpeg, args, nil); err != nil {
				t.Fatalf("generate %s: %v", test.name, err)
			}
			output := filepath.Join(root, "output."+test.extension)
			if err := tools.EmbedThumbnail(ctx, input, image, output, false, nil); err != nil {
				t.Fatal(err)
			}
			probe, err := tools.Probe(ctx, output)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, stream := range probe.Streams {
				if stream.CodecType != test.wantKind {
					continue
				}
				if stream.Disposition["attached_pic"] == 1 {
					found = true
				}
			}
			if !found {
				t.Fatalf("no embedded thumbnail in streams %#v", probe.Streams)
			}
		})
	}
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
