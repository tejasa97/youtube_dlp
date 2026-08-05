package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	ConcatPlaylistNever      = "never"
	ConcatPlaylistAlways     = "always"
	ConcatPlaylistMultiVideo = "multi_video"
	maxConcatPlaylistInputs  = 128
)

func validConcatPlaylist(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", ConcatPlaylistNever, ConcatPlaylistAlways, ConcatPlaylistMultiVideo:
		return true
	default:
		return false
	}
}

func (operation *operation) concatPlaylist(
	ctx context.Context, playlistInfo value.Info, children []Result, expectedEntries int,
) (Artifact, error) {
	policy := strings.ToLower(strings.TrimSpace(operation.request.ConcatPlaylist))
	if policy == "" || policy == ConcatPlaylistNever || operation.request.Simulate || operation.request.SkipDownload {
		return Artifact{}, nil
	}
	if expectedEntries != len(children) {
		return Artifact{}, fmt.Errorf("%w: concat requires every selected playlist entry", ffmpeg.ErrInvalidOperation)
	}
	if len(children) < 2 {
		return Artifact{}, nil
	}
	if len(children) > maxConcatPlaylistInputs {
		return Artifact{}, fmt.Errorf("%w: concat input limit exceeded", ffmpeg.ErrInvalidOperation)
	}
	paths := make([]string, len(children))
	for index, child := range children {
		path := filepath.Clean(child.Filename)
		if path == "" || path == "." || path == "-" {
			return Artifact{}, fmt.Errorf("%w: playlist entry %d has no media path", ffmpeg.ErrInvalidOperation, index+1)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return Artifact{}, fmt.Errorf("%w: inspect playlist entry %d: %v", ffmpeg.ErrMediaFailure, index+1, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return Artifact{}, fmt.Errorf("%w: playlist entry %d is not a regular file", ffmpeg.ErrInvalidOperation, index+1)
		}
		paths[index] = path
	}
	if policy == ConcatPlaylistMultiVideo && !playlistCanConcat(playlistInfo, len(children)) {
		return Artifact{}, nil
	}
	tools, err := operation.discoverFFmpeg()
	if err != nil {
		return Artifact{}, err
	}
	signature, extension, err := compatibleConcatInputs(ctx, tools, paths)
	if err != nil {
		return Artifact{}, err
	}
	_ = signature
	outputInfo := value.NewInfo(playlistInfo.Fields().Clone())
	if title, ok := playlistInfo.Title(); ok {
		outputInfo.Set("playlist_title", value.String(title))
	}
	if id, ok := playlistInfo.ID(); ok {
		outputInfo.Set("playlist_id", value.String(id))
	}
	outputInfo.Set("ext", value.String(extension))
	outputInfo.Set("playlist_index", value.Int(1))
	destination, err := operation.resolveOutputPath(
		operation.request.outputRoot(OutputPathPLVideo),
		operation.request.outputTemplate(OutputTemplatePLVideo), outputInfo,
	)
	if err != nil {
		return Artifact{}, fmt.Errorf("render playlist concat path: %w", err)
	}
	destination = filepath.Clean(destination)
	if _, err := confinedPostprocessPath(operation.request.outputRoot(OutputPathHome), destination); err != nil {
		return Artifact{}, fmt.Errorf("confine playlist concat destination: %w", err)
	}
	for _, path := range paths {
		if portablePathKey(path) == portablePathKey(destination) {
			return Artifact{}, fmt.Errorf("%w: concat destination collides with an input", ffmpeg.ErrInvalidOperation)
		}
	}
	if err := tools.Concat(ctx, paths, destination, operation.request.postprocessorOverwrites(), operation.eventSink()); err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: destination, Kind: "playlist_media"}, nil
}

func playlistCanConcat(info value.Info, count int) bool {
	if count < 2 {
		return false
	}
	if marker, ok := info.Lookup("_type").StringValue(); ok && strings.EqualFold(marker, "multi_video") {
		return true
	}
	marker, ok := info.Lookup("multi_video").Bool()
	return ok && marker
}

type concatStreamSignature struct {
	codecType string
	codecName string
	width     int
	height    int
}

func compatibleConcatInputs(ctx context.Context, tools *ffmpeg.Toolset, paths []string) ([]concatStreamSignature, string, error) {
	var baseline []concatStreamSignature
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(paths[0])), ".")
	for index, path := range paths {
		probe, err := tools.Probe(ctx, path)
		if err != nil {
			return nil, "", fmt.Errorf("%w: inspect concat input %d", ffmpeg.ErrMediaFailure, index+1)
		}
		if len(probe.Streams) == 0 {
			return nil, "", fmt.Errorf("%w: concat input %d has no streams", ffmpeg.ErrInvalidOperation, index+1)
		}
		current := make([]concatStreamSignature, len(probe.Streams))
		for streamIndex, stream := range probe.Streams {
			current[streamIndex] = concatStreamSignature{
				codecType: stream.CodecType, codecName: stream.CodecName,
				width: stream.Width, height: stream.Height,
			}
		}
		if index == 0 {
			baseline = current
			continue
		}
		if len(current) != len(baseline) {
			return nil, "", fmt.Errorf("%w: concat inputs have different stream counts", ffmpeg.ErrInvalidOperation)
		}
		for streamIndex := range baseline {
			if current[streamIndex] != baseline[streamIndex] {
				return nil, "", fmt.Errorf("%w: concat inputs differ at stream %d", ffmpeg.ErrInvalidOperation, streamIndex)
			}
		}
		if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."); ext != extension {
			extension = "mkv"
		}
	}
	return baseline, extension, nil
}
