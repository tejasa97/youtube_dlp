package ytdlp

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const maximumEmbeddedMetadataValue = 8192

var metadataEmbeddingContainers = map[string]bool{
	"flac": true, "m4a": true, "mka": true, "mkv": true, "mov": true,
	"mp3": true, "mp4": true, "ogg": true, "opus": true, "webm": true,
}

func (request Request) embedsChapters() bool {
	if request.EmbedChapters != nil {
		return *request.EmbedChapters
	}
	return request.EmbedMetadata || request.SponsorBlock.Mark
}

func validateMetadataEmbeddingContainer(path string, request Request) error {
	if !request.EmbedMetadata && !request.embedsChapters() {
		return nil
	}
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if !metadataEmbeddingContainers[container] {
		return fmt.Errorf("%w: metadata and chapters cannot be embedded in container %q", ffmpeg.ErrInvalidOperation, container)
	}
	return nil
}

// applyAutomaticMetadataEmbedding mirrors the pinned FFmpegMetadata stage. It
// runs after conversion, subtitle embedding, and ModifyChapters so the final
// container receives the post-cut chapter timeline and canonical metadata.
func (operation *operation) applyAutomaticMetadataEmbedding(
	ctx context.Context, info value.Info, mediaPath string, sink events.Sink,
) (bool, error) {
	addMetadata := operation.request.EmbedMetadata
	addChapters := operation.request.embedsChapters()
	if !addMetadata && !addChapters {
		return false, nil
	}
	if err := validateMetadataEmbeddingContainer(mediaPath, operation.request); err != nil {
		return false, err
	}
	metadata := canonicalEmbeddedMetadata(info)
	chapters, err := canonicalEmbeddedChapters(info)
	if err != nil {
		return false, err
	}
	if !addMetadata {
		metadata = nil
	}
	if !addChapters {
		chapters = nil
	}
	if len(metadata) == 0 && len(chapters) == 0 {
		if err := operation.client.emit(ctx, Event{Kind: EventMetadataWarning, Message: "there is no metadata or chapter information to embed"}); err != nil {
			return false, err
		}
		return false, nil
	}
	tools, err := operation.discoverFFmpeg()
	if err != nil {
		return false, err
	}
	if len(chapters) > 0 && len(metadata) > 0 {
		if err := tools.EmbedMetadataAndChapters(ctx, mediaPath, mediaPath, metadata, chapters, true, sink); err != nil {
			return false, err
		}
		return true, nil
	}
	if len(chapters) > 0 {
		if err := tools.EmbedChapters(ctx, mediaPath, mediaPath, chapters, true, sink); err != nil {
			return false, err
		}
	}
	if len(metadata) > 0 {
		if err := tools.EmbedMetadata(ctx, mediaPath, mediaPath, metadata, true, sink); err != nil {
			return false, err
		}
	}
	return true, nil
}

func canonicalEmbeddedMetadata(info value.Info) ffmpeg.Metadata {
	metadata := make(ffmpeg.Metadata)
	add := func(outputs []string, inputs ...string) {
		for _, input := range inputs {
			if text, ok := embeddedMetadataText(info.Lookup(input)); ok {
				for _, output := range outputs {
					metadata[output] = text
				}
				return
			}
		}
	}
	add([]string{"title"}, "track", "title")
	add([]string{"date"}, "upload_date")
	add([]string{"description", "synopsis"}, "description")
	add([]string{"purl", "comment"}, "webpage_url")
	add([]string{"track"}, "track_number")
	add([]string{"artist"}, "artist", "artists", "creator", "creators", "uploader", "uploader_id")
	add([]string{"composer"}, "composer", "composers")
	add([]string{"genre"}, "genre", "genres", "categories", "tags")
	add([]string{"album"}, "album", "series")
	add([]string{"album_artist"}, "album_artist", "album_artists")
	add([]string{"disc"}, "disc_number")
	add([]string{"show"}, "series")
	add([]string{"season_number"}, "season_number")
	add([]string{"episode_id"}, "episode", "episode_id")
	add([]string{"episode_sort"}, "episode_number")
	return metadata
}

func embeddedMetadataText(input value.Value) (string, bool) {
	var text string
	switch input.Kind() {
	case value.KindString:
		text, _ = input.StringValue()
	case value.KindInt:
		number, _ := input.Int()
		text = strconv.FormatInt(number, 10)
	case value.KindFloat:
		number, _ := input.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return "", false
		}
		text = strconv.FormatFloat(number, 'g', -1, 64)
	case value.KindList:
		items, _ := input.ListValue()
		parts := make([]string, 0, len(items))
		for _, item := range items {
			part, ok := embeddedMetadataText(item)
			if !ok {
				continue
			}
			parts = append(parts, part)
		}
		text = strings.Join(parts, ", ")
	default:
		return "", false
	}
	text = strings.ToValidUTF8(text, "")
	text = strings.TrimSpace(strings.NewReplacer("\x00", "", "\r", " ", "\n", " ").Replace(text))
	if text == "" {
		return "", false
	}
	if len(text) > maximumEmbeddedMetadataValue {
		text = text[:maximumEmbeddedMetadataValue]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text, true
}

func canonicalEmbeddedChapters(info value.Info) ([]ffmpeg.Chapter, error) {
	items, ok := info.Lookup("chapters").ListValue()
	if !ok || len(items) == 0 {
		return nil, nil
	}
	if len(items) > 1000 {
		return nil, fmt.Errorf("%w: too many chapters", ffmpeg.ErrInvalidOperation)
	}
	chapters := make([]ffmpeg.Chapter, 0, len(items))
	var previousEnd float64
	for _, item := range items {
		object, ok := item.Object()
		if !ok {
			return nil, fmt.Errorf("%w: invalid chapter metadata", ffmpeg.ErrInvalidOperation)
		}
		start, startOK := chapterSeconds(object.Lookup("start_time"))
		end, endOK := chapterSeconds(object.Lookup("end_time"))
		if !startOK || !endOK || start < 0 || end <= start || start < previousEnd {
			return nil, fmt.Errorf("%w: invalid chapter boundaries", ffmpeg.ErrInvalidOperation)
		}
		title, _ := embeddedMetadataText(object.Lookup("title"))
		chapters = append(chapters, ffmpeg.Chapter{
			Start: time.Duration(start * float64(time.Second)),
			End:   time.Duration(end * float64(time.Second)),
			Title: title,
		})
		previousEnd = end
	}
	return chapters, nil
}

func chapterSeconds(input value.Value) (float64, bool) {
	if number, ok := input.Float(); ok {
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	}
	if number, ok := input.Int(); ok {
		return float64(number), true
	}
	return 0, false
}
