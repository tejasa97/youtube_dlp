package youtube_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPublicYouTubeCompositionExcludesMixedExtractorPackage(t *testing.T) {
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/tejasa97/ytdlp-go/providers/youtube")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list public YouTube composition dependencies: %v\n%s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if dependency == "github.com/tejasa97/ytdlp-go/internal/extractor" {
			t.Fatal("public YouTube composition depends on mixed internal/extractor")
		}
	}
}
