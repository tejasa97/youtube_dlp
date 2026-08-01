package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestOutputArtifactFlagsAndIDOrdering(t *testing.T) {
	request := captureCLIRequest(t, "--id")
	if !request.UseID || request.OutputTemplate != "" {
		t.Fatalf("--id request=%+v", request)
	}
	request = captureCLIRequest(t, "--id", "--output", "%(title)s.%(ext)s")
	if request.UseID {
		t.Fatalf("explicit output did not override --id: %+v", request)
	}
	request = captureCLIRequest(t, "--id", "--output", "infojson:%(id)s.info.json")
	if !request.UseID || request.OutputTemplates[ytdlp.OutputTemplateInfoJSON] == "" {
		t.Fatalf("typed output unexpectedly disabled --id: %+v", request)
	}
	request = captureCLIRequest(t, "--output-na-placeholder", "missing", "--autonumber-start", "7", "--autonumber-size", "3", "--clean-info-json")
	if request.Filesystem.OutputNaPlaceholder != "missing" || request.AutonumberStart != 7 || request.AutonumberSize != 3 || request.RelatedFiles.CleanInfoJSON == nil || !*request.RelatedFiles.CleanInfoJSON {
		t.Fatalf("output artifact request=%+v", request)
	}
	request = captureCLIRequest(t, "--no-clean-info-json")
	if request.RelatedFiles.CleanInfoJSON == nil || *request.RelatedFiles.CleanInfoJSON {
		t.Fatalf("--no-clean-info-json request=%+v", request)
	}
}

func TestLoadInfoJSONAndRemoveCacheCLIRequestsAreURLIndependent(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), "loaded.info.json")
	if err := os.WriteFile(infoPath, []byte(`{"id":"x","title":"X","url":"https://example.invalid/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := captureCLIRequest(t, "--load-info-json", infoPath)
	if request.LoadInfoJSON != infoPath || request.URL != "" {
		t.Fatalf("load-info request=%+v", request)
	}
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	request = captureCLIRequest(t, "--cache-dir", cacheRoot, "--rm-cache-dir")
	if !request.RemoveCacheDir || request.CacheDir != cacheRoot || request.URL != "" {
		t.Fatalf("remove-cache request=%+v", request)
	}
}

type artifactAutonumberRunner struct {
	requests []ytdlp.Request
}

func (runner *artifactAutonumberRunner) Run(_ context.Context, request ytdlp.Request) (ytdlp.Result, error) {
	runner.requests = append(runner.requests, request)
	return ytdlp.Result{AutonumberCount: 1}, nil
}

func TestCLIPropagatesAutonumberAcrossMultipleInputs(t *testing.T) {
	runner := &artifactAutonumberRunner{}
	var stdout, stderr bytes.Buffer
	code := runContextIOWithDependencies(context.Background(), []string{
		"--autonumber-start", "9", "--autonumber-size", "4", "https://example.invalid/one", "https://example.invalid/two",
	}, nil, &stdout, &stderr, runDependencies{newRunner: func([]ytdlp.Option) cliRunner { return runner }})
	if code != 0 || len(runner.requests) != 2 {
		t.Fatalf("code=%d requests=%+v stderr=%q", code, runner.requests, stderr.String())
	}
	if runner.requests[0].AutonumberIndex != 0 || runner.requests[1].AutonumberIndex != 1 {
		t.Fatalf("autonumber indexes=%d,%d", runner.requests[0].AutonumberIndex, runner.requests[1].AutonumberIndex)
	}
}
