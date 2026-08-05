package engine_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPublicEngineContractsExcludeInternalDependencies(t *testing.T) {
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/tejasa97/youtube_dlp/engine")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list engine dependencies: %v\n%s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if strings.HasPrefix(dependency, "github.com/tejasa97/youtube_dlp/internal/") {
			t.Fatalf("public engine contract depends on prohibited package %q", dependency)
		}
	}
}
