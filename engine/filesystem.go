package engine

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
	"unicode"
	"unicode/utf8"

	outputtemplate "github.com/tejasa97/youtube_dlp/internal/compat/template"
	"github.com/tejasa97/youtube_dlp/internal/downloader"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	maxFilenameAutonumberSize   = 64
	maxFilenameTrim             = 4096
	maxOutputNaPlaceholderBytes = 256
)

var errInvalidFilenameOptions = errors.New("invalid filename options")

func (operation *operation) filenameOptions() outputtemplate.FilenameOptions {
	return filenameOptionsFor(operation.request.Filesystem, operation.request.AutonumberSize)
}

// filenameOptionsFor is the single conversion from public request controls to
// the compatibility renderer. Output preview uses this same pure mapping so a
// reservation proposal cannot drift from the runtime filename policy.
func filenameOptionsFor(filesystem FilesystemOptions, autonumberSize int) outputtemplate.FilenameOptions {
	return outputtemplate.FilenameOptions{
		RestrictFilenames:   filesystem.RestrictFilenames,
		WindowsFilenames:    filesystem.WindowsFilenames,
		TrimFilenames:       filesystem.TrimFilenames,
		OutputNaPlaceholder: filesystem.OutputNaPlaceholder,
		AutonumberSize:      normalizedAutonumberSize(autonumberSize),
	}
}

// validateFilenameOptions is shared by Request preflight and the public
// network-free preview. Keeping the bounds here prevents preview-only callers
// from reaching the renderer with controls that runtime would reject.
func validateFilenameOptions(filesystem FilesystemOptions, autonumberSize int) error {
	if autonumberSize < 0 || autonumberSize > maxFilenameAutonumberSize {
		return fmt.Errorf("%w: autonumber size", errInvalidFilenameOptions)
	}
	if filesystem.TrimFilenames < 0 || filesystem.TrimFilenames > maxFilenameTrim {
		return fmt.Errorf("%w: trim filenames", errInvalidFilenameOptions)
	}
	placeholder := filesystem.OutputNaPlaceholder
	if len(placeholder) > maxOutputNaPlaceholderBytes || !utf8.ValidString(placeholder) {
		return fmt.Errorf("%w: output NA placeholder", errInvalidFilenameOptions)
	}
	for _, character := range placeholder {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: output NA placeholder control", errInvalidFilenameOptions)
		}
	}
	return nil
}

func normalizedAutonumberSize(size int) int {
	if size <= 0 {
		return 5
	}
	return size
}

func normalizedAutonumberStart(start int) int {
	if start <= 0 {
		return 1
	}
	return start
}

func (operation *operation) resolveOutputPath(outputRoot, pattern string, info value.Info) (string, error) {
	return outputtemplate.ResolveWithOptions(outputRoot, pattern, info, operation.filenameOptions())
}

func applyDownloaderFilesystem(job downloader.Job, filesystem FilesystemOptions) downloader.Job {
	job.NoContinue = filesystem.NoContinue
	job.NoPart = filesystem.NoPart
	return job
}

func (operation *operation) directDownloadJob(url string, headers http.Header, outputRoot, destination string) downloader.Job {
	options := operation.request.Downloader
	return applyDownloaderFilesystem(downloader.Job{
		URL: url, Headers: headers, OutputRoot: outputRoot, Destination: destination,
		Overwrite: operation.request.Overwrite, Attempts: options.Attempts,
		RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
		RateLimit: options.RateLimit, MaxBytes: options.MaxBytes,
		MinFilesize: options.MinFilesize, MaxFilesize: options.MaxFilesize,
		ThrottleRate: options.ThrottleRate, ThrottleWindow: options.ThrottleWindow,
		ThrottleRestarts: options.ThrottleRestarts, FileAttempts: options.FileAttempts,
	}, operation.request.Filesystem)
}

func (operation *operation) ffmpegConfig() ffmpeg.Config {
	return ffmpegConfigFor(operation.request.Filesystem)
}

func ffmpegConfigFor(filesystem FilesystemOptions) ffmpeg.Config {
	ffmpegPath, ffprobePath := ffmpeg.ResolveConfiguredLocation(filesystem.FfmpegLocation)
	return ffmpeg.Config{FFmpegPath: ffmpegPath, FFprobePath: ffprobePath}
}

func plannerCapabilitiesFor(request Request) mediaformat.PlannerCapabilities {
	_, ffmpegErr := ffmpeg.DiscoverFFmpeg(ffmpegConfigFor(request.Filesystem))
	return mediaformat.PlannerCapabilities{
		CanMergeFormats: ffmpegErr == nil,
		OutputToStdout:  request.outputTemplate(OutputTemplateDefault) == "-",
	}
}

func (operation *operation) discoverFFmpeg() (*ffmpeg.Toolset, error) {
	return ffmpeg.Discover(operation.ffmpegConfig())
}

func (operation *operation) discoverFFmpegOnly() (*ffmpeg.Toolset, error) {
	return ffmpeg.DiscoverFFmpeg(operation.ffmpegConfig())
}

func metadataModificationTime(info value.Info) (time.Time, bool) {
	if timestamp, ok := info.Lookup("timestamp").Int(); ok && timestamp > 0 {
		return time.Unix(timestamp, 0).UTC(), true
	}
	if uploadDate, ok := info.Lookup("upload_date").StringValue(); ok && len(uploadDate) == 8 {
		parsed, err := time.ParseInLocation("20060102", uploadDate, time.UTC)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func (operation *operation) applyOutputMtime(path string, info value.Info) error {
	if operation.request.Filesystem.NoMtime {
		return nil
	}
	modTime, ok := metadataModificationTime(info)
	if !ok {
		return nil
	}
	return os.Chtimes(path, modTime, modTime)
}
