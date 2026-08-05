package ytdlp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
)

const (
	FixupNever        = "never"
	FixupIgnore       = "ignore"
	FixupWarn         = "warn"
	FixupDetectOrWarn = "detect_or_warn"
	FixupForce        = "force"
)

func validFixupPolicy(policy string) bool {
	if policy == "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case FixupNever, FixupIgnore, FixupWarn, FixupDetectOrWarn, FixupForce:
		return true
	default:
		return false
	}
}

func automaticFixupEnabled(policy string) bool {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case FixupWarn, FixupDetectOrWarn, FixupForce:
		return true
	default:
		return false
	}
}

func (operation *operation) applyFixupPolicy(ctx context.Context, mediaPath string, sink events.Sink) (string, error) {
	policy := strings.ToLower(strings.TrimSpace(operation.request.FixupPolicy))
	if policy == "" || policy == FixupNever || policy == FixupIgnore {
		return mediaPath, nil
	}
	tools, err := operation.discoverFFmpeg()
	if err != nil {
		if policy == FixupWarn || policy == FixupDetectOrWarn {
			return mediaPath, operation.emitFixupWarning(ctx, "ffmpeg is unavailable for automatic fixup")
		}
		return mediaPath, err
	}
	probe, err := tools.Probe(ctx, mediaPath)
	if err != nil {
		if policy == FixupForce {
			return mediaPath, err
		}
		return mediaPath, operation.emitFixupWarning(ctx, "could not inspect media for automatic fixup")
	}
	kind := detectFixupKind(mediaPath, probe)
	if kind == ffmpeg.FixupNone {
		if policy == FixupForce {
			return mediaPath, fmt.Errorf("%w: no supported fixup detected for %s", ffmpeg.ErrInvalidOperation, filepath.Base(mediaPath))
		}
		return mediaPath, nil
	}
	if policy == FixupWarn {
		return mediaPath, operation.emitFixupWarning(ctx, "known media fixup available: "+string(kind))
	}
	if err := operation.snapshotTransactionRemovedPath(ctx, mediaPath); err != nil {
		return mediaPath, fmt.Errorf("snapshot media before fixup: %w", err)
	}
	if err := (ffmpegFixupRunner{tools: tools}).run(ctx, mediaPath, kind, sink); err != nil {
		return mediaPath, err
	}
	return mediaPath, nil
}

type ffmpegFixupRunner struct{ tools *ffmpeg.Toolset }

func (runner ffmpegFixupRunner) run(ctx context.Context, path string, kind ffmpeg.Fixup, sink events.Sink) error {
	return runner.tools.ApplyFixup(ctx, path, path, kind, true, sink)
}

func detectFixupKind(path string, probe ffmpeg.Probe) ffmpeg.Fixup {
	format := strings.ToLower(probe.Format.FormatName)
	if strings.Contains(format, "mpegts") || strings.EqualFold(filepath.Ext(path), ".ts") {
		return ffmpeg.FixupMPEGTS
	}
	if strings.EqualFold(filepath.Ext(path), ".m4a") {
		for _, stream := range probe.Streams {
			if stream.CodecType == "audio" && strings.EqualFold(stream.CodecName, "aac") {
				return ffmpeg.FixupM4AAudio
			}
		}
	}
	return ffmpeg.FixupNone
}

func (operation *operation) emitFixupWarning(ctx context.Context, message string) error {
	if operation.client == nil {
		return nil
	}
	return operation.client.emit(ctx, Event{Kind: EventMetadataWarning, Message: message})
}
