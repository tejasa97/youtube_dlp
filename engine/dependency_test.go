package engine_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPublicEngineContractsExcludeInternalDependencies(t *testing.T) {
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/tejasa97/ytdlp-go/engine")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list engine dependencies: %v\n%s", err, output)
	}
	prohibited := []string{
		"github.com/tejasa97/ytdlp-go/pkg/ytdlp",
		"github.com/tejasa97/ytdlp-go/internal/extractor",
		"github.com/tejasa97/ytdlp-go/internal/providers/",
		"github.com/tejasa97/ytdlp-go/internal/javascript/ejs",
		"github.com/tejasa97/ytdlp-go/internal/youtubepot",
	}
	for _, dependency := range strings.Fields(string(output)) {
		if dependency == prohibited[0] || dependency == prohibited[1] ||
			strings.HasPrefix(dependency, prohibited[2]) ||
			dependency == prohibited[3] || dependency == prohibited[4] {
			t.Fatalf("public engine contract depends on prohibited package %q", dependency)
		}
	}
}
