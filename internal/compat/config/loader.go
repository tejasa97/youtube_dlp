package config

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type sourceNode struct {
	source   Source
	children []*sourceNode
}

type loader struct {
	request   Request
	limits    Limits
	loaded    map[string]bool
	fileCount int
	stdinRead bool
}

func newLoader(request Request) *loader {
	request.Environment = normalizeEnvironment(request.Environment)
	return &loader{request: request, limits: normalizeLimits(request.Limits), loaded: make(map[string]bool)}
}

func (l *loader) load(ctx context.Context) (Result, error) {
	if err := contextFailure(ctx, "load", ""); err != nil {
		return Result{}, err
	}
	commandTokens := make([]Token, len(l.request.CommandLine))
	for index, value := range l.request.CommandLine {
		commandTokens[index] = Token{Value: value, Source: "<command-line>", Line: 1, Column: index + 1}
	}
	command := &sourceNode{source: Source{Kind: SourceCommandLine, Path: "<command-line>", Tokens: commandTokens}}
	commandControls, err := scanControls(commandTokens, l.limits)
	if err != nil {
		return Result{}, err
	}

	var defaults []*sourceNode
	if l.request.IncludeDefaults && !commandControls.ignore {
		for _, group := range DefaultGroups(l.request.Environment) {
			node, err := l.firstDefault(ctx, group)
			if err != nil {
				return Result{}, err
			}
			if node == nil {
				continue
			}
			defaults = append(defaults, node)
			controls, err := scanControls(node.source.Tokens, l.limits)
			if err != nil {
				return Result{}, err
			}
			if controls.ignore {
				if group.Kind == SourceSystem {
					defaults = removeKind(defaults, SourceUser)
				}
				break
			}
		}
	}

	locations := append([]string(nil), l.request.Explicit...)
	locations = append(locations, commandControls.locations...)
	for _, location := range locations {
		node, err := l.loadLocation(ctx, location, "", 1)
		if err != nil {
			return Result{}, err
		}
		if node != nil {
			command.children = append(command.children, node)
		}
	}

	// Defaults are recorded high-to-low but consumed low-to-high. Command-line
	// explicit children are lower priority than their declaring source.
	var ordered []*sourceNode
	for index := len(defaults) - 1; index >= 0; index-- {
		ordered = append(ordered, defaults[index])
	}
	ordered = append(ordered, command)

	var result Result
	for _, node := range ordered {
		flatten(node, &result.Sources, &result.Tokens)
	}
	if len(result.Tokens) > l.limits.MaxTokens {
		return Result{}, configError(ErrorResource, "merge", "", "merged token count exceeds limit", nil)
	}
	expanded, err := ExpandAliases(result.Tokens, l.limits)
	if err != nil {
		return Result{}, err
	}
	result.Tokens = expanded
	result.Arguments = make([]string, len(expanded))
	for index, token := range expanded {
		result.Arguments[index] = token.Value
	}
	return result, nil
}

type controls struct {
	ignore    bool
	locations []string
}

func scanControls(tokens []Token, limits Limits) (controls, error) {
	expanded, err := ExpandAliases(tokens, limits)
	if err != nil {
		return controls{}, err
	}
	var result controls
	for index := 0; index < len(expanded); index++ {
		value := expanded[index].Value
		switch value {
		case "--ignore-config", "--no-config":
			result.ignore = true
		case "--no-config-locations":
			result.locations = nil
		case "--config-locations", "--config-location":
			if index+1 >= len(expanded) {
				return controls{}, tokenError(ErrorSyntax, "controls", expanded[index], value+" requires a path")
			}
			index++
			result.locations = append(result.locations, expanded[index].Value)
		default:
			if strings.HasPrefix(value, "--config-locations=") {
				result.locations = append(result.locations, strings.TrimPrefix(value, "--config-locations="))
			} else if strings.HasPrefix(value, "--config-location=") {
				result.locations = append(result.locations, strings.TrimPrefix(value, "--config-location="))
			}
		}
	}
	return result, nil
}

func (l *loader) firstDefault(ctx context.Context, group Group) (*sourceNode, error) {
	for _, candidate := range group.Candidates {
		if err := contextFailure(ctx, "discover", candidate.Path); err != nil {
			return nil, err
		}
		if candidate.Path == "" || strings.IndexByte(candidate.Path, 0) >= 0 || len(candidate.Path) > l.limits.MaxPathBytes {
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
		return l.loadFile(ctx, candidate.Path, group.Kind, 1)
	}
	return nil, nil
}

func (l *loader) loadLocation(ctx context.Context, location, parent string, depth int) (*sourceNode, error) {
	if err := contextFailure(ctx, "resolve", parent); err != nil {
		return nil, err
	}
	if location == "-" {
		return l.loadStdin(ctx, depth)
	}
	if location == "" || strings.IndexByte(location, 0) >= 0 || len(location) > l.limits.MaxPathBytes {
		return nil, configError(ErrorPath, "resolve", parent, "invalid configuration path", nil)
	}
	if !filepath.IsAbs(location) && parent != "" {
		location = filepath.Join(parent, location)
	}
	info, err := os.Stat(location)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, configError(ErrorPath, "resolve", location, "configuration location does not exist", err)
		}
		return nil, configError(ErrorIO, "resolve", location, "unable to inspect configuration location", err)
	}
	if info.IsDir() {
		location = filepath.Join(location, "yt-dlp.conf")
		info, err = os.Stat(location)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, configError(ErrorPath, "resolve", location, "directory does not contain yt-dlp.conf", err)
			}
			return nil, configError(ErrorIO, "resolve", location, "unable to inspect directory configuration", err)
		}
	}
	if !info.Mode().IsRegular() {
		return nil, configError(ErrorPath, "resolve", location, "configuration location is not a regular file", nil)
	}
	return l.loadFile(ctx, location, SourceExplicit, depth)
}

func (l *loader) loadFile(ctx context.Context, path string, kind SourceKind, depth int) (*sourceNode, error) {
	if depth > l.limits.MaxDepth {
		return nil, configError(ErrorRecursion, "load", path, "configuration nesting exceeds limit", nil)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, configError(ErrorIO, "resolve", path, "unable to resolve configuration path", err)
	}
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return nil, configError(ErrorPath, "resolve", path, "unable to make configuration path absolute", err)
	}
	if l.loaded[realPath] {
		return nil, nil
	}
	if l.fileCount >= l.limits.MaxFiles {
		return nil, configError(ErrorResource, "load", realPath, "configuration file count exceeds limit", nil)
	}
	l.loaded[realPath] = true
	l.fileCount++
	data, err := l.readBounded(ctx, realPath, func() (io.ReadCloser, error) { return os.Open(realPath) })
	if err != nil {
		return nil, err
	}
	return l.parseNode(ctx, data, realPath, kind, depth)
}

func (l *loader) loadStdin(ctx context.Context, depth int) (*sourceNode, error) {
	if l.stdinRead {
		return nil, nil
	}
	if l.request.Stdin == nil {
		return nil, configError(ErrorPath, "read", "<stdin>", "stdin configuration was requested but no reader was provided", nil)
	}
	if depth > l.limits.MaxDepth {
		return nil, configError(ErrorRecursion, "load", "<stdin>", "configuration nesting exceeds limit", nil)
	}
	l.stdinRead = true
	data, err := l.readBounded(ctx, "<stdin>", func() (io.ReadCloser, error) {
		if reader, ok := l.request.Stdin.(io.ReadCloser); ok {
			return reader, nil
		}
		return io.NopCloser(l.request.Stdin), nil
	})
	if err != nil {
		return nil, err
	}
	return l.parseNode(ctx, data, "<stdin>", SourceStdin, depth)
}

func (l *loader) parseNode(ctx context.Context, data []byte, path string, kind SourceKind, depth int) (*sourceNode, error) {
	text, err := Decode(data, path)
	if err != nil {
		return nil, err
	}
	tokens, err := Tokenize(text, path, l.limits)
	if err != nil {
		return nil, err
	}
	node := &sourceNode{source: Source{Kind: kind, Path: path, Tokens: tokens}}
	controls, err := scanControls(tokens, l.limits)
	if err != nil {
		return nil, err
	}
	parent := ""
	if path != "<stdin>" {
		parent = filepath.Dir(path)
	}
	for _, location := range controls.locations {
		child, err := l.loadLocation(ctx, location, parent, depth+1)
		if err != nil {
			return nil, err
		}
		if child != nil {
			node.children = append(node.children, child)
		}
	}
	return node, nil
}

func (l *loader) readBounded(ctx context.Context, source string, open func() (io.ReadCloser, error)) ([]byte, error) {
	reader, err := open()
	if err != nil {
		return nil, configError(ErrorIO, "read", source, "unable to open configuration", err)
	}
	defer reader.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = reader.Close()
		case <-done:
		}
	}()
	buffer := make([]byte, 32<<10)
	data := make([]byte, 0, min(int(l.limits.MaxFileBytes), len(buffer)))
	for {
		if err := contextFailure(ctx, "read", source); err != nil {
			return nil, err
		}
		count, readErr := reader.Read(buffer)
		if err := contextFailure(ctx, "read", source); err != nil {
			return nil, err
		}
		if int64(len(data)+count) > l.limits.MaxFileBytes {
			return nil, configError(ErrorResource, "read", source, "configuration file exceeds byte limit", nil)
		}
		data = append(data, buffer[:count]...)
		if errors.Is(readErr, io.EOF) {
			return data, nil
		}
		if readErr != nil {
			return nil, configError(ErrorIO, "read", source, "unable to read configuration", readErr)
		}
		if count == 0 {
			return nil, configError(ErrorIO, "read", source, "configuration reader made no progress", io.ErrNoProgress)
		}
	}
}

func flatten(node *sourceNode, sources *[]Source, tokens *[]Token) {
	for index := len(node.children) - 1; index >= 0; index-- {
		flatten(node.children[index], sources, tokens)
	}
	*sources = append(*sources, node.source)
	*tokens = append(*tokens, node.source.Tokens...)
}

func removeKind(nodes []*sourceNode, kind SourceKind) []*sourceNode {
	result := nodes[:0]
	for _, node := range nodes {
		if node.source.Kind != kind {
			result = append(result, node)
		}
	}
	return result
}

func contextFailure(ctx context.Context, op, source string) error {
	if err := ctx.Err(); err != nil {
		return configError(ErrorCanceled, op, source, "operation canceled", err)
	}
	return nil
}
