// Package config implements bounded, source-located configuration discovery
// and token merging compatible with the principal yt-dlp configuration flow.
package config

import (
	"context"
	"io"
)

// Platform selects the target path convention used for default discovery.
type Platform string

const (
	PlatformLinux   Platform = "linux"
	PlatformDarwin  Platform = "darwin"
	PlatformWindows Platform = "windows"
)

// SourceKind identifies where a configuration token originated.
type SourceKind string

const (
	SourceCommandLine SourceKind = "command_line"
	SourcePortable    SourceKind = "portable"
	SourceHome        SourceKind = "home"
	SourceUser        SourceKind = "user"
	SourceSystem      SourceKind = "system"
	SourceExplicit    SourceKind = "explicit"
	SourceStdin       SourceKind = "stdin"
)

// Environment contains already-resolved environment inputs. Keeping this
// explicit makes discovery deterministic and testable for every target OS.
type Environment struct {
	Platform        Platform
	HomeDir         string
	XDGConfigHome   string
	AppData         string
	ExecutableDir   string
	HomeConfigDir   string
	SystemConfigDir string
}

// Limits bound all attacker-influenced configuration work.
type Limits struct {
	MaxFileBytes     int64
	MaxFiles         int
	MaxDepth         int
	MaxTokens        int
	MaxTokenBytes    int
	MaxPathBytes     int
	MaxAliasTriggers int
}

// DefaultLimits returns conservative product defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:     4 << 20,
		MaxFiles:         128,
		MaxDepth:         16,
		MaxTokens:        100_000,
		MaxTokenBytes:    1 << 20,
		MaxPathBytes:     16 << 10,
		MaxAliasTriggers: 100,
	}
}

// Request describes one immutable configuration operation. Explicit paths
// have command-line priority and may name a file, a directory, or "-".
type Request struct {
	Environment     Environment
	CommandLine     []string
	Explicit        []string
	IncludeDefaults bool
	Stdin           io.Reader
	Limits          Limits
}

// Candidate is a default location in lookup order within its group.
type Candidate struct {
	Kind SourceKind
	Path string
}

// Group is one precedence tier. The first existing candidate is selected.
type Group struct {
	Kind       SourceKind
	Candidates []Candidate
}

// Token retains exact origin information for deterministic diagnostics.
type Token struct {
	Value  string
	Source string
	Line   int
	Column int
}

// Source records a loaded configuration and its unexpanded tokens.
type Source struct {
	Kind   SourceKind
	Path   string
	Tokens []Token
}

// Result is ordered from lowest to highest precedence. Arguments is the
// alias-expanded argv that the CLI parser should consume.
type Result struct {
	Sources   []Source
	Tokens    []Token
	Arguments []string
}

// Load discovers, reads, tokenizes, recursively resolves, and merges config.
func Load(ctx context.Context, request Request) (Result, error) {
	return newLoader(request).load(ctx)
}
