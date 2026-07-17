package format

import (
	"errors"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestBestSelectsFirstDownloadableFormat(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.String("invalid"),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("best")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/media")},
			value.Field{Key: "ext", Value: value.String("mp4")},
		)),
	)}))
	selected, err := Best(info)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "best" || selected.Ext != "mp4" {
		t.Fatalf("selection = %#v", selected)
	}
}

func TestBestRejectsMissingFormats(t *testing.T) {
	if _, err := Best(value.NewInfo(nil)); !errors.Is(err, ErrNoFormats) {
		t.Fatalf("Best() error = %v", err)
	}
}
