package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/ytdlp-go/pkg/ytdlp"
)

func TestRunRoutingControlsWireToRequest(t *testing.T) {
	request := captureCLIRequest(t, "--force-generic-extractor", "--default-search", "scsearch5:")
	if !request.ForceGenericExtractor || request.DefaultSearch != "scsearch5:" {
		t.Fatalf("request=%+v", request)
	}
}

type routingBatchRunner struct {
	mu       sync.Mutex
	requests []ytdlp.Request
}

func (runner *routingBatchRunner) Run(_ context.Context, request ytdlp.Request) (ytdlp.Result, error) {
	runner.mu.Lock()
	runner.requests = append(runner.requests, request)
	runner.mu.Unlock()
	return ytdlp.Result{}, nil
}

func TestRunRoutingControlsPreserveBatchInputs(t *testing.T) {
	path := t.TempDir() + "/inputs.txt"
	if err := os.WriteFile(path, []byte("first search\nsecond search\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &routingBatchRunner{}
	var stdout, stderr bytes.Buffer
	code := runContextIOWithDependencies(context.Background(), []string{
		"--force-generic-extractor", "--default-search", "auto", "--batch-file", path,
	}, strings.NewReader(""), &stdout, &stderr, runDependencies{newRunner: func([]ytdlp.Option) cliRunner { return runner }})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.requests) != 2 || runner.requests[0].URL != "first search" || runner.requests[1].URL != "second search" {
		t.Fatalf("requests=%+v", runner.requests)
	}
	for _, request := range runner.requests {
		if !request.ForceGenericExtractor || request.DefaultSearch != "auto" {
			t.Fatalf("routing controls lost in request=%+v", request)
		}
	}
}
