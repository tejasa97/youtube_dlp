package conformance

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var forbiddenPythonInvocation = []*regexp.Regexp{
	regexp.MustCompile(`exec\.Command(?:Context)?\([^\n,]*["']python(?:3)?["']`),
	regexp.MustCompile(`exec\.LookPath\(["']python(?:3)?["']\)`),
	regexp.MustCompile(`os\.Executable\([^)]*python`),
}

// TestProductionSourcesDoNotInvokePython is a source-level tripwire. The
// Docker gate remains authoritative for the built artifact, while this test
// gives a direct review failure if production Go code hard-codes a Python
// executable or cgo bridge.
func TestProductionSourcesDoNotInvokePython(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	for _, root := range []string{"cmd", "internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(repository, root), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(data)
			if strings.Contains(text, `import "C"`) {
				t.Errorf("%s imports cgo", path)
			}
			for _, pattern := range forbiddenPythonInvocation {
				if pattern.MatchString(text) {
					t.Errorf("%s contains a Python invocation matching %s", path, pattern)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	dockerfile, err := os.ReadFile(filepath.Join(repository, ".github", "python-free.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	if !strings.Contains(text, "RUN ! command -v python && ! command -v python3") || !strings.Contains(text, "FROM scratch") {
		t.Fatal("Python-free Dockerfile must prove absent interpreters and use a scratch runtime")
	}
}
