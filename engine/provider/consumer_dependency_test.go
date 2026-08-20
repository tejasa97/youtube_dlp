package provider_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestProviderConsumersExcludeRootEngineCycle(t *testing.T) {
	tests := []struct {
		packagePath string
		prohibited  []string
	}{
		{
			packagePath: "github.com/tejasa97/ytdlp-go/internal/javascript/ejs",
			prohibited:  []string{"github.com/tejasa97/ytdlp-go/engine"},
		},
		{
			packagePath: "github.com/tejasa97/ytdlp-go/internal/youtubepot",
			prohibited:  []string{"github.com/tejasa97/ytdlp-go/engine"},
		},
		{
			packagePath: "github.com/tejasa97/ytdlp-go/internal/providers/youtube",
			prohibited: []string{
				"github.com/tejasa97/ytdlp-go/engine",
				"github.com/tejasa97/ytdlp-go/internal/extraction",
				"github.com/tejasa97/ytdlp-go/internal/extractor",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.packagePath, func(t *testing.T) {
			command := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", test.packagePath)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("go list dependencies: %v\n%s", err, output)
			}
			dependencies := make(map[string]bool)
			for _, dependency := range strings.Fields(string(output)) {
				dependencies[dependency] = true
			}
			for _, prohibited := range test.prohibited {
				if dependencies[prohibited] {
					t.Fatalf("dependency graph contains prohibited package %q", prohibited)
				}
			}
		})
	}
}
