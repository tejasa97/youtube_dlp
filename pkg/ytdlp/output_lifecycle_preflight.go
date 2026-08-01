package ytdlp

import (
	"fmt"
	"path/filepath"
	"strings"

	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// preflightOutputLifecycles resolves every deterministic path that a plan can
// publish before the first lifecycle starts. Passing the complete set through
// PR 7's preflight prevents two plan-specific sidecars or postprocessor
// outputs from silently overwriting one another and teaches rollback which
// destinations existed before this run.
func (operation *operation) preflightOutputLifecycles(
	info value.Info,
	plans []mediaformat.OutputPlan,
	mediaDestinations []string,
	selectedSubtitles []subtitleTrack,
) error {
	var destinations []destinationPolicy
	for index, plan := range plans {
		planInfo := selectedPlanInfo(info, plan)
		operation.applyThumbnailEmbeddingOutputExtension(&planInfo, plan.Tracks)
		if !operation.request.SkipDownload {
			destinations = append(destinations, destinationPolicy{
				path: mediaDestinations[index], overwrite: operation.request.Overwrite,
			})
		}
		derived, err := operation.outputLifecycleDestinations(planInfo, plan, mediaDestinations[index], selectedSubtitles)
		if err != nil {
			return err
		}
		for _, path := range derived {
			destinations = append(destinations, destinationPolicy{
				path: path, overwrite: operation.request.Overwrite,
			})
		}
		if !operation.request.SkipDownload {
			postprocessorPaths, err := operation.postprocessorDestinations(mediaDestinations[index])
			if err != nil {
				return err
			}
			for _, path := range postprocessorPaths {
				destinations = append(destinations, destinationPolicy{
					path: path, overwrite: operation.request.postprocessorOverwrites(),
				})
			}
		}
	}
	return operation.preflightDestinationPolicies(destinations)
}

func (operation *operation) outputLifecycleDestinations(
	info value.Info,
	plan mediaformat.OutputPlan,
	mediaDestination string,
	selectedSubtitles []subtitleTrack,
) ([]string, error) {
	var destinations []string
	addRelated := func(kind, extension string) error {
		templateType := outputTemplateTypeForRelatedFile(kind, false)
		path, err := operation.relatedFilePath(
			operation.request.outputRoot(outputPathTypeForTemplate(templateType)),
			operation.request.outputTemplate(templateType), info, extension,
		)
		if err != nil {
			return err
		}
		destinations = append(destinations, path)
		return nil
	}
	if operation.request.RelatedFiles.WriteInfoJSON {
		if err := addRelated("infojson", "info.json"); err != nil {
			return nil, err
		}
	}
	if operation.request.RelatedFiles.WriteDescription {
		if _, ok := info.Lookup("description").StringValue(); ok {
			if err := addRelated("description", "description"); err != nil {
				return nil, err
			}
		}
	}
	if rawURL, ok := info.Lookup("webpage_url").StringValue(); ok {
		if _, err := safeLinkURL(rawURL); err == nil {
			for _, linkType := range selectedLinkTypes(operation.request.RelatedFiles) {
				if err := addRelated("link", linkType); err != nil {
					return nil, err
				}
			}
		}
	}

	if operation.request.Thumbnails.Write || operation.request.Thumbnails.WriteAll || operation.request.Thumbnails.Embed {
		mapping, err := parseThumbnailConversionMapping(operation.request.Thumbnails.ConvertFormat)
		if err != nil {
			return nil, err
		}
		tracks, err := selectThumbnails(&info)
		if err != nil {
			return nil, err
		}
		multiple := operation.request.Thumbnails.WriteAll && len(tracks) > 1
		root := operation.request.outputRoot(OutputPathThumbnail)
		pattern := operation.request.outputTemplate(OutputTemplateThumbnail)
		for _, track := range tracks {
			source, err := operation.thumbnailPath(root, pattern, info, track, multiple)
			if err != nil {
				return nil, err
			}
			destinations = append(destinations, source)
			final, err := thumbnailConversionPath(operation.request.outputRoot(OutputPathHome), source, track.extension, mapping)
			if err != nil {
				return nil, err
			}
			if final != source {
				destinations = append(destinations, final)
			}
		}
	}

	if len(selectedSubtitles) > 0 {
		root := operation.request.outputRoot(OutputPathSubtitle)
		pattern := operation.request.outputTemplate(OutputTemplateSubtitle)
		for _, track := range selectedSubtitles {
			expectedExtension, ok := info.Lookup("ext").StringValue()
			if !ok || !subtitleExtensionPattern.MatchString(expectedExtension) {
				expectedExtension = "subtitle"
			}
			outputInfo := value.NewInfo(info.Fields().Clone())
			outputInfo.Set("ext", value.String(expectedExtension))
			base, err := operation.resolveOutputPath(root, pattern, outputInfo)
			if err != nil {
				return nil, err
			}
			source := subtitleFilename(base, expectedExtension, track.language, track.extension)
			destinations = append(destinations, source)
			if format := operation.request.Subtitles.ConvertFormat; format != "" {
				extension := format
				if extension == "webvtt" {
					extension = "vtt"
				}
				if format == "vtt" {
					extension = "vtt"
				}
				if !strings.EqualFold(track.extension, extension) {
					destinations = append(destinations, strings.TrimSuffix(source, filepath.Ext(source))+"."+extension)
				}
			}
		}
	}

	return destinations, nil
}

func (operation *operation) postprocessorDestinations(downloadedPath string) ([]string, error) {
	root := operation.request.outputRoot(OutputPathHome)
	current := downloadedPath
	var destinations []string
	addOutput := func(requested, input, extension string, advances bool) error {
		path, err := postprocessOutput(root, requested, input, extension)
		if err != nil {
			return err
		}
		destinations = append(destinations, path)
		if advances {
			current = path
		}
		return nil
	}
	for index, step := range operation.request.Postprocessors {
		switch {
		case step.ExtractAudio != nil:
			if err := addOutput(step.ExtractAudio.Destination, current, step.ExtractAudio.Codec, true); err != nil {
				return nil, fmt.Errorf("postprocessors[%d]: %w", index, err)
			}
		case step.Remux != nil:
			format := step.Remux.Format
			if format == "" {
				format = "mkv"
			}
			if err := addOutput(step.Remux.Destination, current, format, true); err != nil {
				return nil, fmt.Errorf("postprocessors[%d]: %w", index, err)
			}
		case step.RecodeVideo != nil:
			sourceExtension := strings.TrimPrefix(strings.ToLower(filepath.Ext(current)), ".")
			target, skip, mapErr := ffmpeg.ResolveRecodeMapping(sourceExtension, step.RecodeVideo.Format)
			if mapErr != nil {
				return nil, fmt.Errorf("postprocessors[%d]: %w", index, mapErr)
			}
			if skip != "" {
				// Pinned FFmpegVideoConvertorPP.run no-ops when the resolved
				// target equals the source or no mapping rule applies: no
				// destination is reserved and current must remain unchanged
				// so subsequent steps operate on the original media path.
				_ = skip
				continue
			}
			if err := addOutput(step.RecodeVideo.Destination, current, target, true); err != nil {
				return nil, fmt.Errorf("postprocessors[%d]: %w", index, err)
			}
		case step.ConvertSubtitle != nil:
			source, err := postprocessInput(root, step.ConvertSubtitle.Source)
			if err != nil {
				return nil, err
			}
			if err := addOutput(step.ConvertSubtitle.Destination, source, step.ConvertSubtitle.Format, false); err != nil {
				return nil, fmt.Errorf("postprocessors[%d]: %w", index, err)
			}
		case step.ConvertThumbnail != nil:
			source, err := postprocessInput(root, step.ConvertThumbnail.Source)
			if err != nil {
				return nil, err
			}
			if err := addOutput(step.ConvertThumbnail.Destination, source, step.ConvertThumbnail.Format, false); err != nil {
				return nil, fmt.Errorf("postprocessors[%d]: %w", index, err)
			}
		case step.EmbedMetadata != nil:
			if err := addOutput(step.EmbedMetadata.Destination, current, filepath.Ext(current), true); err != nil {
				return nil, err
			}
		case step.EmbedChapters != nil:
			if err := addOutput(step.EmbedChapters.Destination, current, filepath.Ext(current), true); err != nil {
				return nil, err
			}
		case step.EmbedThumbnail != nil:
			if err := addOutput(step.EmbedThumbnail.Destination, current, filepath.Ext(current), true); err != nil {
				return nil, err
			}
		case step.EmbedSubtitle != nil:
			if err := addOutput(step.EmbedSubtitle.Destination, current, filepath.Ext(current), true); err != nil {
				return nil, err
			}
		case step.Fixup != nil:
			if err := addOutput(step.Fixup.Destination, current, filepath.Ext(current), true); err != nil {
				return nil, err
			}
		case step.Concat != nil:
			if err := addOutput(step.Concat.Destination, current, filepath.Ext(current), true); err != nil {
				return nil, err
			}
		case step.Move != nil:
			if err := addOutput(step.Move.Destination, current, filepath.Ext(current), true); err != nil {
				return nil, err
			}
		}
	}
	if err := validateMetadataEmbeddingContainer(current, operation.request); err != nil {
		return nil, err
	}
	return destinations, nil
}
