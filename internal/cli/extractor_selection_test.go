package cli

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestRunExtractorSelectionFlagsPreservePinnedRuleOrderAndAliases(t *testing.T) {
	request := captureCLIRequest(t, "--use-extractors", "default,-generic", "--ies", "YouTube.*, end")
	want := []string{"default", "-generic", "YouTube.*", "end"}
	if !reflect.DeepEqual(request.ExtractorSelection.Rules, want) {
		t.Fatalf("rules = %#v, want %#v", request.ExtractorSelection.Rules, want)
	}
}

func TestRunExtractorSelectionEmptyRulesNormalizeToDefault(t *testing.T) {
	request := captureCLIRequest(t, "--ies", ",,")
	if len(request.ExtractorSelection.Rules) != 0 {
		t.Fatalf("selection = %+v", request.ExtractorSelection)
	}
}

func TestRunExtractorSelectionEmptyRulesUseDefaultRouting(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--ies", ",,", "--skip-download", "--print-json", server.URL + "/page"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Deterministic Fixture") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunExtractorSelectionValidationFailsBeforeRunnerNetwork(t *testing.T) {
	var out, errout strings.Builder
	runnerCalled := false
	code := runContextIOWithDependencies(context.Background(), []string{"--ies", "[malformed", "https://example.invalid/video"}, strings.NewReader(""), &out, &errout, runDependencies{
		newRunner: func([]ytdlp.Option) cliRunner {
			runnerCalled = true
			return &captureCLIRunner{}
		},
	})
	if code == 0 || runnerCalled || !strings.Contains(errout.String(), "invalid extractor selection") {
		t.Fatalf("code=%d runnerCalled=%v stderr=%q", code, runnerCalled, errout.String())
	}
}
