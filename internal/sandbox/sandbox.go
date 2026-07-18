package sandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	maxPaths         = 64
	maxArguments     = 256
	maxArgumentBytes = 64 << 10
	maxSecretHandles = 32
)

type Adapter string

const (
	AdapterBubblewrap  Adapter = "bubblewrap"
	AdapterSandboxExec Adapter = "sandbox-exec"
)

type Limits struct {
	AddressSpaceBytes uint64
	CPUSeconds        uint64
	Processes         uint64
	OpenFiles         uint64
}

func (limits Limits) requested() bool {
	return limits.AddressSpaceBytes != 0 || limits.CPUSeconds != 0 || limits.Processes != 0 || limits.OpenFiles != 0
}

type Spec struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	ReadOnlyPaths    []string
	WritablePaths    []string
	AllowNetwork     bool
	// SecretHandles are opaque broker identifiers. Secret values are not a
	// supported field and are never inserted into argv or the environment.
	SecretHandles []string
	Limits        Limits
}

type Plan struct {
	Adapter          Adapter
	Executable       string
	Arguments        []string
	Environment      []string
	WorkingDirectory string
	ReadOnlyPaths    []string
	WritablePaths    []string
	SecretHandles    []string
	NetworkAllowed   bool
	Limitations      []string
}

type Lookup func(string) (string, error)

func Prepare(spec Spec) (Plan, error) {
	return PrepareForOS(runtime.GOOS, spec, exec.LookPath)
}

// PrepareForOS is exported for deterministic portability tests. Production
// callers use Prepare, which supplies the actual GOOS and executable lookup.
func PrepareForOS(goos string, spec Spec, lookup Lookup) (Plan, error) {
	if goos != "linux" && goos != "darwin" {
		return Plan{}, ErrUnsupportedPlatform
	}
	normalized, err := validateSpec(spec)
	if err != nil {
		return Plan{}, err
	}
	if lookup == nil {
		return Plan{}, ErrInvalidConfig
	}
	switch goos {
	case "linux":
		return linuxPlan(normalized, lookup)
	case "darwin":
		return darwinPlan(normalized, lookup)
	default:
		return Plan{}, ErrUnsupportedPlatform
	}
}

func validateSpec(spec Spec) (Spec, error) {
	if len(spec.Arguments) > maxArguments || len(spec.ReadOnlyPaths) > maxPaths || len(spec.WritablePaths) > maxPaths || len(spec.SecretHandles) > maxSecretHandles {
		return Spec{}, ErrResourceLimit
	}
	argumentBytes := 0
	for _, argument := range spec.Arguments {
		argumentBytes += len(argument)
		if strings.IndexByte(argument, 0) >= 0 || len(argument) > 16<<10 || argumentBytes > maxArgumentBytes {
			return Spec{}, ErrResourceLimit
		}
	}
	if err := validateLimits(spec.Limits); err != nil {
		return Spec{}, err
	}
	executable, executableInfo, err := canonicalPath(spec.Executable)
	if err != nil || !executableInfo.Mode().IsRegular() || executableInfo.Mode()&os.ModeSymlink != 0 || executableInfo.Mode().Perm()&0o111 == 0 || !trustedReadOwner(executableInfo) {
		return Spec{}, ErrUnsafePath
	}
	workingDirectory, workingInfo, err := canonicalPath(spec.WorkingDirectory)
	if err != nil || !workingInfo.IsDir() || workingInfo.Mode()&os.ModeSymlink != 0 || !trustedReadOwner(workingInfo) {
		return Spec{}, ErrUnsafePath
	}
	readOnly, err := canonicalRoots(spec.ReadOnlyPaths, false)
	if err != nil {
		return Spec{}, err
	}
	writable, err := canonicalRoots(spec.WritablePaths, true)
	if err != nil {
		return Spec{}, err
	}
	if !covered(executable, readOnly) || (!covered(workingDirectory, readOnly) && !covered(workingDirectory, writable)) {
		return Spec{}, fmt.Errorf("%w: executable and working directory require declared roots", ErrInvalidConfig)
	}
	for _, readPath := range readOnly {
		for _, writePath := range writable {
			if covered(readPath, []string{writePath}) || covered(writePath, []string{readPath}) {
				return Spec{}, fmt.Errorf("%w: read/write roots overlap", ErrInvalidConfig)
			}
		}
	}
	if covered(executable, writable) {
		return Spec{}, fmt.Errorf("%w: executable cannot be writable", ErrInvalidConfig)
	}
	handles := append([]string(nil), spec.SecretHandles...)
	sort.Strings(handles)
	for index, handle := range handles {
		if !validHandle(handle) || (index > 0 && handle == handles[index-1]) {
			return Spec{}, fmt.Errorf("%w: invalid secret handle", ErrInvalidConfig)
		}
	}
	spec.Executable = executable
	spec.WorkingDirectory = workingDirectory
	spec.ReadOnlyPaths = readOnly
	spec.WritablePaths = writable
	spec.Arguments = append([]string(nil), spec.Arguments...)
	spec.SecretHandles = handles
	return spec, nil
}

func validateLimits(limits Limits) error {
	if limits.AddressSpaceBytes > 64<<30 || limits.CPUSeconds > 24*60*60 || limits.Processes > 1024 || limits.OpenFiles > 16_384 {
		return ErrResourceLimit
	}
	if limits.AddressSpaceBytes != 0 && limits.AddressSpaceBytes < 16<<20 {
		return fmt.Errorf("%w: address-space limit is below safe minimum", ErrInvalidConfig)
	}
	if limits.CPUSeconds == 0 && (limits.Processes != 0 || limits.OpenFiles != 0 || limits.AddressSpaceBytes != 0) {
		// A CPU cap is required whenever host resource caps are requested, so a
		// forgotten wall-clock supervisor does not create an unbounded process.
		return fmt.Errorf("%w: resource limits require a CPU cap", ErrInvalidConfig)
	}
	return nil
}

func canonicalRoots(paths []string, writable bool) ([]string, error) {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, input := range paths {
		canonical, info, err := canonicalPath(input)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !trustedReadOwner(info) {
			return nil, ErrUnsafePath
		}
		if writable && (!info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(info)) {
			return nil, ErrUnsafePath
		}
		if _, duplicate := seen[canonical]; duplicate {
			return nil, fmt.Errorf("%w: duplicate sandbox root", ErrInvalidConfig)
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	sort.Strings(result)
	return result, nil
}

func canonicalPath(input string) (string, os.FileInfo, error) {
	if input == "" || !filepath.IsAbs(input) || filepath.Clean(input) != input || strings.IndexByte(input, 0) >= 0 || strings.ContainsAny(input, "\r\n") {
		return "", nil, ErrUnsafePath
	}
	canonical, err := filepath.EvalSymlinks(input)
	if err != nil || canonical != input {
		return "", nil, ErrUnsafePath
	}
	info, err := os.Lstat(input)
	if err != nil {
		return "", nil, ErrUnsafePath
	}
	return canonical, info, nil
}

func covered(target string, roots []string) bool {
	for _, root := range roots {
		if target == root {
			return true
		}
		relative, err := filepath.Rel(root, target)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}

func validHandle(handle string) bool {
	if handle == "" || len(handle) > 64 || handle[0] < 'a' || handle[0] > 'z' {
		return false
	}
	for _, character := range handle {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func linuxPlan(spec Spec, lookup Lookup) (Plan, error) {
	bubblewrap, err := lookupExecutable("bwrap", lookup)
	if err != nil {
		return Plan{}, err
	}
	arguments := []string{
		"--die-with-parent", "--new-session", "--unshare-all", "--clearenv",
		"--setenv", "HOME", "/nonexistent", "--setenv", "LANG", "C",
		"--setenv", "LC_ALL", "C", "--setenv", "PATH", "/usr/bin:/bin",
		"--setenv", "TMPDIR", "/tmp",
	}
	if spec.AllowNetwork {
		arguments = append(arguments, "--share-net")
	}
	arguments = append(arguments, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	for _, directory := range bindParents(append(append([]string(nil), spec.ReadOnlyPaths...), spec.WritablePaths...)) {
		arguments = append(arguments, "--dir", directory)
	}
	for _, root := range spec.ReadOnlyPaths {
		arguments = append(arguments, "--ro-bind", root, root)
	}
	for _, root := range spec.WritablePaths {
		arguments = append(arguments, "--bind", root, root)
	}
	arguments = append(arguments, "--chdir", spec.WorkingDirectory, "--", spec.Executable)
	arguments = append(arguments, spec.Arguments...)
	planExecutable := bubblewrap
	if spec.Limits.requested() {
		prlimit, lookupErr := lookupExecutable("prlimit", lookup)
		if lookupErr != nil {
			return Plan{}, ErrUnsupportedLimit
		}
		limitArguments := make([]string, 0, 6)
		if spec.Limits.AddressSpaceBytes != 0 {
			limitArguments = append(limitArguments, "--as="+strconv.FormatUint(spec.Limits.AddressSpaceBytes, 10))
		}
		if spec.Limits.CPUSeconds != 0 {
			limitArguments = append(limitArguments, "--cpu="+strconv.FormatUint(spec.Limits.CPUSeconds, 10))
		}
		if spec.Limits.Processes != 0 {
			limitArguments = append(limitArguments, "--nproc="+strconv.FormatUint(spec.Limits.Processes, 10))
		}
		if spec.Limits.OpenFiles != 0 {
			limitArguments = append(limitArguments, "--nofile="+strconv.FormatUint(spec.Limits.OpenFiles, 10))
		}
		limitArguments = append(limitArguments, "--", bubblewrap)
		arguments = append(limitArguments, arguments...)
		planExecutable = prlimit
	}
	return Plan{
		Adapter: AdapterBubblewrap, Executable: planExecutable, Arguments: arguments,
		Environment:      []string{"HOME=/nonexistent", "LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin", "TMPDIR=/tmp"},
		WorkingDirectory: spec.WorkingDirectory, ReadOnlyPaths: spec.ReadOnlyPaths,
		WritablePaths: spec.WritablePaths, SecretHandles: spec.SecretHandles,
		NetworkAllowed: spec.AllowNetwork,
		Limitations:    []string{"dynamic runtimes and shared libraries must be declared as read-only roots", "wall-clock cancellation and process-group termination remain supervisor responsibilities"},
	}, nil
}

func darwinPlan(spec Spec, lookup Lookup) (Plan, error) {
	if spec.Limits.requested() {
		return Plan{}, ErrUnsupportedLimit
	}
	sandboxExec, err := lookupExecutable("sandbox-exec", lookup)
	if err != nil {
		return Plan{}, err
	}
	profile := darwinProfile(spec)
	arguments := []string{"-p", profile, "--", spec.Executable}
	arguments = append(arguments, spec.Arguments...)
	return Plan{
		Adapter: AdapterSandboxExec, Executable: sandboxExec, Arguments: arguments,
		Environment:      []string{"HOME=/nonexistent", "LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin", "TMPDIR=/tmp"},
		WorkingDirectory: spec.WorkingDirectory, ReadOnlyPaths: spec.ReadOnlyPaths,
		WritablePaths: spec.WritablePaths, SecretHandles: spec.SecretHandles,
		NetworkAllowed: spec.AllowNetwork,
		Limitations:    []string{"sandbox-exec is deprecated by Apple and availability is checked at runtime", "CPU, memory, descriptor, and process limits require a separate supervisor", "process-tree cancellation remains a supervisor responsibility"},
	}, nil
}

func darwinProfile(spec Spec) string {
	var profile strings.Builder
	profile.WriteString("(version 1)\n(deny default)\n")
	profile.WriteString("(allow process-info*)\n(allow signal (target self))\n")
	profile.WriteString("(allow process-exec (literal \"")
	profile.WriteString(escapeProfile(spec.Executable))
	profile.WriteString("\"))\n")
	profile.WriteString("(allow file-read* (subpath \"/System/Library\") (subpath \"/usr/lib\") (literal \"/dev/null\"))\n")
	for _, root := range spec.ReadOnlyPaths {
		profile.WriteString("(allow file-read* (")
		profile.WriteString(profilePathRule(root))
		profile.WriteString(") )\n")
	}
	for _, root := range spec.WritablePaths {
		profile.WriteString("(allow file-read* file-write* (subpath \"")
		profile.WriteString(escapeProfile(root))
		profile.WriteString("\"))\n")
	}
	if spec.AllowNetwork {
		profile.WriteString("(allow network*)\n")
	}
	return profile.String()
}

func profilePathRule(root string) string {
	info, err := os.Lstat(root)
	if err == nil && info.IsDir() {
		return "subpath \"" + escapeProfile(root) + "\""
	}
	return "literal \"" + escapeProfile(root) + "\""
}

func escapeProfile(input string) string {
	return strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(input)
}

func lookupExecutable(name string, lookup Lookup) (string, error) {
	path, err := lookup(name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || path == "" {
			return "", ErrAdapterUnavailable
		}
		return "", ErrAdapterUnavailable
	}
	canonical, info, err := canonicalPath(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || !trustedReadOwner(info) {
		return "", ErrAdapterUnavailable
	}
	return canonical, nil
}

func bindParents(roots []string) []string {
	set := make(map[string]struct{})
	for _, root := range roots {
		for parent := filepath.Dir(root); parent != string(filepath.Separator) && parent != "."; parent = filepath.Dir(parent) {
			set[parent] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for path := range set {
		result = append(result, path)
	}
	sort.Slice(result, func(i, j int) bool {
		leftDepth := strings.Count(result[i], string(filepath.Separator))
		rightDepth := strings.Count(result[j], string(filepath.Separator))
		if leftDepth == rightDepth {
			return result[i] < result[j]
		}
		return leftDepth < rightDepth
	})
	return result
}
