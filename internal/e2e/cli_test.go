package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestCLIProcessWalkingSkeleton(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", "../../cmd/ytdlp-go",
		"--quiet", "--print-json", "--output-dir", root, server.URL+"/page")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("CLI process: %v; stderr = %s", err, stderr.String())
	}
	if !json.Valid(bytes.TrimSpace(stdout.Bytes())) {
		t.Fatalf("CLI stdout is not JSON: %q", stdout.String())
	}
	downloaded, err := os.ReadFile(filepath.Join(root, "Deterministic Fixture.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloaded, server.Media()) {
		t.Fatal("CLI process downloaded different bytes")
	}
}
