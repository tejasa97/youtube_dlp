package engine_test

import (
	"context"
	"testing"

	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/internal/enginetest"
)

func TestCycleFreeRootEngineTestHarness(t *testing.T) {
	bundle := engine.Compose(
		func(label string) []engine.Provider[enginetest.Request] {
			return []engine.Provider[enginetest.Request]{enginetest.Provider{
				ProviderName: "fixture", Host: "fixture.example",
			}}
		},
		func(operation engine.Operation, label string) enginetest.Request {
			return enginetest.Request{Request: operation.Request, Label: label}
		},
		engine.Hooks[string]{},
	)
	runtime, err := bundle.NewRuntime("cycle-free")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := runtime.Select("https://fixture.example/watch")
	if err != nil {
		t.Fatal(err)
	}
	result, err := selected.Extract(context.Background(), engine.Operation{
		Request: engine.ProviderRequest{URL: "https://fixture.example/watch"},
	}, "cycle-free")
	if err != nil {
		t.Fatal(err)
	}
	if title, ok := result.Info.Title(); !ok || title != "cycle-free" {
		t.Fatalf("title=%q ok=%v", title, ok)
	}
}
