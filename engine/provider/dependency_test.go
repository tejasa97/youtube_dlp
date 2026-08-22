package provider_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCycleFreeProviderPackageDependencies(t *testing.T) {
	command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/tejasa97/ytdlp-go/engine/provider")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list provider dependencies: %v\n%s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if dependency == "github.com/tejasa97/ytdlp-go/engine" ||
			strings.HasPrefix(dependency, "github.com/tejasa97/ytdlp-go/internal/") {
			t.Fatalf("cycle-free provider package depends on prohibited package %q", dependency)
		}
	}
}
