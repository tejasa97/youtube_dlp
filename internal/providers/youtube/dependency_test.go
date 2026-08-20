package youtube

import (
	"os/exec"
	"strings"
	"testing"
)

func TestProviderDependencyClosureExcludesMixedExtractorPackage(t *testing.T) {
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list provider dependencies: %v\n%s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if dependency == "github.com/tejasa97/ytdlp-go/internal/extractor" {
			t.Fatal("internal/providers/youtube depends on mixed internal/extractor")
		}
	}
}
