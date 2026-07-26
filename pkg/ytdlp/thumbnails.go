package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxThumbnails        = 256
	maxThumbnailURLBytes = 8 << 10
	maxThumbnailBytes    = 16 << 20
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
	preference float64
	width      float64
	height     float64
	metadata   *value.Object
	index      int
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
		id := thumbnailOriginalID(metadata.Lookup("id"))
		metadata.Set("ext", value.String(extension))
		tracks = append(tracks, thumbnailTrack{
			id: id, extension: extension, rawURL: rawURL, headers: headers,
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
	if !options.Write && !options.WriteAll {
		return nil, 0, nil
	}
	tracks, err := selectThumbnails(info)
	if err != nil || len(tracks) == 0 {
		return nil, 0, err
	}
	outputRoot := operation.request.OutputDir
	if outputRoot == "" {
		outputRoot = "."
	}
	templateType := OutputTemplateThumbnail
	if playlist {
		templateType = OutputTemplatePLThumbnail
	}
	pattern := operation.request.outputTemplate(templateType)
	writeAll := options.WriteAll
	multiple := writeAll && len(tracks) > 1
	artifacts := make([]Artifact, 0, len(tracks))
	failed := make(map[*value.Object]bool)
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
		options := operation.request.Downloader
		if options.MaxBytes <= 0 || options.MaxBytes > maxThumbnailBytes {
			options.MaxBytes = maxThumbnailBytes
		}
		result, downloadErr := downloader.New(thumbnailRedirectTransport{client: operation.transport}).Download(ctx, downloader.Job{
			URL: track.rawURL, Headers: track.headers, OutputRoot: outputRoot, Destination: destination,
			Overwrite: operation.request.Overwrite, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
			RateLimit: options.RateLimit, MaxBytes: options.MaxBytes,
			ThrottleRate: options.ThrottleRate, ThrottleWindow: options.ThrottleWindow,
			ThrottleRestarts: options.ThrottleRestarts, FileAttempts: options.FileAttempts,
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
		track.metadata.Set("filepath", value.String(result.Path))
		artifacts = append(artifacts, Artifact{Path: result.Path, Kind: "thumbnail"})
		total += result.Bytes
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
