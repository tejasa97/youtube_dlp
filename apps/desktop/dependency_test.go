package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDesktopProductionDependenciesStayFocusedOnYouTube(t *testing.T) {
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list Desktop dependencies: %v\n%s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		switch {
		case dependency == "github.com/tejasa97/ytdlp-go/pkg/ytdlp":
			t.Fatalf("Desktop reaches broad compatibility facade %q", dependency)
		case dependency == "github.com/tejasa97/ytdlp-go/internal/extractor":
			t.Fatalf("Desktop reaches mixed extractor package %q", dependency)
		case strings.HasPrefix(dependency, "github.com/tejasa97/ytdlp-go/internal/providers/") &&
			dependency != "github.com/tejasa97/ytdlp-go/internal/providers/youtube":
			t.Fatalf("Desktop reaches non-YouTube provider package %q", dependency)
		}
	}
}
