package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/ytdlp-go/ytdlp/apps/desktop/internal/ffmpegdetect"
	"github.com/ytdlp-go/ytdlp/apps/desktop/internal/jobs"
	"github.com/ytdlp-go/ytdlp/apps/desktop/internal/store"
	"github.com/ytdlp-go/ytdlp/apps/desktop/internal/urlcheck"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

// App is the Wails-bound root. Every exported method is reachable from
// the frontend via the generated bindings in wailsjs/go/main/App.js.
type App struct {
	ctx        context.Context
	store      *store.Store
	jobs       *jobs.Manager
	mu         sync.Mutex
	lastFFmpeg ffmpegdetect.Status
}

// NewApp constructs the App. The Wails bind() call wires every public
// method to the JS side.
func NewApp() *App { return &App{} }

// startup is called once by Wails after the window is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	statePath, err := store.DefaultPath()
	if err != nil {
		wailsruntime.LogErrorf(ctx, "desktop: store path: %v", err)
		statePath = filepath.Join(os.TempDir(), "ytdlp-desktop", "state.json")
	}
	st, err := store.Open(statePath)
	if err != nil {
		wailsruntime.LogErrorf(ctx, "desktop: open store: %v", err)
		st = store.NewEphemeral()
	}
	a.store = st

	a.jobs = jobs.New(nil, func(ev jobs.Event) {
		// Events are dispatched on a background goroutine by the jobs
		// package; Wails runtime is safe to call from any goroutine.
		wailsruntime.EventsEmit(a.ctx, ev.Name, ev)
		// For terminal events, append to history so the Downloads page
		// stays in sync.
		if ev.Name == jobs.EventJobUpdate && isTerminal(ev.Job.Status) {
			a.recordHistory(ev.Job)
		}
	})

	settings := a.store.Settings()
	a.jobs.SetFFmpegLocation(settings.FFmpegPath)
	a.setFFmpegStatus(ffmpegdetect.Probe(ctx, settings.FFmpegPath))
}

// shutdown is called by Wails when the window closes. It cancels every
// in-flight job so the process can exit cleanly.
func (a *App) shutdown(ctx context.Context) {
	if a.jobs == nil {
		return
	}
	for _, snap := range a.jobs.List() {
		if snap.Status == jobs.StatusActive || snap.Status == jobs.StatusPending {
			a.jobs.Cancel(snap.ID)
		}
	}
	a.jobs.Close()
}

// ---------------------------------------------------------------------------
// Bound methods
// ---------------------------------------------------------------------------

// GetSettings returns the persisted settings.
func (a *App) GetSettings() store.Settings { return a.store.Settings() }

// UpdateSettings persists new settings and re-probes ffmpeg so the UI
// stays accurate.
func (a *App) UpdateSettings(next store.Settings) (store.Settings, error) {
	if strings.TrimSpace(next.DownloadFolder) == "" {
		return store.Settings{}, errors.New("download folder is required")
	}
	expanded, err := expandHome(next.DownloadFolder)
	if err != nil {
		return store.Settings{}, err
	}
	if err := os.MkdirAll(expanded, 0o755); err != nil {
		return store.Settings{}, fmt.Errorf("could not create folder: %w", err)
	}
	next.DownloadFolder = expanded
	if err := a.store.SetSettings(next); err != nil {
		return store.Settings{}, err
	}
	a.jobs.SetFFmpegLocation(next.FFmpegPath)
	a.setFFmpegStatus(ffmpegdetect.Probe(a.ctx, next.FFmpegPath))
	wailsruntime.EventsEmit(a.ctx, "settings:update", a.store.Settings())
	return a.store.Settings(), nil
}

// PickDownloadFolder opens a native folder picker and returns the
// chosen path (empty string if the user cancelled).
func (a *App) PickDownloadFolder() (string, error) {
	return wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:                "Choose download folder",
		DefaultDirectory:     a.store.Settings().DownloadFolder,
		CanCreateDirectories: true,
	})
}

// PickFFmpegPath opens a file picker for a binary and returns the
// selected path. The caller validates the path with ConfigureFFmpeg.
func (a *App) PickFFmpegPath() (string, error) {
	pattern := "ffmpeg"
	if runtime.GOOS == "windows" {
		pattern = "ffmpeg.exe"
	}
	return wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "Choose ffmpeg binary",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "ffmpeg binary", Pattern: pattern},
			{DisplayName: "All files", Pattern: "*.*"},
		},
	})
}

// GetFFmpegStatus returns the current ffmpeg probe result.
func (a *App) GetFFmpegStatus() ffmpegdetect.Status { return a.ffmpegStatus() }

// ProbeFFmpeg re-runs detection and broadcasts the result.
func (a *App) ProbeFFmpeg() ffmpegdetect.Status {
	status := ffmpegdetect.Probe(a.ctx, a.store.Settings().FFmpegPath)
	a.setFFmpegStatus(status)
	wailsruntime.EventsEmit(a.ctx, "ffmpeg:update", status)
	return status
}

// ConfigureFFmpeg validates a path, persists it, and re-probes.
func (a *App) ConfigureFFmpeg(path string) (ffmpegdetect.Status, error) {
	status := ffmpegdetect.ConfigurePath(a.ctx, path)
	if !status.Available {
		a.setFFmpegStatus(status)
		wailsruntime.EventsEmit(a.ctx, "ffmpeg:update", status)
		return status, errors.New(status.Message)
	}
	settings := a.store.Settings()
	settings.FFmpegPath = status.Path
	if err := a.store.SetSettings(settings); err != nil {
		return status, err
	}
	a.jobs.SetFFmpegLocation(status.Path)
	a.setFFmpegStatus(status)
	wailsruntime.EventsEmit(a.ctx, "ffmpeg:update", status)
	wailsruntime.EventsEmit(a.ctx, "settings:update", a.store.Settings())
	return status, nil
}

// ClearFFmpegPath removes the configured path so the app falls back to
// PATH discovery.
func (a *App) ClearFFmpegPath() ffmpegdetect.Status {
	settings := a.store.Settings()
	settings.FFmpegPath = ""
	_ = a.store.SetSettings(settings)
	a.jobs.SetFFmpegLocation("")
	status := ffmpegdetect.Probe(a.ctx, "")
	a.setFFmpegStatus(status)
	wailsruntime.EventsEmit(a.ctx, "ffmpeg:update", status)
	wailsruntime.EventsEmit(a.ctx, "settings:update", a.store.Settings())
	return status
}

// ---------------------------------------------------------------------------
// URL validation & analyse
// ---------------------------------------------------------------------------

// ValidateURL returns either an accepted single-video Result or an
// error whose Reason() explains why it was rejected.
func (a *App) ValidateURL(raw string) (urlcheck.Result, error) {
	return urlcheck.Validate(raw)
}

// AnalyzeURL fetches metadata for one single YouTube video. It refuses
// to call the core engine for non-video URLs.
func (a *App) AnalyzeURL(raw string) (jobs.InfoSummary, error) {
	res, err := urlcheck.Validate(raw)
	if err != nil {
		return jobs.InfoSummary{}, err
	}
	// EJS preprocessing has a 55-second execution budget. Leave additional
	// room for the watch page and player-script requests around that phase.
	ctx, cancel := context.WithTimeout(a.ctx, 75*time.Second)
	defer cancel()
	summary, err := a.jobs.Analyze(ctx, res.URL)
	if err != nil {
		wailsruntime.LogErrorf(a.ctx, "desktop: analyze video: %v", err)
		return jobs.InfoSummary{}, errors.New(friendlyAnalyzeError(err))
	}
	if summary.Title == "" {
		summary.Title = "Untitled video"
	}
	summary.URL = res.URL
	summary.VideoID = res.VideoID
	return summary, nil
}

// ---------------------------------------------------------------------------
// Queue
// ---------------------------------------------------------------------------

// StartDownload enqueues a download and starts the FIFO worker.
func (a *App) StartDownload(req jobs.Request) (string, error) {
	if req.URL == "" {
		return "", errors.New("empty url")
	}
	res, err := urlcheck.Validate(req.URL)
	if err != nil {
		return "", err
	}
	req.URL = res.URL
	if req.VideoID == "" {
		req.VideoID = res.VideoID
	}
	settings := a.store.Settings()
	if req.OutputDir == "" {
		req.OutputDir = settings.DownloadFolder
	}
	if req.Quality == "" {
		req.Quality = jobs.QualityBest
	}
	if !isSupportedQuality(req.Quality) {
		return "", fmt.Errorf("unsupported quality preset %q", req.Quality)
	}
	return a.jobs.Submit(req)
}

// ListJobs returns the current queue + history snapshot.
func (a *App) ListJobs() []jobs.JobSnapshot { return a.jobs.List() }

// CancelJob cancels an active or pending job.
func (a *App) CancelJob(id string) { a.jobs.Cancel(id) }

// RetryJob re-queues a failed or canceled job.
func (a *App) RetryJob(id string) error { return a.jobs.Retry(id) }

// RemoveJob drops a terminal job from the manager.
func (a *App) RemoveJob(id string) { a.jobs.Remove(id) }

// ClearCompletedJobs removes every terminal job from the in-memory queue.
func (a *App) ClearCompletedJobs() { a.jobs.ClearTerminal() }

// ---------------------------------------------------------------------------
// Downloads (history)
// ---------------------------------------------------------------------------

// ListDownloads returns the persisted history.
func (a *App) ListDownloads() []store.HistoryEntry { return a.store.History() }

// RemoveDownload deletes one history entry.
func (a *App) RemoveDownload(id string) error {
	removed, err := a.store.RemoveHistory(id)
	if err == nil && removed {
		wailsruntime.EventsEmit(a.ctx, "history:update", a.store.History())
	}
	return err
}

// ClearDownloads empties the persisted history.
func (a *App) ClearDownloads() error {
	if err := a.store.ClearHistory(); err != nil {
		return err
	}
	wailsruntime.EventsEmit(a.ctx, "history:update", a.store.History())
	return nil
}

// OpenFile opens a downloaded file with the OS default application.
// It uses xdg-open / open / rundll32 so the platform handler is the
// one users already trust.
func (a *App) OpenFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}
	expanded, err := expandHome(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(expanded); err != nil {
		return err
	}
	return launchWithOS(expanded)
}

// RevealInFinder reveals a path in Finder / Explorer / file manager.
func (a *App) RevealInFinder(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}
	expanded, err := expandHome(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(expanded); err != nil {
		return err
	}
	return revealWithOS(expanded)
}

// CopyDiagnostics writes a small, sanitised diagnostic report to the
// clipboard. It never includes the URL or any file path beyond the
// folder basename.
func (a *App) CopyDiagnostics() (string, error) {
	status := a.ffmpegStatus()
	settings := a.store.Settings()
	report := strings.Builder{}
	report.WriteString("ytdlp-desktop diagnostics\n")
	report.WriteString("App: ytdlp-desktop v0 (Go " + runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")\n")
	report.WriteString("Download folder: " + filepath.Base(settings.DownloadFolder) + "\n")
	// Privacy: do not include the absolute FFmpeg path. The basename
	// tells support which binary the user picked without disclosing
	// home-directory layout. If no configured path is present we still
	// note whether detection succeeded.
	if status.Path != "" {
		report.WriteString("FFmpeg: " + status.Message + " (" + filepath.Base(status.Path) + ")")
	} else {
		report.WriteString("FFmpeg: " + status.Message)
	}
	report.WriteString("\n")
	report.WriteString(fmt.Sprintf("Queue depth: %d\n", len(a.jobs.List())))
	text := report.String()
	if err := wailsruntime.ClipboardSetText(a.ctx, text); err != nil {
		return "", err
	}
	return text, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func isSupportedQuality(q jobs.Quality) bool {
	for _, candidate := range jobs.AllQualities {
		if candidate == q {
			return true
		}
	}
	return false
}

func isTerminal(s jobs.Status) bool {
	return s == jobs.StatusComplete || s == jobs.StatusFailed || s == jobs.StatusCanceled
}

func (a *App) setFFmpegStatus(status ffmpegdetect.Status) {
	a.mu.Lock()
	a.lastFFmpeg = status
	a.mu.Unlock()
}

func (a *App) ffmpegStatus() ffmpegdetect.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastFFmpeg
}

func (a *App) recordHistory(snap jobs.JobSnapshot) {
	if snap.Status != jobs.StatusComplete {
		return
	}
	if snap.AbsolutePath == "" {
		return
	}
	entry := store.HistoryEntry{
		ID:            snap.ID,
		VideoID:       snap.VideoID,
		Title:         snap.Title,
		Channel:       snap.Channel,
		Quality:       string(snap.Quality),
		Filename:      snap.Filename,
		AbsolutePath:  snap.AbsolutePath,
		SizeBytes:     snap.Bytes,
		CompletedAt:   snap.CompletedAt,
		DurationLabel: snap.DurationLabel,
		Thumbnail:     snap.Thumbnail,
	}
	if err := a.store.AppendHistory(entry); err != nil {
		wailsruntime.LogErrorf(a.ctx, "desktop: append history: %v", err)
		return
	}
	wailsruntime.EventsEmit(a.ctx, "history:update", a.store.History())
}

func expandHome(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func launchWithOS(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func revealWithOS(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", path).Start()
	case "windows":
		return exec.Command("explorer", "/select,", path).Start()
	default:
		dir := filepath.Dir(path)
		return exec.Command("xdg-open", dir).Start()
	}
}

func friendlyAnalyzeError(err error) string {
	if err == nil {
		return "We could not read that video. Try again in a moment."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "Video analysis timed out — retry"
	}
	var typed *ytdlp.Error
	if errors.As(err, &typed) {
		switch typed.Category {
		case ytdlp.ErrorUnsupported:
			if isYouTubeChallengeTimeout(err) {
				return "YouTube challenge timed out — retry"
			}
			return "That link is not a supported single YouTube video."
		case ytdlp.ErrorAuthentication:
			return "That video requires sign-in and is not available in this version."
		case ytdlp.ErrorInvalidInput:
			return "That YouTube link is not valid."
		case ytdlp.ErrorNetwork:
			return "We could not reach YouTube. Check your connection and try again."
		case ytdlp.ErrorCancelled:
			return "Video analysis was canceled."
		case ytdlp.ErrorSecurity:
			return "This video was blocked by a security check."
		}
	}
	return "We could not read that video. Try again in a moment."
}

func isYouTubeChallengeTimeout(err error) bool {
	message := err.Error()
	return strings.Contains(message, "JavaScript challenge solver unavailable") &&
		strings.Contains(message, "EJS helper timeout") &&
		strings.Contains(message, "JavaScript execution timed out")
}
