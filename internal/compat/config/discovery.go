package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Discover selects the first existing regular file in each default group.
// Missing candidates are normal; inaccessible or unsafe candidates are
// categorized failures. Results retain high-to-low precedence order.
func Discover(ctx context.Context, environment Environment, limits Limits) ([]Candidate, error) {
	limits = normalizeLimits(limits)
	var selected []Candidate
	for _, group := range DefaultGroups(environment) {
		for _, candidate := range group.Candidates {
			if err := contextFailure(ctx, "discover", candidate.Path); err != nil {
				return nil, err
			}
			if candidate.Path == "" || strings.IndexByte(candidate.Path, 0) >= 0 || len(candidate.Path) > limits.MaxPathBytes {
				return nil, configError(ErrorPath, "discover", candidate.Path, "invalid configuration candidate path", nil)
			}
			info, err := os.Stat(candidate.Path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, configError(ErrorIO, "discover", candidate.Path, "unable to inspect configuration candidate", err)
			}
			if !info.Mode().IsRegular() {
				return nil, configError(ErrorPath, "discover", candidate.Path, "configuration candidate is not a regular file", nil)
			}
			selected = append(selected, candidate)
			break
		}
	}
	return selected, nil
}

// RuntimeEnvironment captures the current process environment once.
func RuntimeEnvironment() Environment {
	home, _ := os.UserHomeDir()
	platform := Platform(runtime.GOOS)
	appData := os.Getenv("appdata")
	if platform == PlatformWindows {
		appData = firstNonempty(os.Getenv("APPDATA"), appData)
	}
	return Environment{
		Platform:        platform,
		HomeDir:         home,
		XDGConfigHome:   os.Getenv("XDG_CONFIG_HOME"),
		AppData:         appData,
		ExecutableDir:   executableDir(),
		SystemConfigDir: "/etc",
	}
}

func executableDir() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(executable)
}

// DefaultGroups returns yt-dlp's principal portable, home, user, and system
// lookup candidates. Groups are high-to-low precedence; candidates are tried
// in order and stop after the first existing file.
func DefaultGroups(environment Environment) []Group {
	environment = normalizeEnvironment(environment)
	groups := []Group{
		candidateGroup(SourcePortable, environment, environment.ExecutableDir),
		candidateGroup(SourceHome, environment, environment.HomeConfigDir),
	}

	xdg := environment.XDGConfigHome
	if xdg == "" {
		xdg = targetJoin(environment.Platform, environment.HomeDir, ".config")
	}
	userDirs := []string{targetJoin(environment.Platform, xdg, "yt-dlp")}
	if environment.AppData != "" {
		userDirs = append(userDirs, targetJoin(environment.Platform, environment.AppData, "yt-dlp"))
	}
	userDirs = append(userDirs, targetJoin(environment.Platform, environment.HomeDir, ".yt-dlp"))
	groups = append(groups, Group{Kind: SourceUser, Candidates: configDirCandidates(environment.Platform, SourceUser, userDirs...)})

	systemDir := targetJoin(environment.Platform, environment.SystemConfigDir, "yt-dlp")
	groups = append(groups, Group{Kind: SourceSystem, Candidates: configDirCandidates(environment.Platform, SourceSystem, systemDir)})
	return groups
}

func normalizeEnvironment(environment Environment) Environment {
	if environment.Platform == "" {
		environment.Platform = Platform(runtime.GOOS)
	}
	if environment.SystemConfigDir == "" {
		environment.SystemConfigDir = "/etc"
	}
	return environment
}

func candidateGroup(kind SourceKind, environment Environment, directory string) Group {
	if kind == SourcePortable && directory == "" {
		return Group{Kind: kind}
	}
	return Group{Kind: kind, Candidates: []Candidate{{Kind: kind, Path: targetJoin(environment.Platform, directory, "yt-dlp.conf")}}}
}

func configDirCandidates(platform Platform, kind SourceKind, directories ...string) []Candidate {
	var candidates []Candidate
	for _, directory := range directories {
		head, tail := targetSplit(platform, directory)
		candidates = append(candidates, Candidate{Kind: kind, Path: targetJoin(platform, head, "yt-dlp.conf")})
		if strings.HasPrefix(tail, ".") {
			candidates = append(candidates, Candidate{Kind: kind, Path: targetJoin(platform, head, "yt-dlp.conf.txt")})
		}
		candidates = append(candidates,
			Candidate{Kind: kind, Path: targetJoin(platform, directory, "config")},
			Candidate{Kind: kind, Path: targetJoin(platform, directory, "config.txt")},
		)
	}
	return candidates
}

func targetJoin(platform Platform, parts ...string) string {
	separator := "/"
	if platform == PlatformWindows {
		separator = `\`
	}
	var result string
	for _, part := range parts {
		if part == "" {
			continue
		}
		part = strings.ReplaceAll(part, "/", separator)
		part = strings.ReplaceAll(part, `\`, separator)
		if result == "" {
			result = strings.TrimRight(part, separator)
			if result == "" && strings.HasPrefix(part, separator) {
				result = separator
			}
			continue
		}
		result = strings.TrimRight(result, separator) + separator + strings.Trim(part, separator)
	}
	return result
}

func targetSplit(platform Platform, value string) (string, string) {
	separator := "/"
	if platform == PlatformWindows {
		separator = `\`
	}
	value = strings.TrimRight(value, separator)
	index := strings.LastIndex(value, separator)
	if index < 0 {
		return "", value
	}
	return value[:index], value[index+1:]
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
