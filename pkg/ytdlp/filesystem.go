package ytdlp

import (
	"net/http"
	"os"
	"time"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func (operation *operation) filenameOptions() outputtemplate.FilenameOptions {
	fs := operation.request.Filesystem
	return outputtemplate.FilenameOptions{
		RestrictFilenames: fs.RestrictFilenames,
		WindowsFilenames:  fs.WindowsFilenames,
		TrimFilenames:     fs.TrimFilenames,
	}
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
		ThrottleRate: options.ThrottleRate, ThrottleWindow: options.ThrottleWindow,
		ThrottleRestarts: options.ThrottleRestarts, FileAttempts: options.FileAttempts,
	}, operation.request.Filesystem)
}

func (operation *operation) ffmpegConfig() ffmpeg.Config {
	ffmpegPath, ffprobePath := ffmpeg.ResolveConfiguredLocation(operation.request.Filesystem.FfmpegLocation)
	return ffmpeg.Config{FFmpegPath: ffmpegPath, FFprobePath: ffprobePath}
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
