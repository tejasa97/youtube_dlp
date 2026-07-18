//go:build darwin || linux

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type sandboxFixture struct {
	spec   Spec
	tools  map[string]string
	lookup Lookup
}

func newSandboxFixture(t testing.TB) sandboxFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	readRoot := filepath.Join(base, "pack")
	writeRoot := filepath.Join(base, "writable")
	toolRoot := filepath.Join(base, "tools")
	for _, directory := range []string{readRoot, writeRoot, toolRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	plugin := filepath.Join(readRoot, "plugin")
	if err := os.WriteFile(plugin, []byte("synthetic executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]string)
	for _, name := range []string{"bwrap", "prlimit", "sandbox-exec"} {
		path := filepath.Join(toolRoot, name)
		if err := os.WriteFile(path, []byte("synthetic adapter"), 0o700); err != nil {
			t.Fatal(err)
		}
		tools[name] = path
	}
	return sandboxFixture{
		spec: Spec{
			Executable: plugin, Arguments: []string{"extract", "fixture"},
			WorkingDirectory: readRoot, ReadOnlyPaths: []string{readRoot},
			WritablePaths: []string{writeRoot}, SecretHandles: []string{"cookie.main", "oauth-token"},
		},
		tools: tools,
		lookup: func(name string) (string, error) {
			path, ok := tools[name]
			if !ok {
				return "", exec.ErrNotFound
			}
			return path, nil
		},
	}
}

func TestLinuxPlanIsolatesFilesystemNetworkAndSecrets(t *testing.T) {
	fixture := newSandboxFixture(t)
	plan, err := PrepareForOS("linux", fixture.spec, fixture.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Adapter != AdapterBubblewrap || plan.Executable != fixture.tools["bwrap"] {
		t.Fatalf("unexpected adapter: %#v", plan)
	}
	for _, required := range []string{"--die-with-parent", "--new-session", "--unshare-all", "--ro-bind", "--bind", "--chdir"} {
		if !slices.Contains(plan.Arguments, required) {
			t.Fatalf("missing %s in %q", required, plan.Arguments)
		}
	}
	if slices.Contains(plan.Arguments, "--share-net") {
		t.Fatal("network was shared without permission")
	}
	if strings.Contains(strings.Join(plan.Arguments, " ")+strings.Join(plan.Environment, " "), "secret-value") {
		t.Fatal("secret value leaked into launch surface")
	}
	if strings.Join(plan.SecretHandles, ",") != "cookie.main,oauth-token" {
		t.Fatalf("unexpected handles: %q", plan.SecretHandles)
	}

	fixture.spec.AllowNetwork = true
	plan, err = PrepareForOS("linux", fixture.spec, fixture.lookup)
	if err != nil || !slices.Contains(plan.Arguments, "--share-net") || !plan.NetworkAllowed {
		t.Fatalf("network permission plan = %#v, error = %v", plan, err)
	}
}

func TestLinuxPlanUsesPrlimitForRequestedCaps(t *testing.T) {
	fixture := newSandboxFixture(t)
	fixture.spec.Limits = Limits{AddressSpaceBytes: 256 << 20, CPUSeconds: 30, Processes: 8, OpenFiles: 64}
	plan, err := PrepareForOS("linux", fixture.spec, fixture.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Executable != fixture.tools["prlimit"] {
		t.Fatalf("limits did not select prlimit: %#v", plan)
	}
	joined := strings.Join(plan.Arguments, " ")
	for _, expected := range []string{"--as=268435456", "--cpu=30", "--nproc=8", "--nofile=64", fixture.tools["bwrap"]} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in %q", expected, joined)
		}
	}

	delete(fixture.tools, "prlimit")
	if _, err := PrepareForOS("linux", fixture.spec, fixture.lookup); !errors.Is(err, ErrUnsupportedLimit) {
		t.Fatalf("missing prlimit error = %v", err)
	}
}

func TestDarwinPlanHasExplicitProfileAndLimitDeviation(t *testing.T) {
	fixture := newSandboxFixture(t)
	plan, err := PrepareForOS("darwin", fixture.spec, fixture.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Adapter != AdapterSandboxExec || plan.Executable != fixture.tools["sandbox-exec"] || len(plan.Arguments) < 4 || plan.Arguments[0] != "-p" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	profile := plan.Arguments[1]
	for _, expected := range []string{"(deny default)", "process-exec", fixture.spec.Executable, fixture.spec.ReadOnlyPaths[0], fixture.spec.WritablePaths[0]} {
		if !strings.Contains(profile, expected) {
			t.Fatalf("profile missing %q: %s", expected, profile)
		}
	}
	if strings.Contains(profile, "network*") {
		t.Fatal("profile allowed network without permission")
	}
	fixture.spec.AllowNetwork = true
	plan, err = PrepareForOS("darwin", fixture.spec, fixture.lookup)
	if err != nil || !strings.Contains(plan.Arguments[1], "(allow network*)") {
		t.Fatalf("network profile = %#v, error = %v", plan, err)
	}
	fixture.spec.Limits = Limits{CPUSeconds: 1}
	if _, err := PrepareForOS("darwin", fixture.spec, fixture.lookup); !errors.Is(err, ErrUnsupportedLimit) {
		t.Fatalf("darwin limit error = %v", err)
	}
}

func TestPrepareRejectsMissingAdapterAndUnsupportedPlatforms(t *testing.T) {
	fixture := newSandboxFixture(t)
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	if _, err := PrepareForOS("linux", fixture.spec, missing); !errors.Is(err, ErrAdapterUnavailable) {
		t.Fatalf("missing adapter error = %v", err)
	}
	for _, goos := range []string{"windows", "freebsd", "plan9", ""} {
		if _, err := PrepareForOS(goos, Spec{}, nil); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("PrepareForOS(%q) error = %v", goos, err)
		}
	}
}

func TestPrepareRejectsUnsafePathsOverlapsAndHandles(t *testing.T) {
	t.Run("symlink executable", func(t *testing.T) {
		fixture := newSandboxFixture(t)
		link := filepath.Join(filepath.Dir(fixture.spec.Executable), "link")
		if err := os.Symlink(fixture.spec.Executable, link); err != nil {
			t.Fatal(err)
		}
		fixture.spec.Executable = link
		if _, err := PrepareForOS("linux", fixture.spec, fixture.lookup); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("symlink error = %v", err)
		}
	})
	t.Run("overlap", func(t *testing.T) {
		fixture := newSandboxFixture(t)
		fixture.spec.WritablePaths = []string{fixture.spec.ReadOnlyPaths[0]}
		if _, err := PrepareForOS("linux", fixture.spec, fixture.lookup); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("overlap error = %v", err)
		}
	})
	t.Run("executable outside root", func(t *testing.T) {
		fixture := newSandboxFixture(t)
		fixture.spec.ReadOnlyPaths = []string{fixture.spec.WritablePaths[0]}
		fixture.spec.WritablePaths = nil
		if _, err := PrepareForOS("linux", fixture.spec, fixture.lookup); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("coverage error = %v", err)
		}
	})
	for _, handle := range []string{"", "UPPER", "contains secret", "token=secret", strings.Repeat("x", 65)} {
		fixture := newSandboxFixture(t)
		fixture.spec.SecretHandles = []string{handle}
		if _, err := PrepareForOS("linux", fixture.spec, fixture.lookup); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("handle %q error = %v", handle, err)
		}
	}
}

func TestPrepareBoundsArgumentsAndLimits(t *testing.T) {
	fixture := newSandboxFixture(t)
	fixture.spec.Arguments = make([]string, maxArguments+1)
	if _, err := PrepareForOS("linux", fixture.spec, fixture.lookup); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("arguments error = %v", err)
	}
	fixture = newSandboxFixture(t)
	fixture.spec.Limits = Limits{AddressSpaceBytes: 1 << 20, CPUSeconds: 1}
	if _, err := PrepareForOS("linux", fixture.spec, fixture.lookup); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("low memory limit error = %v", err)
	}
	fixture.spec.Limits = Limits{CPUSeconds: 24*60*60 + 1}
	if _, err := PrepareForOS("linux", fixture.spec, fixture.lookup); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversized CPU limit error = %v", err)
	}
}

func FuzzPrepareArguments(fuzz *testing.F) {
	fixture := newSandboxFixture(fuzz)
	for _, seed := range []string{"extract", "", "a\x00b", strings.Repeat("x", 1024)} {
		fuzz.Add(seed)
	}
	fuzz.Fuzz(func(t *testing.T, argument string) {
		if len(argument) > 1<<17 {
			t.Skip()
		}
		spec := fixture.spec
		spec.Arguments = []string{argument}
		_, _ = PrepareForOS("linux", spec, fixture.lookup)
	})
}
