package config

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadReferenceDerivedFixture(t *testing.T) {
	fixtureDir := filepath.Join("..", "..", "..", "conformance", "config")
	root, err := filepath.Abs(filepath.Join(fixtureDir, "root.conf"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Load(context.Background(), Request{CommandLine: []string{"--verbose"}, Explicit: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Arguments []string `json:"arguments_low_to_high"`
	}
	data, err := os.ReadFile(filepath.Join(fixtureDir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Arguments, expected.Arguments) {
		t.Fatalf("arguments mismatch:\n got %#v\nwant %#v", result.Arguments, expected.Arguments)
	}
	if got := sourceKinds(result.Sources); !reflect.DeepEqual(got, []SourceKind{SourceExplicit, SourceExplicit, SourceCommandLine}) {
		t.Fatalf("source order = %#v", got)
	}
}

func TestLoadDefaultPrecedenceAcrossGroups(t *testing.T) {
	root := t.TempDir()
	environment := Environment{
		Platform: PlatformLinux, HomeDir: filepath.Join(root, "home"), XDGConfigHome: filepath.Join(root, "xdg"),
		ExecutableDir: filepath.Join(root, "bin"), HomeConfigDir: filepath.Join(root, "downloads"),
		SystemConfigDir: filepath.Join(root, "etc"),
	}
	groups := DefaultGroups(environment)
	for _, group := range groups {
		path := group.Candidates[0].Path
		writeFixture(t, path, "--output "+string(group.Kind)+"\n--tag "+string(group.Kind)+"\n")
	}
	result, err := Load(context.Background(), Request{
		Environment: environment, IncludeDefaults: true,
		CommandLine: []string{"--output", "command", "--tag", "command"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []SourceKind{SourceSystem, SourceUser, SourceHome, SourcePortable, SourceCommandLine}
	if got := sourceKinds(result.Sources); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("source order = %#v, want %#v", got, wantKinds)
	}
	if got := lastOption(result.Arguments, "--output"); got != "command" {
		t.Fatalf("effective output = %q", got)
	}
	if got := optionValues(result.Arguments, "--tag"); !reflect.DeepEqual(got, []string{"system", "user", "home", "portable", "command"}) {
		t.Fatalf("repeated values = %#v", got)
	}
}

func TestLoadSelectsFirstExistingCandidateInEachGroup(t *testing.T) {
	root := t.TempDir()
	environment := Environment{Platform: PlatformLinux, HomeDir: filepath.Join(root, "home"), XDGConfigHome: filepath.Join(root, "xdg"), ExecutableDir: filepath.Join(root, "missing-bin"), HomeConfigDir: filepath.Join(root, "missing-home"), SystemConfigDir: filepath.Join(root, "missing-etc")}
	user := DefaultGroups(environment)[2]
	writeFixture(t, user.Candidates[1].Path, "--output first\n")
	writeFixture(t, user.Candidates[2].Path, "--output later\n")
	result, err := Load(context.Background(), Request{Environment: environment, IncludeDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := lastOption(result.Arguments, "--output"); got != "first" {
		t.Fatalf("output = %q", got)
	}
	if len(result.Sources) != 2 || result.Sources[0].Path != mustRealPath(t, user.Candidates[1].Path) {
		t.Fatalf("sources = %#v", result.Sources)
	}
}

func TestLoadExplicitPrecedenceRelativeDirectoriesAndDeduplication(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "outer.conf"), "--config-locations one.conf --config-locations nested --output outer\n")
	writeFixture(t, filepath.Join(root, "one.conf"), "--config-locations outer.conf --output one\n")
	writeFixture(t, filepath.Join(root, "nested", "yt-dlp.conf"), "--output two\n")
	result, err := Load(context.Background(), Request{Explicit: []string{filepath.Join(root, "outer.conf")}, CommandLine: []string{"--output", "command"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := lastOption(result.Arguments, "--output"); got != "command" {
		t.Fatalf("output = %q", got)
	}
	if got := optionValues(result.Arguments, "--output"); !reflect.DeepEqual(got, []string{"two", "one", "outer", "command"}) {
		t.Fatalf("precedence = %#v", got)
	}
	if got := sourceKinds(result.Sources); !reflect.DeepEqual(got, []SourceKind{SourceExplicit, SourceExplicit, SourceExplicit, SourceCommandLine}) {
		t.Fatalf("sources = %#v", got)
	}
}

func TestLoadIgnoreConfigFromCommandPortableAndSystem(t *testing.T) {
	t.Run("command", func(t *testing.T) {
		root := t.TempDir()
		env := Environment{Platform: PlatformLinux, ExecutableDir: root, HomeDir: root, XDGConfigHome: filepath.Join(root, "xdg"), SystemConfigDir: filepath.Join(root, "etc")}
		writeFixture(t, filepath.Join(root, "yt-dlp.conf"), "--output should-not-load")
		result, err := Load(context.Background(), Request{Environment: env, IncludeDefaults: true, CommandLine: []string{"--ignore-config"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Sources) != 1 {
			t.Fatalf("sources = %#v", result.Sources)
		}
	})
	t.Run("portable", func(t *testing.T) {
		root := t.TempDir()
		env := Environment{Platform: PlatformLinux, ExecutableDir: filepath.Join(root, "bin"), HomeConfigDir: filepath.Join(root, "homecfg"), HomeDir: filepath.Join(root, "home"), XDGConfigHome: filepath.Join(root, "xdg"), SystemConfigDir: filepath.Join(root, "etc")}
		writeFixture(t, filepath.Join(env.ExecutableDir, "yt-dlp.conf"), "--ignore-config --output portable")
		writeFixture(t, filepath.Join(env.HomeConfigDir, "yt-dlp.conf"), "--output home")
		result, err := Load(context.Background(), Request{Environment: env, IncludeDefaults: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := sourceKinds(result.Sources); !reflect.DeepEqual(got, []SourceKind{SourcePortable, SourceCommandLine}) {
			t.Fatalf("sources = %#v", got)
		}
	})
	t.Run("system-removes-user", func(t *testing.T) {
		root := t.TempDir()
		env := Environment{Platform: PlatformLinux, ExecutableDir: filepath.Join(root, "bin"), HomeConfigDir: filepath.Join(root, "homecfg"), HomeDir: filepath.Join(root, "home"), XDGConfigHome: filepath.Join(root, "xdg"), SystemConfigDir: filepath.Join(root, "etc")}
		groups := DefaultGroups(env)
		writeFixture(t, groups[2].Candidates[0].Path, "--output user")
		writeFixture(t, groups[3].Candidates[0].Path, "--ignore-config --output system")
		result, err := Load(context.Background(), Request{Environment: env, IncludeDefaults: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := sourceKinds(result.Sources); !reflect.DeepEqual(got, []SourceKind{SourceSystem, SourceCommandLine}) {
			t.Fatalf("sources = %#v", got)
		}
	})
}

func TestLoadStdinOnceAndCancellation(t *testing.T) {
	result, err := Load(context.Background(), Request{Explicit: []string{"-", "-"}, Stdin: strings.NewReader("--output stdin")})
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceKinds(result.Sources); !reflect.DeepEqual(got, []SourceKind{SourceStdin, SourceCommandLine}) {
		t.Fatalf("sources = %#v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingReader{cancel: cancel}
	_, err = Load(ctx, Request{Explicit: []string{"-"}, Stdin: reader})
	if !IsCategory(err, ErrorCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestLoadCancellationInterruptsClosableReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := newBlockingReadCloser()
	done := make(chan error, 1)
	go func() {
		_, err := Load(ctx, Request{Explicit: []string{"-"}, Stdin: reader})
		done <- err
	}()
	<-reader.entered
	cancel()
	err := <-done
	if !IsCategory(err, ErrorCanceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestLoadCategorizedPathRecursionAndSizeFailures(t *testing.T) {
	_, err := Load(context.Background(), Request{Explicit: []string{"bad\x00path"}})
	if !IsCategory(err, ErrorPath) {
		t.Fatalf("NUL path = %v", err)
	}
	_, err = Load(context.Background(), Request{Explicit: []string{filepath.Join(t.TempDir(), "missing")}})
	if !IsCategory(err, ErrorPath) {
		t.Fatalf("missing path = %v", err)
	}

	root := t.TempDir()
	for index := 0; index < 4; index++ {
		content := "--output end"
		if index < 3 {
			content = "--config-locations " + string(rune('a'+index+1)) + ".conf"
		}
		writeFixture(t, filepath.Join(root, string(rune('a'+index))+".conf"), content)
	}
	limits := DefaultLimits()
	limits.MaxDepth = 2
	_, err = Load(context.Background(), Request{Explicit: []string{filepath.Join(root, "a.conf")}, Limits: limits})
	if !IsCategory(err, ErrorRecursion) {
		t.Fatalf("depth = %v", err)
	}

	large := filepath.Join(root, "large.conf")
	writeFixture(t, large, strings.Repeat("x", 20))
	limits = DefaultLimits()
	limits.MaxFileBytes = 10
	_, err = Load(context.Background(), Request{Explicit: []string{large}, Limits: limits})
	if !IsCategory(err, ErrorResource) {
		t.Fatalf("size = %v", err)
	}
}

func TestLoadAliasDefinesConfigLocation(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "child.conf"), "--output child")
	writeFixture(t, filepath.Join(root, "root.conf"), `--alias include "--config-locations {0}" --include child.conf --output root`)
	result, err := Load(context.Background(), Request{Explicit: []string{filepath.Join(root, "root.conf")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := optionValues(result.Arguments, "--output"); !reflect.DeepEqual(got, []string{"child", "root"}) {
		t.Fatalf("output = %#v", got)
	}
}

type cancelingReader struct {
	cancel context.CancelFunc
	done   bool
}

type blockingReadCloser struct {
	entered chan struct{}
	closed  chan struct{}
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{entered: make(chan struct{}), closed: make(chan struct{})}
}
func (r *blockingReadCloser) Read([]byte) (int, error) {
	close(r.entered)
	<-r.closed
	return 0, os.ErrClosed
}
func (r *blockingReadCloser) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return nil
}

func (r *cancelingReader) Read(buffer []byte) (int, error) {
	if !r.done {
		r.done = true
		copy(buffer, "--output partial")
		r.cancel()
		return len("--output partial"), nil
	}
	return 0, io.EOF
}

func sourceKinds(sources []Source) []SourceKind {
	result := make([]SourceKind, len(sources))
	for index := range sources {
		result[index] = sources[index].Kind
	}
	return result
}

func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRealPath(t *testing.T, path string) string {
	t.Helper()
	result, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err = filepath.Abs(result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func optionValues(arguments []string, option string) []string {
	var result []string
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == option {
			result = append(result, arguments[index+1])
			index++
		}
	}
	return result
}

func lastOption(arguments []string, option string) string {
	values := optionValues(arguments, option)
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}
