package ytdlp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestAlphaAPIContractCompilesAndCategorizesCancellation(t *testing.T) {
	var runner ytdlp.Runner = ytdlp.NewClient()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.Run(ctx, ytdlp.Request{URL: "https://example.invalid/media.mp4", SkipDownload: true})
	if !errors.Is(err, context.Canceled) || !ytdlp.IsCategory(err, ytdlp.ErrorCancelled) {
		t.Fatalf("Run() error = %v", err)
	}
	if ytdlp.APIVersion != "v1alpha1" || len(ytdlp.CompatibilityReferenceCommit) != 40 {
		t.Fatalf("API metadata = %q %q", ytdlp.APIVersion, ytdlp.CompatibilityReferenceCommit)
	}
}

func TestAlphaEventJSONContract(t *testing.T) {
	event := ytdlp.Event{
		Kind: ytdlp.EventDownloadProgress, Extractor: "fixture", URL: "https://media.example/video",
		Path: "video.mp4", Bytes: 4, Total: 8, Attempt: 2, Resuming: true, Fragment: 1, Fragments: 3,
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"download_progress","extractor":"fixture","url":"https://media.example/video","path":"video.mp4","bytes":4,"total":8,"attempt":2,"resuming":true,"fragment":1,"fragments":3}`
	if string(encoded) != want {
		t.Fatalf("event JSON = %s", encoded)
	}
}
