package ytdlp

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/tejasa97/youtube_dlp/internal/compat/sections"
	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
)

// sectionDownloadSelections delegates a sectioned download of the selected
// formats to ffmpeg through DownloadSections. It rejects inputs that cannot
// be safely delegated (credential-isolated headers, unsafe protocols) before
// producing output, matching the pinned FFmpegFD abort behavior. Single
// selections delegate the URL directly; multiple selections are handed to
// ffmpeg as separate video/audio inputs and mapped.
func (operation *operation) sectionDownloadSelections(
	ctx context.Context,
	selections []mediaformat.Selection,
	bounds sections.Section,
	destination string,
	overwrite bool,
	forceKeyframes bool,
	sink events.Sink,
) (string, int64, error) {
	if len(selections) == 0 {
		return "", 0, fmt.Errorf("%w: no selected formats for section", extractor.ErrUnsupported)
	}
	if err := validateCredentialIsolatedDispatch(selections, true); err != nil {
		return "", 0, err
	}
	tools, err := operation.discoverFFmpegOnly()
	if err != nil {
		return "", 0, fmt.Errorf("%w: section download requires ffmpeg", err)
	}
	inputs, err := sectionInputsFromSelections(selections)
	if err != nil {
		return "", 0, err
	}
	ffmpegBounds := ffmpeg.SectionBounds{Start: bounds.Start, End: bounds.End}
	if err := tools.DownloadSections(ctx, inputs, ffmpegBounds, destination, overwrite, forceKeyframes, sink); err != nil {
		return "", 0, err
	}
	info, err := fileStat(destination)
	if err != nil {
		return "", 0, err
	}
	return destination, info.Size(), nil
}

// sectionInputsFromSelections converts format selections into typed ffmpeg
// section inputs, rejecting any selection whose URL is not a safe delegated
// http(s) media URL. Separate video and audio tracks produce separate inputs
// with the appropriate stream mapping flags.
func sectionInputsFromSelections(selections []mediaformat.Selection) ([]ffmpeg.SectionInput, error) {
	inputs := make([]ffmpeg.SectionInput, 0, len(selections))
	for _, selection := range selections {
		switch selection.Protocol {
		case "https", "http", "m3u8_native", "http_dash_segments":
			// Delegable to ffmpeg.
		default:
			return nil, fmt.Errorf("%w: This format cannot be partially downloaded. Aborting", extractor.ErrUnsupported)
		}
		if selection.CredentialIsolated {
			return nil, fmt.Errorf("%w: section download cannot enforce credential-isolated media", extractor.ErrTransportIsolation)
		}
		input := ffmpeg.SectionInput{
			URL:      selection.URL,
			Headers:  cloneSelectionHeaders(selection.Headers),
			HasVideo: selection.VCodec != "none" && selection.ACodec == "none",
			HasAudio: selection.ACodec != "none" && selection.VCodec == "none",
		}
		inputs = append(inputs, input)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: no section inputs", extractor.ErrUnsupported)
	}
	return inputs, nil
}

func cloneSelectionHeaders(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

// fileStat returns the size of a completed section output.
func fileStat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
