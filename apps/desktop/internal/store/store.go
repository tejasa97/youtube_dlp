// Package store persists user settings and a minimal download history to
// disk under the desktop app's per-user data directory.
//
// Persistence is intentionally separate from pkg/ytdlp so the core
// library stays free of UI state and OS-specific paths.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Settings is the JSON-serialized user settings document.
type Settings struct {
	DownloadFolder string `json:"downloadFolder"`
	FFmpegPath     string `json:"ffmpegPath"`
	WindowWidth    int    `json:"windowWidth"`
	WindowHeight   int    `json:"windowHeight"`
}

// HistoryEntry is one completed download shown in the Downloads page.
type HistoryEntry struct {
	ID            string `json:"id"`
	VideoID       string `json:"videoId"`
	Title         string `json:"title"`
	Channel       string `json:"channel"`
	Quality       string `json:"quality"`
	Filename      string `json:"filename"`
	AbsolutePath  string `json:"absolutePath"`
	SizeBytes     int64  `json:"sizeBytes"`
	CompletedAt   string `json:"completedAt"`
	DurationLabel string `json:"durationLabel"`
	Thumbnail     string `json:"thumbnail,omitempty"`
}

// State bundles settings and history. The store holds a copy in memory
// and writes through to disk on every mutation. The data set is small
// (settings plus a bounded history) so a single JSON file is fine.
type State struct {
	Settings Settings       `json:"settings"`
	History  []HistoryEntry `json:"history"`
}

// Store is the thread-safe owner of the persisted State.
type Store struct {
	path  string
	mu    sync.RWMutex
	state State
}

// NewEphemeral returns a fully functional in-memory store. It is used only as
// a last-resort Desktop fallback when the operating-system config directory
// cannot be opened. Mutations remain available for the current process but are
// not persisted.
func NewEphemeral() *Store { return &Store{state: defaultState()} }

// Open loads (or initialises) the store at the given file path. If the
// directory does not exist it is created. If the file is missing or
// corrupt the default state is written and returned.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create dir: %w", err)
	}
	s := &Store{path: path, state: defaultState()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.writeLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.state); err != nil {
			// Corrupt file: start fresh but keep the bad file as a
			// .bak for forensics. The app must keep working.
			_ = os.Rename(path, path+".bak")
			s.state = defaultState()
			if werr := s.writeLocked(); werr != nil {
				return nil, werr
			}
		}
	}
	return s, nil
}

func defaultState() State {
	return State{
		Settings: Settings{
			DownloadFolder: defaultDownloadDir(),
			WindowWidth:    1180,
			WindowHeight:   760,
		},
		History: []HistoryEntry{},
	}
}

// DefaultPath returns the per-user JSON file path used when no explicit
// path is given. It honours the OS conventions via os.UserConfigDir.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ytdlp-desktop", "state.json"), nil
}

func defaultDownloadDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Downloads", "ytdlp-desktop")
	}
	return filepath.Join(os.TempDir(), "ytdlp-desktop")
}

// Settings returns a copy of the current settings.
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Settings
}

// SetSettings replaces the settings atomically and persists the result.
func (s *Store) SetSettings(next Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Settings = next
	return s.writeLocked()
}

// History returns a copy of the stored history in newest-first order.
func (s *Store) History() []HistoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]HistoryEntry, len(s.state.History))
	copy(out, s.state.History)
	sort.Slice(out, func(i, j int) bool { return out[i].CompletedAt > out[j].CompletedAt })
	return out
}

// AppendHistory adds one entry to history and trims the list to the
// newest MaxHistoryItems.
func (s *Store) AppendHistory(entry HistoryEntry) error {
	if entry.CompletedAt == "" {
		entry.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.History = append([]HistoryEntry{entry}, s.state.History...)
	if len(s.state.History) > MaxHistoryItems {
		s.state.History = s.state.History[:MaxHistoryItems]
	}
	return s.writeLocked()
}

// MaxHistoryItems caps the on-disk history. Older entries are dropped.
const MaxHistoryItems = 200

// RemoveHistory drops a history entry by id. Returns true if found.
func (s *Store) RemoveHistory(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, h := range s.state.History {
		if h.ID == id {
			s.state.History = append(s.state.History[:i], s.state.History[i+1:]...)
			return true, s.writeLocked()
		}
	}
	return false, nil
}

// ClearHistory removes every history entry.
func (s *Store) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.History = []HistoryEntry{}
	return s.writeLocked()
}

// writeLocked serialises state and writes it atomically. The caller must
// hold s.mu for writing.
func (s *Store) writeLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("store: write tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	return nil
}
