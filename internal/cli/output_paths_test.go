package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/testserver"
	"github.com/tejasa97/youtube_dlp/pkg/ytdlp"
)

func TestParseOutputPathSpecification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		types []ytdlp.OutputPathType
		path  string
		fail  bool
	}{
		{input: "downloads", types: []ytdlp.OutputPathType{ytdlp.OutputPathHome}, path: "downloads"},
		{input: "home:downloads", types: []ytdlp.OutputPathType{ytdlp.OutputPathHome}, path: "downloads"},
		{input: "subtitle,thumbnail:sidecars", types: []ytdlp.OutputPathType{ytdlp.OutputPathSubtitle, ytdlp.OutputPathThumbnail}, path: "sidecars"},
		{input: `C:\Downloads`, types: []ytdlp.OutputPathType{ytdlp.OutputPathHome}, path: `C:\Downloads`},
		{input: "future:value", types: []ytdlp.OutputPathType{ytdlp.OutputPathHome}, path: "future:value"},
		{input: "temp:parts", fail: true},
		{input: "subtitle:", types: []ytdlp.OutputPathType{ytdlp.OutputPathSubtitle}},
		{input: "subtitle,subtitle:captions", types: []ytdlp.OutputPathType{ytdlp.OutputPathSubtitle, ytdlp.OutputPathSubtitle}, path: "captions"},
		{input: "", fail: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.input, func(t *testing.T) {
			types, path, err := parseOutputPathSpecification(test.input)
			if test.fail {
				if err == nil {
					t.Fatalf("accepted %q: types=%v path=%q", test.input, types, path)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(types, test.types) || path != test.path {
				t.Fatalf("parse(%q) types=%v path=%q error=%v", test.input, types, path, err)
			}
		})
	}
}

func TestHomePathFromArgsUsesSharedTypedParser(t *testing.T) {
	t.Parallel()
	args := []string{
		"--paths=subtitle:captions",
		"-P", "home,thumbnail:first",
		"--paths", `C:\Downloads`,
	}
	if got := homePathFromArgs(args); got != `C:\Downloads` {
		t.Fatalf("homePathFromArgs() = %q", got)
	}
}

func TestOutputPathFlagClearsTypedAndHomeValues(t *testing.T) {
	t.Parallel()
	home := "configured-home"
	value := outputPathFlag{
		home:   &home,
		values: ytdlp.OutputPaths{ytdlp.OutputPathSubtitle: "configured-captions"},
	}
	if err := value.Set("subtitle:"); err != nil {
		t.Fatal(err)
	}
	if _, present := value.values[ytdlp.OutputPathSubtitle]; present {
		t.Fatalf("empty typed value was not cleared: %#v", value.values)
	}
	value.values[ytdlp.OutputPathSubtitle] = "configured-captions"
	if err := value.Set("subtitle:."); err != nil {
		t.Fatal(err)
	}
	if _, present := value.values[ytdlp.OutputPathSubtitle]; present {
		t.Fatalf("dot typed value was not cleared: %#v", value.values)
	}
	if err := value.Set("home:"); err != nil {
		t.Fatal(err)
	}
	if home != "." {
		t.Fatalf("empty home reset = %q", home)
	}
}

func TestRunRoutesArtifactsThroughTypedOutputPaths(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--output-dir", root,
		"--paths", "subtitle:captions",
		"--paths", "infojson:metadata",
		"--paths", "link:links",
		"--skip-download", "--write-subs", "--write-info-json", "--write-url-link",
		server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, pattern := range []string{
		filepath.Join(root, "captions", "*.vtt"),
		filepath.Join(root, "metadata", "*.info.json"),
		filepath.Join(root, "links", "*.url"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			t.Fatalf("pattern %q matches=%v error=%v", pattern, matches, err)
		}
	}
}

func TestRunOutputPathPrecedenceAcrossConfigAndOutputDir(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	config := strings.Join([]string{
		"--skip-download",
		"--write-subs",
		"--paths subtitle:configured",
	}, "\n") + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--config-location", configPath,
		"--output-dir", root,
		"--paths", "subtitle:command",
		server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	commandMatches, err := filepath.Glob(filepath.Join(root, "command", "*.vtt"))
	if err != nil || len(commandMatches) == 0 {
		t.Fatalf("command matches=%v error=%v", commandMatches, err)
	}
	configuredMatches, err := filepath.Glob(filepath.Join(root, "configured", "*.vtt"))
	if err != nil || len(configuredMatches) != 0 {
		t.Fatalf("configured matches=%v error=%v", configuredMatches, err)
	}
}

func TestRunTypedOutputPathCanResetConfiguredValue(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	config := strings.Join([]string{
		"--skip-download",
		"--write-subs",
		"--paths subtitle:configured",
	}, "\n") + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for index, reset := range []string{"subtitle:", "subtitle:."} {
		t.Run(reset, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), string(rune('a'+index)))
			var stdout, stderr bytes.Buffer
			code := Run([]string{
				"--config-location", configPath,
				"--output-dir", root,
				"--paths", reset,
				server.URL + "/page",
			}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			homeMatches, err := filepath.Glob(filepath.Join(root, "*.vtt"))
			if err != nil || len(homeMatches) == 0 {
				t.Fatalf("home matches=%v error=%v", homeMatches, err)
			}
			configuredMatches, err := filepath.Glob(filepath.Join(root, "configured", "*.vtt"))
			if err != nil || len(configuredMatches) != 0 {
				t.Fatalf("configured matches=%v error=%v", configuredMatches, err)
			}
		})
	}
}

func FuzzParseOutputPathSpecification(f *testing.F) {
	f.Add("subtitle:captions")
	f.Add(`C:\Downloads`)
	f.Add("temp:parts")
	f.Fuzz(func(t *testing.T, specification string) {
		types, path, err := parseOutputPathSpecification(specification)
		if err != nil {
			return
		}
		if len(types) == 0 {
			t.Fatalf("accepted empty result: %#v %q", types, path)
		}
		for _, pathType := range types {
			if !supportedCLIOutputPathType(pathType) {
				t.Fatalf("accepted unsupported type %q", pathType)
			}
		}
	})
}
