// Package jobs manages the desktop app's download queue. It serialises
// downloads (one active at a time, FIFO waiting) and bridges events
// from the focused engine composition into app-friendly JobSnapshot values the
// UI renders.
//
// The package deliberately keeps state in memory; persistence of
// completed downloads is handled by the store package.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tejasa97/youtube_dlp/engine"
	provideryoutube "github.com/tejasa97/youtube_dlp/providers/youtube"
)

// Quality is the user-visible quality preset. The string values are
// stable and part of the UI contract.
type Quality string

const (
	QualityBest      Quality = "best"
	Quality4K        Quality = "4k"
	Quality1440p     Quality = "1440p"
	Quality1080p     Quality = "1080p"
	Quality720p      Quality = "720p"
	QualityAudioOnly Quality = "audio"
)

// AllQualities is the fixed V0 quality list.
var AllQualities = []Quality{
	QualityBest, Quality4K, Quality1440p, Quality1080p, Quality720p, QualityAudioOnly,
}

// Label returns the user-visible label for a quality.
func (q Quality) Label() string {
	switch q {
	case QualityBest:
		return "Best"
	case Quality4K:
		return "4K"
	case Quality1440p:
		return "1440p"
	case Quality1080p:
		return "1080p"
	case Quality720p:
		return "720p"
	case QualityAudioOnly:
		return "Audio only"
	}
	return string(q)
}

// ytdlpFormat returns the core ytdlp format selector for a preset.
func (q Quality) ytdlpFormat() string {
	switch q {
	case QualityBest:
		return "bv*+ba/b"
	case Quality4K:
		return "bv*[height<=2160]+ba/b[height<=2160]"
	case Quality1440p:
		return "bv*[height<=1440]+ba/b[height<=1440]"
	case Quality1080p:
		return "bv*[height<=1080]+ba/b[height<=1080]"
	case Quality720p:
		return "bv*[height<=720]+ba/b[height<=720]"
	case QualityAudioOnly:
		return "ba/b"
	}
	return "b"
}

// outputTemplate keeps artifacts for different V0 presets distinct. A video
// and audio-only download of the same title may share an extension, so the
// default title-only template would otherwise let the later job overwrite the
// earlier successful artifact.
func (q Quality) outputTemplate() string {
	return fmt.Sprintf("%%(title)s [%%(id)s] [%s].%%(ext)s", q.Label())
}

// Status is the lifecycle state of a job.
type Status string

const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

// Request is the data needed to schedule one download.
type Request struct {
	URL       string  `json:"url"`
	VideoID   string  `json:"videoId"`
	Title     string  `json:"title"`
	Channel   string  `json:"channel"`
	Quality   Quality `json:"quality"`
	OutputDir string  `json:"outputDir"`
	Duration  string  `json:"duration"`
	Thumbnail string  `json:"thumbnail"`
}

// JobSnapshot is the immutable view of a job exposed to the UI.
type JobSnapshot struct {
	ID            string  `json:"id"`
	URL           string  `json:"url"`
	VideoID       string  `json:"videoID"`
	Title         string  `json:"title"`
	Channel       string  `json:"channel"`
	Quality       Quality `json:"quality"`
	QualityLabel  string  `json:"qualityLabel"`
	OutputDir     string  `json:"outputDir"`
	DurationLabel string  `json:"durationLabel"`
	Thumbnail     string  `json:"thumbnail"`
	Status        Status  `json:"status"`
	CreatedAt     string  `json:"createdAt"`
	StartedAt     string  `json:"startedAt,omitempty"`
	CompletedAt   string  `json:"completedAt,omitempty"`
	Bytes         int64   `json:"bytes"`
	Total         int64   `json:"total"`
	Progress      float64 `json:"progress"`
	SpeedBps      float64 `json:"speedBps"`
	ETASeconds    float64 `json:"etaSeconds"`
	Filename      string  `json:"filename"`
	AbsolutePath  string  `json:"absolutePath"`
	Message       string  `json:"message"`
	ErrorReason   string  `json:"errorReason,omitempty"`
}

// Listener receives lifecycle events. The queue preserves event order through
// one dedicated dispatcher instead of launching an independent goroutine for
// every snapshot.
type Listener func(event Event)

// Event names match the Wails events emitted by the app layer.
const (
	EventJobUpdate = "job:update"
	EventQueue     = "queue:update"
)

// Event is a single lifecycle notification.
type Event struct {
	Name  string        `json:"name"`
	Job   JobSnapshot   `json:"job"`
	Queue []JobSnapshot `json:"queue"`
}

// Manager owns the queue and the client used for metadata analysis. Downloads
// use short-lived clients so each job can attach its own event handler.
type Manager struct {
	client         *engine.Client
	listener       Listener
	events         chan Event
	runDownload    downloadRunner
	ffmpegLocation string
	mu             sync.Mutex
	all            map[string]*jobState
	order          []string
	active         string
}

type downloadRunner func(context.Context, engine.Request, engine.EventHandler) (engine.Result, error)

type jobState struct {
	snap     JobSnapshot
	cancel   context.CancelFunc
	ctx      context.Context
	done     chan struct{}
	startBps time.Time
	startByt int64
}

// New creates a Manager. listener may be nil for headless tests.
func New(client *engine.Client, listener Listener) *Manager {
	if client == nil {
		client = newFocusedClient()
	}
	manager := &Manager{
		client:      client,
		listener:    listener,
		runDownload: defaultDownloadRunner,
		all:         make(map[string]*jobState),
	}
	if listener != nil {
		manager.events = make(chan Event, 256)
		go manager.dispatchEvents()
	}
	return manager
}

func defaultDownloadRunner(ctx context.Context, req engine.Request, handler engine.EventHandler) (engine.Result, error) {
	client := newFocusedClient(engine.WithEventHandler(handler))
	defer client.Close()
	return client.Run(ctx, req)
}

// newFocusedClient is the single Desktop composition factory. Analysis keeps
// one instance and each download creates one with only its event handler
// differing, so both paths receive the complete YouTube provider family.
func newFocusedClient(options ...engine.Option) *engine.Client {
	return engine.NewClient(provideryoutube.NewComposition(), options...)
}

// Close releases the analysis client's helper process. In-flight download
// clients are owned and closed by defaultDownloadRunner.
func (m *Manager) Close() { m.client.Close() }

func (m *Manager) dispatchEvents() {
	for event := range m.events {
		m.listener(event)
	}
}

// SetFFmpegLocation updates the per-request tool location. It is deliberately
// scoped to future requests so the desktop process never mutates PATH while a
// download is already running.
func (m *Manager) SetFFmpegLocation(path string) {
	m.mu.Lock()
	m.ffmpegLocation = strings.TrimSpace(path)
	m.mu.Unlock()
}

func (m *Manager) ffmpegLocationSnapshot() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ffmpegLocation
}

// Submit enqueues a new job and returns its id. The job starts as soon
// as it reaches the head of the FIFO queue.
func (m *Manager) Submit(req Request) (string, error) {
	if req.URL == "" {
		return "", errors.New("jobs: empty url")
	}
	if req.Quality == "" {
		req.Quality = QualityBest
	}
	if !isKnownQuality(req.Quality) {
		return "", fmt.Errorf("jobs: unsupported quality %q", req.Quality)
	}
	if req.OutputDir == "" {
		return "", errors.New("jobs: empty output directory")
	}
	if err := ensureDir(req.OutputDir); err != nil {
		return "", fmt.Errorf("jobs: prepare output dir: %w", err)
	}

	id := uuid.NewString()
	state := &jobState{
		snap: JobSnapshot{
			ID:            id,
			URL:           req.URL,
			VideoID:       req.VideoID,
			Title:         req.Title,
			Channel:       req.Channel,
			Quality:       req.Quality,
			QualityLabel:  req.Quality.Label(),
			OutputDir:     req.OutputDir,
			DurationLabel: req.Duration,
			Thumbnail:     req.Thumbnail,
			Status:        StatusPending,
			CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		},
		done: make(chan struct{}),
	}

	m.mu.Lock()
	m.all[id] = state
	m.order = append(m.order, id)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.mu.Unlock()
	return id, nil
}

// List returns a copy of every job snapshot in display order
// (active first, then queued, then terminal jobs).
func (m *Manager) List() []JobSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// Cancel stops the job if it is running and removes it from the queue.
// It marks terminal jobs (complete/failed/canceled) as canceled so the
// caller can rely on the state to flow.
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	switch state.snap.Status {
	case StatusComplete, StatusFailed, StatusCanceled:
		m.mu.Unlock()
		return
	}
	if m.active == id {
		if state.cancel != nil {
			state.cancel()
		}
		state.snap.Message = "Canceling"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		m.emitQueueLocked()
	} else {
		if state.cancel != nil {
			state.cancel()
		}
		state.snap.Status = StatusCanceled
		state.snap.Message = "Canceled"
		state.snap.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		m.all[id] = state
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		// remove from pending ordering so the row disappears from the
		// queue pane immediately.
		m.removeFromOrderLocked(id)
		m.maybeStartNextLocked()
		m.emitQueueLocked()
	}
	m.mu.Unlock()
}

// Retry re-queues a failed or canceled job. It rebuilds the request
// fields from the stored snapshot so the caller only needs the id.
func (m *Manager) Retry(id string) error {
	m.mu.Lock()
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("jobs: unknown job %q", id)
	}
	if state.snap.Status != StatusFailed && state.snap.Status != StatusCanceled {
		m.mu.Unlock()
		return fmt.Errorf("jobs: only failed or canceled jobs can be retried")
	}
	state.snap.Status = StatusPending
	state.snap.Progress = 0
	state.snap.Bytes = 0
	state.snap.Total = 0
	state.snap.SpeedBps = 0
	state.snap.ETASeconds = 0
	state.snap.Message = ""
	state.snap.ErrorReason = ""
	state.snap.StartedAt = ""
	state.snap.CompletedAt = ""
	state.startBps = time.Time{}
	state.startByt = 0
	state.done = make(chan struct{})
	m.order = append(m.order, id)
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
	m.mu.Unlock()
	return nil
}

// Remove drops a terminal job from the manager entirely.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	state, ok := m.all[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	switch state.snap.Status {
	case StatusComplete, StatusFailed, StatusCanceled:
	default:
		m.mu.Unlock()
		return
	}
	delete(m.all, id)
	m.removeFromOrderLocked(id)
	m.emitQueueLocked()
	m.mu.Unlock()
}

// ClearTerminal removes every job in a terminal state.
func (m *Manager) ClearTerminal() {
	m.mu.Lock()
	for id, state := range m.all {
		switch state.snap.Status {
		case StatusComplete, StatusFailed, StatusCanceled:
			delete(m.all, id)
			m.removeFromOrderLocked(id)
		}
	}
	m.emitQueueLocked()
	m.mu.Unlock()
}

// Find returns a snapshot by id.
func (m *Manager) Find(id string) (JobSnapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.all[id]
	if !ok {
		return JobSnapshot{}, false
	}
	return state.snap, true
}

// Active returns the currently running job id, if any.
func (m *Manager) Active() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// maybeStartNextLocked launches the head of the pending queue if no job
// is currently running. Caller must hold m.mu.
func (m *Manager) maybeStartNextLocked() {
	if m.active != "" {
		return
	}
	for len(m.order) > 0 {
		id := m.order[0]
		state, ok := m.all[id]
		if !ok {
			m.order = m.order[1:]
			continue
		}
		if state.snap.Status != StatusPending {
			m.order = m.order[1:]
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		state.ctx = ctx
		state.cancel = cancel
		state.snap.Status = StatusActive
		state.snap.StartedAt = time.Now().UTC().Format(time.RFC3339)
		state.snap.Message = "Preparing"
		m.active = id
		m.order = m.order[1:]
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		go m.run(state)
		return
	}
}

func (m *Manager) run(state *jobState) {
	defer close(state.done)

	m.mu.Lock()
	req := engine.Request{
		URL:            state.snap.URL,
		OutputDir:      state.snap.OutputDir,
		Format:         state.snap.Quality.ytdlpFormat(),
		OutputTemplate: state.snap.Quality.outputTemplate(),
		Overwrite:      true,
		Playlist:       engine.PlaylistOptions{Disabled: true},
		Filesystem: engine.FilesystemOptions{
			FfmpegLocation: m.ffmpegLocation,
		},
	}
	ctx := state.ctx
	runner := m.runDownload
	m.mu.Unlock()

	handler := func(ctx context.Context, ev engine.Event) error {
		m.handleEvent(state, ev)
		return nil
	}

	result, err := runner(ctx, req, handler)

	m.mu.Lock()
	defer m.mu.Unlock()

	if state.ctx.Err() != nil {
		state.snap.Status = StatusCanceled
		state.snap.Message = "Canceled"
		state.snap.ErrorReason = "canceled"
	} else if err != nil {
		state.snap.Status = StatusFailed
		state.snap.Message = humanError(err)
		state.snap.ErrorReason = errorReason(err)
		if state.snap.Bytes == 0 {
			state.snap.Progress = 0
		} else if state.snap.Total > 0 {
			state.snap.Progress = clampFloat(float64(state.snap.Bytes) / float64(state.snap.Total))
		} else {
			state.snap.Progress = 0
		}
	} else {
		state.snap.Status = StatusComplete
		state.snap.Message = "Completed"
		state.snap.Progress = 1
		if result.Filename != "" {
			state.snap.Filename = filepath.Base(result.Filename)
			if abs, absErr := filepath.Abs(result.Filename); absErr == nil {
				state.snap.AbsolutePath = abs
			}
			if state.snap.Bytes == 0 {
				state.snap.Bytes = result.Bytes
			}
		}
		if state.snap.Bytes == 0 {
			state.snap.Bytes = result.Bytes
		}
		if state.snap.Title == "" {
			state.snap.Title = state.snap.Filename
		}
	}
	state.snap.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	m.active = ""
	m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	m.maybeStartNextLocked()
	m.emitQueueLocked()
}

func (m *Manager) handleEvent(state *jobState, ev engine.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state.ctx != nil && state.ctx.Err() != nil && state.snap.Status == StatusActive {
		return
	}

	switch ev.Kind {
	case engine.EventDownloadStarting:
		state.snap.Message = "Starting download"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventDownloadProgress:
		if ev.Bytes > 0 {
			state.snap.Bytes = ev.Bytes
		}
		if ev.Total > 0 {
			state.snap.Total = ev.Total
		}
		if state.snap.Total > 0 {
			state.snap.Progress = clampFloat(float64(state.snap.Bytes) / float64(state.snap.Total))
		}
		// ETA / speed are computed locally from a short rolling window.
		now := time.Now()
		if state.startBps.IsZero() {
			state.startBps = now
			state.startByt = state.snap.Bytes
		}
		elapsed := now.Sub(state.startBps).Seconds()
		if elapsed >= 0.5 {
			delta := state.snap.Bytes - state.startByt
			if delta > 0 {
				state.snap.SpeedBps = float64(delta) / elapsed
				if state.snap.Total > 0 && state.snap.SpeedBps > 0 {
					remaining := float64(state.snap.Total - state.snap.Bytes)
					state.snap.ETASeconds = remaining / state.snap.SpeedBps
				}
			}
			state.startBps = now
			state.startByt = state.snap.Bytes
		}
		state.snap.Message = "Downloading"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventDownloadRetry, engine.EventExtractorRetry:
		state.snap.Message = "Retrying"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventDownloadCompleted:
		if ev.Bytes > 0 {
			state.snap.Bytes = ev.Bytes
		}
		state.snap.Message = "Finalising"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventPostprocessStarting, engine.EventPostprocessProgress:
		state.snap.Message = "Finalising"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	case engine.EventDownloadCancelled:
		state.snap.Message = "Canceled"
		m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
	default:
		if ev.Message != "" {
			state.snap.Message = humanMessage(ev.Message)
			m.emitLocked(Event{Name: EventJobUpdate, Job: state.snap})
		}
	}
}

// emitLocked queues an immutable event for ordered delivery. Caller must hold
// m.mu. The bounded buffer absorbs normal progress bursts; backpressure is
// preferable to silently dropping the latest queue state.
func (m *Manager) emitLocked(ev Event) {
	if m.listener == nil {
		return
	}
	m.events <- ev
}

// emitQueueLocked emits a queue:update event with the current display
// list. Caller must hold m.mu.
func (m *Manager) emitQueueLocked() {
	if m.listener == nil {
		return
	}
	ev := Event{Name: EventQueue, Queue: m.snapshotLocked()}
	go m.listener(ev)
}

func (m *Manager) snapshotLocked() []JobSnapshot {
	out := make([]JobSnapshot, 0, len(m.all))
	for _, state := range m.all {
		out = append(out, state.snap)
	}
	sort.Slice(out, func(i, j int) bool {
		// Active first, then pending in creation order, then terminal
		// jobs by completed-at descending.
		ri, rj := statusRank(out[i].Status), statusRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		if out[i].Status == StatusActive || out[i].Status == StatusPending {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].CompletedAt > out[j].CompletedAt
	})
	return out
}

func (m *Manager) removeFromOrderLocked(id string) {
	for i, candidate := range m.order {
		if candidate == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

func statusRank(s Status) int {
	switch s {
	case StatusActive:
		return 0
	case StatusPending:
		return 1
	case StatusFailed:
		return 2
	case StatusCanceled:
		return 3
	case StatusComplete:
		return 4
	}
	return 5
}

func ensureDir(dir string) error {
	if dir == "" {
		return nil
	}
	if strings.HasPrefix(dir, "~") {
		if home, err := userHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	return mkdirAll(dir, 0o755)
}

func humanError(err error) string {
	if err == nil {
		return "Failed"
	}
	var typed *engine.Error
	if errors.As(err, &typed) {
		switch typed.Category {
		case engine.ErrorUnsupported:
			if isYouTubeChallengeTimeout(err) {
				return "YouTube challenge timed out — retry"
			}
			return "This link is not supported"
		case engine.ErrorAuthentication:
			return "Sign-in is required for this video"
		case engine.ErrorInvalidInput:
			return "The link is not valid"
		case engine.ErrorNetwork:
			return "Network error"
		case engine.ErrorCancelled:
			return "Canceled"
		case engine.ErrorSecurity:
			return "The download was blocked by a security check"
		}
	}
	return humanMessage(err.Error())
}

func isYouTubeChallengeTimeout(err error) bool {
	message := err.Error()
	return strings.Contains(message, "JavaScript challenge solver unavailable") &&
		strings.Contains(message, "EJS helper timeout") &&
		strings.Contains(message, "JavaScript execution timed out")
}

func humanMessage(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > 240 {
		return s[:237] + "..."
	}
	return s
}

func errorReason(err error) string {
	var typed *engine.Error
	if errors.As(err, &typed) {
		return string(typed.Category)
	}
	return "internal"
}

func clampFloat(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func isKnownQuality(quality Quality) bool {
	for _, candidate := range AllQualities {
		if candidate == quality {
			return true
		}
	}
	return false
}

// InfoSummary is the metadata displayed on the Home page after analyse.
type InfoSummary struct {
	Title     string `json:"title"`
	Channel   string `json:"channel"`
	Duration  string `json:"duration"`
	Thumbnail string `json:"thumbnail"`
	VideoID   string `json:"videoId"`
	URL       string `json:"url"`
}

// Analyze calls engine.Run with Simulate=true to extract metadata for
// the Home page preview. It uses NoPlaylist so watch?v=...&list= links
// still surface as a single video.
func (m *Manager) Analyze(ctx context.Context, rawURL string) (InfoSummary, error) {
	if rawURL == "" {
		return InfoSummary{}, errors.New("analyze: empty url")
	}
	req := engine.Request{
		URL:      rawURL,
		Simulate: true,
		Playlist: engine.PlaylistOptions{Disabled: true},
		Filesystem: engine.FilesystemOptions{
			FfmpegLocation: m.ffmpegLocationSnapshot(),
		},
	}
	result, err := m.client.Run(ctx, req)
	if err != nil {
		return InfoSummary{}, err
	}
	var info map[string]any
	if len(result.InfoJSON) > 0 {
		_ = json.Unmarshal(result.InfoJSON, &info)
	}
	summary := InfoSummary{URL: rawURL}
	if v, ok := info["id"].(string); ok {
		summary.VideoID = v
	}
	if v, ok := info["title"].(string); ok {
		summary.Title = v
	}
	if v, ok := info["channel"].(string); ok {
		summary.Channel = v
	} else if v, ok := info["uploader"].(string); ok {
		summary.Channel = v
	}
	if v, ok := info["duration"].(float64); ok {
		summary.Duration = formatDuration(int64(v))
	} else if v, ok := info["duration_string"].(string); ok {
		summary.Duration = v
	}
	if v, ok := info["thumbnail"].(string); ok {
		summary.Thumbnail = v
	}
	return summary, nil
}

// formatDuration renders seconds as a human-readable label.
func formatDuration(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
