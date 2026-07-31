package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/postprocess"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxThumbnails        = 256
	maxThumbnailURLBytes = 8 << 10
	maxThumbnailBytes    = 16 << 20
	maxThumbnailMapping  = 256
	maxThumbnailRules    = 16
)

var thumbnailIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var errUnsafeThumbnailRedirect = errors.New("unsafe thumbnail redirect")

var thumbnailExtensions = map[string]string{
	"avif": "avif", "gif": "gif", "heic": "heic", "heif": "heif",
	"jpeg": "jpeg", "jpg": "jpg", "png": "png", "webp": "webp",
}

type thumbnailTrack struct {
	id         string
	extension  string
	rawURL     string
	headers    http.Header
	isolated   bool
	hostPolicy string
	preference float64
	width      float64
	height     float64
	metadata   *value.Object
	index      int
}

type thumbnailConversionRule struct {
	source string
	target string
}

type thumbnailConversionMapping []thumbnailConversionRule

type thumbnailConvertFunc func(
	context.Context, string, string, string, bool, events.Sink,
) error

func parseThumbnailConversionMapping(input string) (thumbnailConversionMapping, error) {
	normalized := strings.ToLower(strings.TrimSpace(input))
	if normalized == "" || normalized == "none" {
		return nil, nil
	}
	if len(normalized) > maxThumbnailMapping || strings.ContainsAny(normalized, "\x00\r\n") {
		return nil, fmt.Errorf("invalid thumbnail conversion mapping")
	}
	parts := strings.Split(normalized, "/")
	if len(parts) > maxThumbnailRules {
		return nil, fmt.Errorf("thumbnail conversion mapping exceeds %d rules", maxThumbnailRules)
	}
	rules := make(thumbnailConversionMapping, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		source, target, conditional := strings.Cut(part, ">")
		if conditional {
			source = strings.TrimSpace(source)
			target = strings.TrimSpace(target)
			if source == "" || strings.Contains(target, ">") || !thumbnailMappingSource(source) {
				return nil, fmt.Errorf("invalid thumbnail conversion rule %q", part)
			}
		} else {
			target = source
			source = ""
		}
		switch target {
		case "jpg", "png", "webp":
		default:
			return nil, fmt.Errorf("unsupported thumbnail conversion format %q", target)
		}
		rules = append(rules, thumbnailConversionRule{source: source, target: target})
	}
	return rules, nil
}

func thumbnailMappingSource(input string) bool {
	if len(input) == 0 || len(input) > 32 {
		return false
	}
	for _, character := range input {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func (mapping thumbnailConversionMapping) resolve(source string) (string, bool) {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "jpeg" {
		source = "jpg"
	}
	for _, rule := range mapping {
		if rule.source != "" && rule.source != source {
			continue
		}
		if rule.target == source {
			return "", false
		}
		return rule.target, true
	}
	return "", false
}

func selectThumbnails(info *value.Info) ([]thumbnailTrack, error) {
	if info == nil {
		return nil, nil
	}
	raw, hasList := info.Lookup("thumbnails").ListValue()
	hadSource := hasList
	if hasList && len(raw) > maxThumbnails {
		return nil, fmt.Errorf("%w: thumbnail limit", extractor.ErrInvalidMetadata)
	}
	if !hasList || len(raw) == 0 {
		if thumbnail, ok := info.Lookup("thumbnail").StringValue(); ok {
			hadSource = true
			raw = []value.Value{value.ObjectValue(value.NewObject(
				value.Field{Key: "id", Value: value.String("0")},
				value.Field{Key: "url", Value: value.String(thumbnail)},
			))}
		}
	}
	tracks := make([]thumbnailTrack, 0, len(raw))
	for index, item := range raw {
		object, ok := item.Object()
		if !ok {
			continue
		}
		rawURL, ok := object.Lookup("url").StringValue()
		if !ok || !validThumbnailURL(rawURL) {
			continue
		}
		extension := thumbnailExtension(object, rawURL)
		if extension == "" {
			continue
		}
		headers, err := mediaformat.MergeHeaders(object.Lookup("http_headers"))
		if err != nil {
			return nil, err
		}
		metadata := object.Clone()
		isolated, _ := object.Lookup("_credential_isolated").Bool()
		policy, _ := object.Lookup("_ted_host_policy").StringValue()
		id := thumbnailOriginalID(metadata.Lookup("id"))
		metadata.Set("ext", value.String(extension))
		tracks = append(tracks, thumbnailTrack{
			id: id, extension: extension, rawURL: rawURL, headers: headers,
			isolated: isolated || policy != "", hostPolicy: policy,
			preference: thumbnailNumber(metadata.Lookup("preference")),
			width:      thumbnailNumber(metadata.Lookup("width")),
			height:     thumbnailNumber(metadata.Lookup("height")),
			metadata:   metadata, index: index,
		})
	}
	sort.SliceStable(tracks, func(left, right int) bool {
		a, b := tracks[left], tracks[right]
		if a.preference != b.preference {
			return a.preference < b.preference
		}
		if a.width != b.width {
			return a.width < b.width
		}
		if a.height != b.height {
			return a.height < b.height
		}
		if a.id != b.id {
			return a.id < b.id
		}
		if a.rawURL != b.rawURL {
			return a.rawURL < b.rawURL
		}
		return a.index < b.index
	})
	for index := range tracks {
		if tracks[index].id == "" {
			tracks[index].id = strconv.Itoa(index)
		}
		tracks[index].metadata.Set("id", value.String(tracks[index].id))
	}
	if hadSource {
		normalized := make([]value.Value, len(tracks))
		for index := range tracks {
			normalized[index] = value.ObjectValue(tracks[index].metadata)
		}
		info.Set("thumbnails", value.List(normalized...))
	}
	return tracks, nil
}

func (operation *operation) writeThumbnails(ctx context.Context, info *value.Info, playlist bool) ([]Artifact, int64, error) {
	options := operation.request.Thumbnails
	if !options.Write && !options.WriteAll && !options.Embed {
		return nil, 0, nil
	}
	mapping, err := parseThumbnailConversionMapping(options.ConvertFormat)
	if err != nil {
		return nil, 0, err
	}
	tracks, err := selectThumbnails(info)
	if err != nil || len(tracks) == 0 {
		return nil, 0, err
	}
	templateType := OutputTemplateThumbnail
	if playlist {
		templateType = OutputTemplatePLThumbnail
	}
	outputRoot := operation.request.outputRoot(outputPathTypeForTemplate(templateType))
	pattern := operation.request.outputTemplate(templateType)
	writeAll := options.WriteAll
	multiple := writeAll && len(tracks) > 1
	seen := make(map[string]struct{}, len(tracks))
	for _, track := range tracks {
		source, pathErr := thumbnailPath(outputRoot, pattern, *info, track, multiple)
		if pathErr != nil {
			return nil, 0, pathErr
		}
		final, pathErr := thumbnailConversionPath(operation.request.outputRoot(OutputPathHome), source, track.extension, mapping)
		if pathErr != nil {
			return nil, 0, pathErr
		}
		if writeAll {
			if _, duplicate := seen[final]; duplicate {
				return nil, 0, fmt.Errorf("%w: duplicate thumbnail destination", extractor.ErrInvalidMetadata)
			}
			seen[final] = struct{}{}
		}
	}
	converter := operation.thumbnailConvert
	var tools *ffmpeg.Toolset
	if converter == nil {
		converter = func(ctx context.Context, source, destination, format string, overwrite bool, sink events.Sink) error {
			if tools == nil {
				discovered, discoverErr := ffmpeg.Discover(ffmpeg.Config{})
				if discoverErr != nil {
					return discoverErr
				}
				tools = discovered
			}
			return tools.ConvertImage(ctx, source, destination, ffmpeg.ImageOptions{Format: format}, overwrite, sink)
		}
	}
	artifacts := make([]Artifact, 0, len(tracks))
	failed := make(map[*value.Object]bool)
	committedPaths := make(map[string]struct{}, len(tracks))
	var total int64
	for index := len(tracks) - 1; index >= 0; index-- {
		if err := ctx.Err(); err != nil {
			return artifacts, total, err
		}
		track := tracks[index]
		destination, err := thumbnailPath(outputRoot, pattern, *info, track, multiple)
		if err != nil {
			return artifacts, total, err
		}
		if writeAll {
			if _, duplicate := committedPaths[destination]; duplicate {
				return artifacts, total, fmt.Errorf("%w: duplicate thumbnail destination", extractor.ErrInvalidMetadata)
			}
		}
		if err := operation.protectTransactionPath(ctx, destination); err != nil {
			return artifacts, total, err
		}
		downloadOptions := operation.request.Downloader
		if downloadOptions.MaxBytes <= 0 || downloadOptions.MaxBytes > maxThumbnailBytes {
			downloadOptions.MaxBytes = maxThumbnailBytes
		}
		downloadTransport := network.Doer(thumbnailRedirectTransport{client: operation.transport})
		if track.isolated {
			switch track.hostPolicy {
			case "ted":
				downloadTransport = newTedCredentialIsolatedTransport(operation.transport, "thumbnail")
			case "":
				downloadTransport = newCredentialIsolatedTransport(operation.transport)
			default:
				// A nonempty extractor marker must never fall back to the generic
				// transport when its policy is unknown.
				downloadTransport = newTedCredentialIsolatedTransport(operation.transport, "")
			}
		}
		result, downloadErr := downloader.New(downloadTransport).Download(ctx, downloader.Job{
			URL: track.rawURL, Headers: track.headers, OutputRoot: operation.request.outputRoot(OutputPathHome), Destination: destination,
			Overwrite: operation.request.Overwrite, Attempts: downloadOptions.Attempts,
			RetryBaseDelay: downloadOptions.RetryBaseDelay, RetryMaxDelay: downloadOptions.RetryMaxDelay,
			RateLimit: downloadOptions.RateLimit, MaxBytes: downloadOptions.MaxBytes,
			ThrottleRate: downloadOptions.ThrottleRate, ThrottleWindow: downloadOptions.ThrottleWindow,
			ThrottleRestarts: downloadOptions.ThrottleRestarts, FileAttempts: downloadOptions.FileAttempts,
		}, operation.eventSink())
		if downloadErr != nil {
			if errors.Is(downloadErr, context.Canceled) || errors.Is(downloadErr, context.DeadlineExceeded) {
				return artifacts, total, downloadErr
			}
			if !thumbnailRemoteFailure(downloadErr) {
				return artifacts, total, downloadErr
			}
			failed[track.metadata] = true
			if emitErr := operation.client.emit(ctx, Event{
				Kind: EventMetadataWarning, Message: "thumbnail download failed; trying another candidate",
			}); emitErr != nil {
				return artifacts, total, emitErr
			}
			if writeAll {
				continue
			}
			continue
		}
		artifactPath := result.Path
		artifactBytes := result.Bytes
		corrected := false
		if (len(mapping) > 0 || options.Embed) && !strings.EqualFold(track.extension, "webp") {
			webp, magicErr := thumbnailHasWebPMagic(result.Path)
			if magicErr != nil {
				return artifacts, total, magicErr
			}
			if webp {
				correctedPath, pathErr := convertedThumbnailPath(
					operation.request.outputRoot(OutputPathHome), result.Path, "webp",
				)
				if pathErr != nil {
					return artifacts, total, pathErr
				}
				artifactPath, corrected = correctedPath, true
			}
		}
		actualExtension := track.extension
		if corrected {
			actualExtension = "webp"
		}
		target, convert := mapping.resolve(actualExtension)
		finalPath := artifactPath
		if convert {
			finalPath, err = convertedThumbnailPath(
				operation.request.outputRoot(OutputPathHome), artifactPath, target,
			)
			if err != nil {
				return artifacts, total, err
			}
		}
		if writeAll {
			if _, duplicate := committedPaths[finalPath]; duplicate {
				return artifacts, total, fmt.Errorf("%w: duplicate thumbnail destination", extractor.ErrInvalidMetadata)
			}
			if corrected && artifactPath != finalPath {
				if _, duplicate := committedPaths[artifactPath]; duplicate {
					return artifacts, total, fmt.Errorf("%w: duplicate thumbnail destination", extractor.ErrInvalidMetadata)
				}
			}
		}
		if corrected {
			if err := postprocess.SafeMoveContext(
				ctx, result.Path, artifactPath, operation.request.Overwrite,
			); err != nil {
				return artifacts, total, err
			}
			track.extension = actualExtension
			track.metadata.Set("ext", value.String(actualExtension))
		}
		if convert {
			conversionSource := artifactPath
			sourceInfo, statErr := os.Lstat(conversionSource)
			if statErr != nil || sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
				return artifacts, total, fmt.Errorf("%w: thumbnail source is not a regular file", ffmpeg.ErrInvalidOperation)
			}
			destination, pathErr := convertedThumbnailPath(
				operation.request.outputRoot(OutputPathHome), conversionSource, target,
			)
			if pathErr != nil {
				return artifacts, total, pathErr
			}
			if destination != finalPath {
				return artifacts, total, fmt.Errorf("%w: unstable thumbnail conversion path", ffmpeg.ErrInvalidOperation)
			}
			if convertErr := converter(
				ctx, conversionSource, destination, target, operation.request.Overwrite, operation.eventSink(),
			); convertErr != nil {
				return artifacts, total, convertErr
			}
			destinationInfo, statErr := os.Lstat(destination)
			if statErr != nil || destinationInfo.Mode()&os.ModeSymlink != 0 || !destinationInfo.Mode().IsRegular() {
				return artifacts, total, fmt.Errorf("%w: converted thumbnail is not a regular file", ffmpeg.ErrInvalidOperation)
			}
			artifactPath, artifactBytes = destination, destinationInfo.Size()
			track.metadata.Set("ext", value.String(target))
			retainedSource := false
			removeErr := operation.removeLocalFile(conversionSource)
			if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				operation.emitThumbnailCleanupWarning(ctx, conversionSource)
				retainedSource = true
			}
			track.metadata.Set("filepath", value.String(artifactPath))
			artifacts = append(artifacts, Artifact{Path: artifactPath, Kind: "thumbnail"})
			total += artifactBytes
			if retainedSource {
				artifacts = append(artifacts, Artifact{Path: conversionSource, Kind: "thumbnail"})
				total += result.Bytes
				committedPaths[conversionSource] = struct{}{}
			}
			if !writeAll {
				break
			}
			committedPaths[artifactPath] = struct{}{}
			continue
		}
		track.metadata.Set("filepath", value.String(artifactPath))
		artifacts = append(artifacts, Artifact{Path: artifactPath, Kind: "thumbnail"})
		total += artifactBytes
		committedPaths[artifactPath] = struct{}{}
		if !writeAll {
			break
		}
	}
	if len(failed) > 0 {
		retained := make([]value.Value, 0, len(tracks)-len(failed))
		for _, track := range tracks {
			if !failed[track.metadata] {
				retained = append(retained, value.ObjectValue(track.metadata))
			}
		}
		info.Set("thumbnails", value.List(retained...))
	}
	return artifacts, total, nil
}

func thumbnailConversionPath(
	home, source, sourceExtension string, mapping thumbnailConversionMapping,
) (string, error) {
	target, convert := mapping.resolve(sourceExtension)
	if !convert {
		return source, nil
	}
	return convertedThumbnailPath(home, source, target)
}

func convertedThumbnailPath(home, source, target string) (string, error) {
	extension := filepath.Ext(source)
	if extension == "" {
		return "", fmt.Errorf("%w: thumbnail source has no extension", ffmpeg.ErrInvalidOperation)
	}
	destination := strings.TrimSuffix(source, extension) + "." + target
	return confinedPostprocessPath(home, destination)
}

func thumbnailHasWebPMagic(filename string) (bool, error) {
	file, err := os.Open(filename)
	if err != nil {
		return false, err
	}
	defer file.Close()
	var header [12]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	return string(header[0:4]) == "RIFF" && string(header[8:12]) == "WEBP", nil
}

func (operation *operation) emitThumbnailCleanupWarning(ctx context.Context, source string) {
	if operation.client == nil {
		return
	}
	// Conversion has already committed its replacement. Observer failures
	// therefore cannot roll back the operation and are intentionally ignored.
	_ = operation.client.emit(ctx, Event{
		Kind: EventMetadataWarning, Path: source,
		Message: "could not remove a superseded thumbnail sidecar; it remains in the result artifacts",
	})
}

func thumbnailPath(outputRoot, pattern string, info value.Info, track thumbnailTrack, multiple bool) (string, error) {
	outputInfo := value.NewInfo(info.Fields().Clone())
	outputInfo.Set("ext", value.String(track.extension))
	base, err := outputtemplate.Resolve(outputRoot, pattern, outputInfo)
	if err != nil {
		return "", err
	}
	suffix := "." + track.extension
	if strings.HasSuffix(strings.ToLower(base), suffix) {
		base = base[:len(base)-len(suffix)]
	}
	if multiple {
		base += "." + track.id
	}
	return base + suffix, nil
}

func validThumbnailURL(rawURL string) bool {
	if rawURL == "" || len(rawURL) > maxThumbnailURLBytes || strings.ContainsAny(rawURL, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(rawURL)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func thumbnailExtension(object *value.Object, rawURL string) string {
	if declared, ok := object.Lookup("ext").StringValue(); ok {
		if declared = strings.ToLower(strings.TrimSpace(declared)); declared != "" {
			return thumbnailExtensions[declared]
		}
	}
	parsed, err := url.Parse(rawURL)
	if err == nil {
		if extension := thumbnailExtensions[strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), "."))]; extension != "" {
			return extension
		}
	}
	return "jpg"
}

func thumbnailOriginalID(input value.Value) string {
	if text, ok := input.StringValue(); ok && thumbnailIdentifierPattern.MatchString(text) {
		return text
	}
	if integer, ok := input.Int(); ok && integer >= 0 {
		text := strconv.FormatInt(integer, 10)
		if thumbnailIdentifierPattern.MatchString(text) {
			return text
		}
	}
	return ""
}

func thumbnailNumber(input value.Value) float64 {
	if integer, ok := input.Int(); ok {
		return float64(integer)
	}
	if number, ok := input.Float(); ok {
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return -1
		}
		return number
	}
	return -1
}

func thumbnailRemoteFailure(err error) bool {
	var requestError *network.RequestError
	var statusError *network.StatusError
	var downloadStatus *downloader.HTTPStatusError
	return errors.As(err, &requestError) || errors.As(err, &statusError) || errors.As(err, &downloadStatus)
}

type thumbnailRedirectTransport struct{ client *network.Client }

func (transport thumbnailRedirectTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if transport.client == nil || request == nil || request.URL == nil {
		return nil, errUnsafeThumbnailRedirect
	}
	current := request.Clone(ctx)
	current.Header = request.Header.Clone()
	visited := make(map[string]bool)
	for hop := 0; hop <= 5; hop++ {
		key := current.URL.String()
		if visited[key] {
			return nil, errUnsafeThumbnailRedirect
		}
		visited[key] = true
		response, err := transport.client.DoNoRedirect(ctx, current)
		if err != nil {
			return nil, err
		}
		switch response.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
			http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		default:
			return response, nil
		}
		location, err := response.Location()
		response.Body.Close()
		if err != nil {
			return nil, errUnsafeThumbnailRedirect
		}
		next := current.URL.ResolveReference(location)
		if !validThumbnailURL(next.String()) ||
			(current.URL.Scheme == "https" && next.Scheme != "https") {
			return nil, errUnsafeThumbnailRedirect
		}
		headers := current.Header.Clone()
		if !strings.EqualFold(current.URL.Scheme, next.Scheme) ||
			!strings.EqualFold(current.URL.Host, next.Host) {
			headers.Del("Authorization")
			headers.Del("Cookie")
			headers.Del("Proxy-Authorization")
		}
		current = current.Clone(ctx)
		current.URL = next
		current.Host = ""
		current.Header = headers
		if response.StatusCode == http.StatusSeeOther {
			current.Method = http.MethodGet
			current.Body = nil
		}
	}
	return nil, errUnsafeThumbnailRedirect
}
