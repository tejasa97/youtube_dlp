// Package progress renders deterministic yt-dlp-style progress templates.
package progress

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const maxTemplateBytes = 16 << 10

var ErrInvalidProgress = errors.New("invalid progress template")

// Snapshot is deliberately clock-free. Callers supply elapsed/speed/ETA so a
// render is repeatable in tests and usable by terminal and JSON frontends.
type Snapshot struct {
	Status             string
	Filename           string
	DownloadedBytes    int64
	TotalBytes         int64
	TotalBytesEstimate int64
	Elapsed            time.Duration
	Speed              float64
	ETA                time.Duration
}

// Render supports yt-dlp output-template expressions plus the progress
// namespace (for example %(progress._percent_str)s).
func Render(pattern string, snapshot Snapshot) (string, error) {
	if len(pattern) == 0 || len(pattern) > maxTemplateBytes {
		return "", fmt.Errorf("%w: empty or oversized template", ErrInvalidProgress)
	}
	if snapshot.DownloadedBytes < 0 || snapshot.TotalBytes < 0 || snapshot.TotalBytesEstimate < 0 || snapshot.Elapsed < 0 || snapshot.ETA < 0 || math.IsNaN(snapshot.Speed) || math.IsInf(snapshot.Speed, 0) || snapshot.Speed < 0 {
		return "", fmt.Errorf("%w: invalid snapshot", ErrInvalidProgress)
	}
	return template.Render(pattern, snapshot.info())
}

func (s Snapshot) info() value.Info {
	total := s.TotalBytes
	if total == 0 {
		total = s.TotalBytesEstimate
	}
	percent := "NA"
	if total > 0 {
		percent = fmt.Sprintf("%6.1f%%", 100*float64(s.DownloadedBytes)/float64(total))
	}
	eta := "NA"
	if s.ETA > 0 {
		eta = formatDuration(s.ETA)
	}
	speed := "NA"
	if s.Speed > 0 {
		speed = formatBytes(s.Speed) + "/s"
	}
	return value.NewInfo(value.NewObject(
		value.Field{Key: "status", Value: value.String(s.Status)}, value.Field{Key: "filename", Value: value.String(s.Filename)},
		value.Field{Key: "downloaded_bytes", Value: value.Int(s.DownloadedBytes)}, value.Field{Key: "total_bytes", Value: value.Int(s.TotalBytes)}, value.Field{Key: "total_bytes_estimate", Value: value.Int(s.TotalBytesEstimate)},
		value.Field{Key: "elapsed", Value: value.Float(s.Elapsed.Seconds())}, value.Field{Key: "speed", Value: value.Float(s.Speed)}, value.Field{Key: "eta", Value: value.Float(s.ETA.Seconds())},
		value.Field{Key: "progress", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "_percent_str", Value: value.String(percent)}, value.Field{Key: "_speed_str", Value: value.String(speed)}, value.Field{Key: "_eta_str", Value: value.String(eta)}, value.Field{Key: "_elapsed_str", Value: value.String(formatDuration(s.Elapsed))}, value.Field{Key: "_total_bytes_str", Value: value.String(formatBytes(float64(total)))}, value.Field{Key: "_downloaded_bytes_str", Value: value.String(formatBytes(float64(s.DownloadedBytes)))},
		))},
	))
}
func formatDuration(input time.Duration) string {
	seconds := int64(input.Round(time.Second).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%02d:%02d", seconds/60, seconds%60)
}
func formatBytes(input float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	index := 0
	for input >= 1024 && index < len(units)-1 {
		input /= 1024
		index++
	}
	if index == 0 {
		return strconv.FormatInt(int64(input), 10) + "B"
	}
	return fmt.Sprintf("%.1f%s", input, units[index])
}
