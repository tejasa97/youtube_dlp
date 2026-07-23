package ytdlp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxSubtitleLanguages      = 256
	maxSubtitleFormatsPerLang = 32
	maxEmbeddedSubtitleTracks = 64
	maxSubtitleRules          = 128
	maxSubtitleRuleBytes      = 256
	maxSubtitleFormatBytes    = 1024
	maxSubtitleURLBytes       = 8192
	maxSubtitleBytes          = 16 << 20
)

var (
	subtitleLanguagePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	subtitleExtensionPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)
	subtitleExtensionAliases = map[string]string{
		"ass": "ass", "dfxp": "dfxp", "json": "json", "json3": "json3",
		"lrc": "lrc", "sami": "sami", "smi": "smi", "srt": "srt",
		"srv1": "srv1", "srv2": "srv2", "srv3": "srv3", "ssa": "ssa",
		"sub": "sub", "subrip": "srt", "text": "txt", "tml": "ttml",
		"ttml": "ttml", "txt": "txt", "vtt": "vtt", "webvtt": "vtt", "xml": "xml",
	}
	subtitleMIMEExtensions = map[string]string{
		"application/srt": "srt", "application/ttml+xml": "ttml",
		"application/x-subrip": "srt", "text/srt": "srt", "text/vtt": "vtt",
	}
)

type subtitleTrack struct {
	language  string
	extension string
	rawURL    string
	headers   http.Header
	automatic bool
	metadata  *value.Object
}

type subtitleLanguage struct {
	name   string
	tracks []subtitleTrack
}

func validateSubtitleOptions(options SubtitleOptions) error {
	if options.KeepFiles && !options.Embed {
		return fmt.Errorf("subtitle retention requires embedding")
	}
	switch options.ConvertFormat {
	case "", "srt", "ass", "vtt", "webvtt":
	default:
		return fmt.Errorf("unsupported subtitle conversion format")
	}
	options = normalizedSubtitleOptions(options)
	if !options.WriteManual && !options.WriteAutomatic {
		return nil
	}
	if len(options.Languages) > maxSubtitleRules || len(options.Format) > maxSubtitleFormatBytes {
		return fmt.Errorf("subtitle selection exceeds resource limits")
	}
	for _, rule := range options.Languages {
		if len(rule) == 0 || len(rule) > maxSubtitleRuleBytes || strings.ContainsAny(rule, "\x00\r\n") {
			return fmt.Errorf("invalid subtitle language rule")
		}
		pattern := strings.TrimPrefix(rule, "-")
		if pattern == "" {
			return fmt.Errorf("invalid subtitle language rule")
		}
		if pattern == "all" {
			continue
		}
		if _, err := regexp.Compile(`(?i)^(?:` + pattern + `)$`); err != nil {
			return fmt.Errorf("invalid subtitle language regex %q", pattern)
		}
	}
	query := options.Format
	if query == "" {
		query = "best"
	}
	parts := strings.Split(query, "/")
	if len(parts) > maxSubtitleFormatsPerLang {
		return fmt.Errorf("too many subtitle format preferences")
	}
	for _, part := range parts {
		if part != "best" && !subtitleExtensionPattern.MatchString(part) {
			return fmt.Errorf("invalid subtitle format preference %q", part)
		}
	}
	return nil
}

func selectSubtitles(info value.Info, options SubtitleOptions) ([]subtitleTrack, *value.Object, error) {
	options = normalizedSubtitleOptions(options)
	if !options.WriteManual && !options.WriteAutomatic {
		return nil, nil, nil
	}
	available := make([]subtitleLanguage, 0)
	positions := make(map[string]int)
	addCollection := func(field string, automatic bool) error {
		container, ok := info.Lookup(field).Object()
		if !ok {
			return nil
		}
		if container.Len() > maxSubtitleLanguages {
			return fmt.Errorf("%w: subtitle language limit", extractor.ErrInvalidMetadata)
		}
		for _, languageField := range container.Fields() {
			language := languageField.Key
			if !subtitleLanguagePattern.MatchString(language) {
				continue
			}
			if _, exists := positions[language]; exists {
				continue
			}
			entries, ok := languageField.Value.ListValue()
			if !ok || len(entries) > maxSubtitleFormatsPerLang {
				if ok {
					return fmt.Errorf("%w: subtitle format limit", extractor.ErrInvalidMetadata)
				}
				continue
			}
			tracks := make([]subtitleTrack, 0, len(entries))
			for _, entry := range entries {
				object, ok := entry.Object()
				if !ok {
					continue
				}
				rawURL, urlOK := object.Lookup("url").StringValue()
				if !urlOK || !validSubtitleURL(rawURL) {
					continue
				}
				extension := subtitleExtension(object, rawURL)
				if extension == "" {
					continue
				}
				metadata := object.Clone()
				metadata.Set("ext", value.String(extension))
				metadata.Set("_auto", value.Bool(automatic))
				tracks = append(tracks, subtitleTrack{
					language: language, extension: extension, rawURL: rawURL,
					automatic: automatic, metadata: metadata,
				})
			}
			if len(tracks) == 0 {
				continue
			}
			if len(available) >= maxSubtitleLanguages {
				return fmt.Errorf("%w: combined subtitle language limit", extractor.ErrInvalidMetadata)
			}
			positions[language] = len(available)
			available = append(available, subtitleLanguage{name: language, tracks: tracks})
		}
		return nil
	}
	if options.WriteManual {
		if err := addCollection("subtitles", false); err != nil {
			return nil, nil, err
		}
	}
	manualCount := len(available)
	if options.WriteAutomatic {
		if err := addCollection("automatic_captions", true); err != nil {
			return nil, nil, err
		}
	}
	if len(available) == 0 {
		return nil, nil, nil
	}

	requested, err := selectSubtitleLanguages(available, manualCount, options.Languages)
	if err != nil {
		return nil, nil, err
	}
	preferences := strings.Split(options.Format, "/")
	if options.Format == "" {
		preferences = []string{"best"}
	}
	selected := make([]subtitleTrack, 0, len(requested))
	metadata := value.NewObject()
	for _, language := range requested {
		item := available[positions[language]]
		track := chooseSubtitleFormat(item.tracks, preferences)
		track.headers, err = mediaformat.MergeHeaders(info.Lookup("http_headers"), track.metadata.Lookup("http_headers"))
		if err != nil {
			return nil, nil, err
		}
		selected = append(selected, track)
		metadata.Set(language, value.ObjectValue(track.metadata))
	}
	if options.Embed && len(selected) > maxEmbeddedSubtitleTracks {
		return nil, nil, fmt.Errorf("%w: subtitle embedding track limit", extractor.ErrInvalidMetadata)
	}
	return selected, metadata, nil
}

func normalizedSubtitleOptions(options SubtitleOptions) SubtitleOptions {
	if options.Embed && !options.WriteManual && !options.WriteAutomatic {
		options.WriteManual = true
	}
	return options
}

func subtitleExtension(metadata *value.Object, rawURL string) string {
	if extension, ok := metadata.Lookup("ext").StringValue(); ok {
		if subtitleExtensionPattern.MatchString(extension) {
			return extension
		}
		return ""
	}
	for _, field := range []string{"mime_type", "type"} {
		mimeType, ok := metadata.Lookup(field).StringValue()
		if !ok {
			continue
		}
		mimeType = strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
		if extension := subtitleMIMEExtensions[mimeType]; extension != "" {
			return extension
		}
	}
	parsed, err := url.Parse(rawURL)
	if err == nil {
		if extension := knownSubtitleExtension(strings.TrimPrefix(path.Ext(parsed.Path), ".")); extension != "" {
			return extension
		}
		for _, key := range []string{"fmt", "format", "ext"} {
			if extension := knownSubtitleExtension(parsed.Query().Get(key)); extension != "" {
				return extension
			}
		}
	}
	return "vtt"
}

func knownSubtitleExtension(candidate string) string {
	return subtitleExtensionAliases[strings.ToLower(strings.TrimSpace(candidate))]
}

func selectSubtitleLanguages(available []subtitleLanguage, manualCount int, rules []string) ([]string, error) {
	all := make([]string, len(available))
	for index := range available {
		all[index] = available[index].name
	}
	if len(rules) == 0 {
		manual := all[:manualCount]
		for _, candidates := range [][]string{
			filterSubtitleLanguages(manual, func(language string) bool { return language == "en" }),
			filterSubtitleLanguages(manual, func(language string) bool { return strings.HasPrefix(language, "en") }),
			filterSubtitleLanguages(all, func(language string) bool { return language == "en" }),
			filterSubtitleLanguages(all, func(language string) bool { return strings.HasPrefix(language, "en") }),
			manual,
			all,
		} {
			if len(candidates) != 0 {
				return []string{candidates[0]}, nil
			}
		}
		return nil, nil
	}

	requested := make([]string, 0)
	for _, rule := range rules {
		discard := strings.HasPrefix(rule, "-")
		pattern := strings.TrimPrefix(rule, "-")
		matches := all
		if pattern != "all" {
			compiled, err := regexp.Compile(`(?i)^(?:` + pattern + `)$`)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid subtitle language regex", errInvalidRequestOptions)
			}
			matches = filterSubtitleLanguages(all, compiled.MatchString)
		}
		for _, language := range matches {
			if discard {
				requested = removeSubtitleLanguage(requested, language)
			} else if !containsSubtitleLanguage(requested, language) {
				requested = append(requested, language)
			}
		}
	}
	return requested, nil
}

func filterSubtitleLanguages(languages []string, predicate func(string) bool) []string {
	result := make([]string, 0)
	for _, language := range languages {
		if predicate(language) {
			result = append(result, language)
		}
	}
	return result
}

func removeSubtitleLanguage(languages []string, target string) []string {
	result := languages[:0]
	for _, language := range languages {
		if language != target {
			result = append(result, language)
		}
	}
	return result
}

func containsSubtitleLanguage(languages []string, target string) bool {
	for _, language := range languages {
		if language == target {
			return true
		}
	}
	return false
}

func chooseSubtitleFormat(tracks []subtitleTrack, preferences []string) subtitleTrack {
	for _, preference := range preferences {
		if preference == "best" {
			return tracks[len(tracks)-1]
		}
		for index := len(tracks) - 1; index >= 0; index-- {
			if tracks[index].extension == preference {
				return tracks[index]
			}
		}
	}
	return tracks[len(tracks)-1]
}

func validSubtitleURL(rawURL string) bool {
	if rawURL == "" || len(rawURL) > maxSubtitleURLBytes || strings.ContainsAny(rawURL, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(rawURL)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func (operation *operation) downloadSubtitles(ctx context.Context, info value.Info, tracks []subtitleTrack, sink events.Sink) ([]Artifact, int64, error) {
	if len(tracks) == 0 {
		return nil, 0, nil
	}
	outputRoot := operation.request.OutputDir
	if outputRoot == "" {
		outputRoot = "."
	}
	pattern := operation.request.OutputTemplate
	if pattern == "" {
		pattern = "%(title)s.%(ext)s"
	}
	artifacts := make([]Artifact, 0, len(tracks))
	var total int64
	for _, track := range tracks {
		if err := ctx.Err(); err != nil {
			return artifacts, total, err
		}
		outputInfo := value.NewInfo(info.Fields().Clone())
		expectedExtension, ok := outputInfo.Lookup("ext").StringValue()
		if !ok || !subtitleExtensionPattern.MatchString(expectedExtension) {
			expectedExtension = "subtitle"
			outputInfo.Set("ext", value.String(expectedExtension))
		}
		base, err := outputtemplate.Resolve(outputRoot, pattern, outputInfo)
		if err != nil {
			return artifacts, total, err
		}
		destination := subtitleFilename(base, expectedExtension, track.language, track.extension)
		options := operation.request.Downloader
		if options.MaxBytes <= 0 || options.MaxBytes > maxSubtitleBytes {
			options.MaxBytes = maxSubtitleBytes
		}
		result, err := downloader.New(operation.transport).Download(ctx, downloader.Job{
			URL: track.rawURL, Headers: track.headers, OutputRoot: outputRoot, Destination: destination,
			Overwrite: operation.request.Overwrite, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
			RateLimit: options.RateLimit, MaxBytes: options.MaxBytes,
			ThrottleRate: options.ThrottleRate, ThrottleWindow: options.ThrottleWindow,
			ThrottleRestarts: options.ThrottleRestarts, FileAttempts: options.FileAttempts,
		}, sink)
		if err != nil {
			return artifacts, total, err
		}
		track.metadata.Set("filepath", value.String(result.Path))
		artifacts = append(artifacts, Artifact{Path: result.Path, Kind: "subtitle"})
		total += result.Bytes
	}
	return artifacts, total, nil
}

func subtitleFilename(base, expectedExtension, language, extension string) string {
	suffix := filepath.Ext(base)
	if suffix == "."+expectedExtension {
		base = strings.TrimSuffix(base, suffix)
	}
	return base + "." + language + "." + extension
}
