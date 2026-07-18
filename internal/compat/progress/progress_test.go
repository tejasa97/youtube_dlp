package progress

import (
	"errors"
	"testing"
	"time"
)

func TestRender(t *testing.T) {
	got, err := Render("%(status)s %(progress._percent_str)s %(progress._speed_str)s %(progress._eta_str)s", Snapshot{Status: "downloading", DownloadedBytes: 512, TotalBytes: 1024, Speed: 1024, ETA: 65 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got != "downloading   50.0% 1.0KiB/s 01:05" {
		t.Fatalf("got %q", got)
	}
}
func TestRejectInvalid(t *testing.T) {
	if _, err := Render("", Snapshot{}); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Render("%(status)s", Snapshot{DownloadedBytes: -1}); !errors.Is(err, ErrInvalidProgress) {
		t.Fatalf("err=%v", err)
	}
}
func FuzzRender(f *testing.F) {
	f.Add("%(progress._percent_str)s")
	f.Fuzz(func(t *testing.T, pattern string) { _, _ = Render(pattern, Snapshot{TotalBytes: 1}) })
}
