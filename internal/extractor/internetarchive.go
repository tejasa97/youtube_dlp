package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	internetArchiveMaxFiles      = 20_000
	internetArchiveMaxAssetBytes = 2 << 10
)

var (
	internetArchiveIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,254}$`)
	internetArchiveHTMLTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	ErrInternetArchiveNetwork = errors.New("Internet Archive network failure")
)

// InternetArchive extracts public audio and video files from archive.org's
// bounded metadata API. Private files are never offered without an explicit
// authenticated integration.
type InternetArchive struct{}

func NewInternetArchive() InternetArchive { return InternetArchive{} }
func (InternetArchive) Name() string      { return "internetarchive" }

func (InternetArchive) Suitable(parsed *url.URL) bool {
	_, _, ok := classifyInternetArchiveURL(parsed)
	return ok
}

func (InternetArchive) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	identifier, requestedEntry, ok := classifyInternetArchiveURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	var metadata internetArchiveMetadata
	endpoint := "https://archive.org/metadata/" + internetArchiveEscapeSegment(identifier)
	if err := RequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &metadata); err != nil {
		return Extraction{}, categorizeInternetArchiveError(err)
	}
	return normalizeInternetArchive(metadata, identifier, requestedEntry)
}

func classifyInternetArchiveURL(parsed *url.URL) (string, string, bool) {
	if parsed == nil || len(parsed.String()) > 8<<10 ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "archive.org" && host != "www.archive.org" {
		return "", "", false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 2 || (parts[0] != "details" && parts[0] != "embed") {
		return "", "", false
	}
	identifier, err := internetArchiveUnquotePlus(parts[1])
	if err != nil || !internetArchiveIdentifier.MatchString(identifier) {
		return "", "", false
	}
	entry := ""
	if len(parts) > 2 {
		decoded := make([]string, 0, len(parts)-2)
		for _, segment := range parts[2:] {
			value, err := internetArchiveUnquotePlus(segment)
			if err != nil {
				return "", "", false
			}
			decoded = append(decoded, value)
		}
		entry = strings.Join(decoded, "/")
		if !validInternetArchiveAsset(entry) {
			return "", "", false
		}
	}
	return identifier, entry, true
}

type internetArchiveMetadata struct {
	Metadata map[string]any        `json:"metadata"`
	Files    []internetArchiveFile `json:"files"`
	IsDark   any                   `json:"is_dark"`
}

type internetArchiveFile struct {
	Name        string `json:"name"`
	Original    string `json:"original"`
	Format      string `json:"format"`
	Source      string `json:"source"`
	Private     any    `json:"private"`
	Title       any    `json:"title"`
	Description any    `json:"description"`
	Creator     any    `json:"creator"`
	Length      any    `json:"length"`
	Track       any    `json:"track"`
	Album       any    `json:"album"`
	Disc        any    `json:"disc"`
	Year        any    `json:"year"`
	Width       any    `json:"width"`
	Height      any    `json:"height"`
	Size        any    `json:"size"`
}

type internetArchiveEntry struct {
	name       string
	file       internetArchiveFile
	formats    []internetArchiveFile
	thumbnails []internetArchiveFile
	subtitles  []internetArchiveFile
}

func normalizeInternetArchive(metadata internetArchiveMetadata, requestedIdentifier, requestedEntry string) (Extraction, error) {
	identifier := internetArchiveString(metadata.Metadata["identifier"])
	if identifier == "" {
		identifier = requestedIdentifier
	}
	if identifier != requestedIdentifier || !internetArchiveIdentifier.MatchString(identifier) {
		return Extraction{}, fmt.Errorf("%w: mismatched Internet Archive identifier", ErrInvalidMetadata)
	}
	if metadata.Files == nil || len(metadata.Files) > internetArchiveMaxFiles {
		return Extraction{}, fmt.Errorf("%w: invalid Internet Archive file inventory", ErrInvalidMetadata)
	}
	if internetArchivePrivate(metadata.IsDark) {
		return Extraction{}, ErrAuthentication
	}
	entries := make(map[string]*internetArchiveEntry)
	private := make(map[string]bool)
	globalThumbnails := make([]internetArchiveFile, 0)
	for _, file := range metadata.Files {
		if !validInternetArchiveAsset(file.Name) {
			return Extraction{}, fmt.Errorf("%w: unsafe Internet Archive asset", ErrInvalidMetadata)
		}
		if file.Original != "" && !validInternetArchiveAsset(file.Original) {
			return Extraction{}, fmt.Errorf("%w: unsafe Internet Archive original", ErrInvalidMetadata)
		}
		if internetArchivePrivate(file.Private) {
			private[file.Name] = true
			continue
		}
		kind := internetArchiveAssetKind(file)
		if kind == "thumbnail" && file.Original == "" {
			globalThumbnails = append(globalThumbnails, file)
			continue
		}
		if kind != "media" {
			continue
		}
		key := file.Name
		if file.Original != "" && internetArchiveMediaExtension(file.Original) {
			key = file.Original
		}
		entry := entries[key]
		if entry == nil {
			entry = &internetArchiveEntry{name: key}
			entries[key] = entry
		}
		entry.formats = append(entry.formats, file)
		if file.Name == key || strings.EqualFold(file.Source, "original") {
			entry.file = file
		}
	}
	for _, file := range metadata.Files {
		if internetArchivePrivate(file.Private) || file.Original == "" {
			continue
		}
		entry := entries[file.Original]
		if entry == nil {
			continue
		}
		switch internetArchiveAssetKind(file) {
		case "thumbnail":
			entry.thumbnails = append(entry.thumbnails, file)
		case "subtitle":
			entry.subtitles = append(entry.subtitles, file)
		}
	}
	if requestedEntry != "" {
		selected := entries[requestedEntry]
		if selected == nil {
			for _, candidate := range entries {
				for _, format := range candidate.formats {
					if format.Name == requestedEntry {
						selected = candidate
						break
					}
				}
			}
		}
		if selected == nil {
			if private[requestedEntry] {
				return Extraction{}, ErrAuthentication
			}
			return Extraction{}, ErrUnavailable
		}
		return internetArchiveMedia(identifier, metadata.Metadata, selected, globalThumbnails, true)
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		if len(private) != 0 {
			return Extraction{}, ErrAuthentication
		}
		return Extraction{}, ErrUnavailable
	}
	if len(keys) == 1 {
		return internetArchiveMedia(identifier, metadata.Metadata, entries[keys[0]], globalThumbnails, false)
	}
	playlistEntries := make([]Entry, 0, len(keys))
	for _, key := range keys {
		entry := entries[key]
		playlistEntries = append(playlistEntries, Entry{
			URL:          internetArchiveDetailsURL(identifier, key),
			ExtractorKey: "internetarchive",
			ID:           identifier + "/" + key,
			Title:        firstInternetArchiveString(internetArchiveString(entry.file.Title), key),
			Transparent:  true,
		})
	}
	info := internetArchiveItemInfo(identifier, metadata.Metadata)
	return Playlist(value.NewInfo(info), StaticEntries(playlistEntries...))
}

func internetArchiveMedia(identifier string, item map[string]any, entry *internetArchiveEntry, globalThumbnails []internetArchiveFile, entryExplicit bool) (Extraction, error) {
	if entry == nil {
		return Extraction{}, ErrUnavailable
	}
	sort.Slice(entry.formats, func(left, right int) bool {
		leftOriginal := strings.EqualFold(entry.formats[left].Source, "original")
		rightOriginal := strings.EqualFold(entry.formats[right].Source, "original")
		if leftOriginal != rightOriginal {
			return leftOriginal
		}
		return entry.formats[left].Name < entry.formats[right].Name
	})
	formats := make([]value.Value, 0, len(entry.formats))
	for _, file := range entry.formats {
		extension := strings.TrimPrefix(strings.ToLower(path.Ext(file.Name)), ".")
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String(file.Name)},
			value.Field{Key: "url", Value: value.String(internetArchiveDownloadURL(identifier, file.Name))},
			value.Field{Key: "ext", Value: value.String(extension)},
			value.Field{Key: "protocol", Value: value.String("https")},
			value.Field{Key: "format", Value: value.String(strings.TrimSpace(file.Format))},
			value.Field{Key: "format_note", Value: value.String(strings.TrimSpace(file.Source))},
		)
		setPositiveInt(format, "width", internetArchiveInt(file.Width))
		setPositiveInt(format, "height", internetArchiveInt(file.Height))
		setPositiveInt(format, "filesize", internetArchiveInt(file.Size))
		if strings.EqualFold(file.Source, "original") {
			format.Set("source_preference", value.Int(0))
		} else {
			format.Set("source_preference", value.Int(-1))
		}
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	primary := entry.file
	if primary.Name == "" {
		primary = entry.formats[0]
	}
	title := firstInternetArchiveString(internetArchiveString(primary.Title), entry.name)
	info := internetArchiveItemInfo(identifier, item)
	if entryExplicit {
		info.Set("id", value.String(identifier+"/"+entry.name))
		info.Set("title", value.String(title))
		info.Set("webpage_url", value.String(internetArchiveDetailsURL(identifier, entry.name)))
	}
	info.Set("display_id", value.String(entry.name))
	info.Set("track", value.String(strings.TrimSuffix(title, path.Ext(title))))
	info.Set("formats", value.List(formats...))
	if duration := internetArchiveDuration(primary.Length); duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	setPositiveInt(info, "track_number", internetArchiveInt(primary.Track))
	setPositiveInt(info, "disc_number", internetArchiveInt(primary.Disc))
	setPositiveInt(info, "release_year", internetArchiveInt(primary.Year))
	if album := internetArchiveString(primary.Album); album != "" {
		info.Set("album", value.String(album))
	}
	if creators := internetArchiveStrings(primary.Creator); entryExplicit && len(creators) != 0 {
		info.Set("creators", internetArchiveStringList(creators))
	}
	thumbnails := append(append([]internetArchiveFile(nil), entry.thumbnails...), globalThumbnails...)
	sort.Slice(thumbnails, func(i, j int) bool { return thumbnails[i].Name < thumbnails[j].Name })
	thumbnailValues := make([]value.Value, 0, len(thumbnails))
	for _, thumbnail := range thumbnails {
		object := value.NewObject(
			value.Field{Key: "id", Value: value.String(thumbnail.Name)},
			value.Field{Key: "url", Value: value.String(internetArchiveDownloadURL(identifier, thumbnail.Name))},
		)
		setPositiveInt(object, "width", internetArchiveInt(thumbnail.Width))
		setPositiveInt(object, "height", internetArchiveInt(thumbnail.Height))
		setPositiveInt(object, "filesize", internetArchiveInt(thumbnail.Size))
		thumbnailValues = append(thumbnailValues, value.ObjectValue(object))
	}
	if len(thumbnailValues) != 0 {
		info.Set("thumbnails", value.List(thumbnailValues...))
	}
	if subtitles := internetArchiveSubtitles(identifier, entry.subtitles); subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

func internetArchiveItemInfo(identifier string, metadata map[string]any) *value.Object {
	title := firstInternetArchiveString(internetArchiveString(metadata["title"]), identifier)
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(identifier)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String("https://archive.org/details/" + internetArchiveEscapeSegment(identifier))},
	)
	if description := cleanInternetArchiveDescription(strings.Join(internetArchiveStrings(metadata["description"]), " ")); description != "" {
		info.Set("description", value.String(description))
	}
	if uploader := firstInternetArchiveString(internetArchiveString(metadata["uploader"]), internetArchiveString(metadata["adder"])); uploader != "" {
		info.Set("uploader", value.String(uploader))
	}
	if creators := internetArchiveStrings(metadata["creator"]); len(creators) != 0 {
		info.Set("creators", internetArchiveStringList(creators))
	}
	if license := internetArchiveString(metadata["licenseurl"]); validHTTPURL(license) {
		info.Set("license", value.String(license))
	}
	if location := internetArchiveString(metadata["venue"]); location != "" {
		info.Set("location", value.String(location))
	}
	setPositiveInt(info, "release_year", internetArchiveInt(metadata["year"]))
	if date := internetArchiveDate(metadata["date"]); date != "" {
		info.Set("release_date", value.String(date))
	}
	if timestamp := internetArchiveTimestamp(firstInternetArchiveString(internetArchiveString(metadata["publicdate"]), internetArchiveString(metadata["addeddate"]))); timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	return info
}

func internetArchiveSubtitles(identifier string, files []internetArchiveFile) *value.Object {
	groups := make(map[string][]value.Value)
	for _, file := range files {
		language := "und"
		base := strings.TrimSuffix(file.Name, path.Ext(file.Name))
		if extension := strings.TrimPrefix(path.Ext(base), "."); len(extension) >= 2 && len(extension) <= 16 {
			language = strings.ToLower(extension)
		}
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(file.Name)), ".")
		groups[language] = append(groups[language], value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(internetArchiveDownloadURL(identifier, file.Name))},
			value.Field{Key: "ext", Value: value.String(ext)},
		)))
	}
	languages := make([]string, 0, len(groups))
	for language := range groups {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	result := value.NewObject()
	for _, language := range languages {
		result.Set(language, value.List(groups[language]...))
	}
	return result
}

func internetArchiveAssetKind(file internetArchiveFile) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(file.Name)), ".")
	if internetArchiveMediaExtension(file.Name) {
		return "media"
	}
	if ext == "vtt" || ext == "srt" || ext == "ass" || ext == "ssa" || ext == "ttml" {
		return "subtitle"
	}
	format := strings.ToLower(file.Format)
	if format == "thumbnail" || format == "item tile" || ext == "jpg" || ext == "jpeg" || ext == "png" || ext == "webp" {
		return "thumbnail"
	}
	return ""
}

func internetArchiveMediaExtension(name string) bool {
	switch strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".") {
	case "mp4", "m4v", "mov", "mkv", "webm", "ogv", "avi", "mpg", "mpeg", "mp3", "m4a", "flac", "ogg", "opus", "wav", "shn", "aac":
		return true
	default:
		return false
	}
}

func validInternetArchiveAsset(name string) bool {
	if name == "" || len(name) > internetArchiveMaxAssetBytes || strings.ContainsAny(name, "\\\x00?#") || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func internetArchiveDownloadURL(identifier, name string) string {
	return "https://archive.org/download/" + internetArchiveEscapeSegment(identifier) + "/" + internetArchiveEscapedPath(name)
}

func internetArchiveDetailsURL(identifier, name string) string {
	return "https://archive.org/details/" + internetArchiveEscapeSegment(identifier) + "/" + internetArchiveEscapedPath(name)
}

func internetArchiveUnquotePlus(segment string) (string, error) {
	return url.PathUnescape(strings.ReplaceAll(segment, "+", " "))
}

func internetArchiveEscapeSegment(segment string) string {
	return strings.ReplaceAll(url.PathEscape(segment), "+", "%2B")
}

func internetArchiveEscapedPath(name string) string {
	parts := strings.Split(name, "/")
	for index := range parts {
		// ArchiveOrgIE applies unquote_plus to entry paths. Escape a literal
		// plus explicitly so generated playlist URLs round-trip exactly.
		parts[index] = internetArchiveEscapeSegment(parts[index])
	}
	return strings.Join(parts, "/")
}

func internetArchiveString(input any) string {
	switch typed := input.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func internetArchiveStrings(input any) []string {
	if scalar := internetArchiveString(input); scalar != "" {
		return []string{scalar}
	}
	list, ok := input.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(list))
	for _, item := range list {
		if text := internetArchiveString(item); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func internetArchiveStringList(input []string) value.Value {
	result := make([]value.Value, len(input))
	for index, text := range input {
		result[index] = value.String(text)
	}
	return value.List(result...)
}

func internetArchiveInt(input any) int64 {
	text := internetArchiveString(input)
	if text == "" {
		return 0
	}
	if parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
		return parsed
	}
	if parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil && parsed > 0 && parsed < 9223372036854775808.0 {
		return int64(parsed)
	}
	return 0
}

func internetArchiveDuration(input any) float64 {
	text := internetArchiveString(input)
	if seconds, err := strconv.ParseFloat(text, 64); err == nil && seconds > 0 {
		return seconds
	}
	parts := strings.Split(text, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	total := 0.0
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || value < 0 || value >= 60 {
			return 0
		}
		total = total*60 + value
	}
	return total
}

func internetArchivePrivate(input any) bool {
	switch typed := input.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
	}
}

func internetArchiveDate(input any) string {
	text := internetArchiveString(input)
	for _, layout := range []string{"2006-01-02", "2006/01/02", "20060102"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.Format("20060102")
		}
	}
	return ""
}

func internetArchiveTimestamp(input string) int64 {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, input); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func firstInternetArchiveString(values ...string) string {
	for _, text := range values {
		if strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func cleanInternetArchiveDescription(input string) string {
	input = internetArchiveHTMLTag.ReplaceAllString(input, " ")
	return strings.Join(strings.Fields(html.UnescapeString(input)), " ")
}

func categorizeInternetArchiveError(err error) error {
	var status *HTTPStatusError
	if !errors.As(err, &status) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrInternetArchiveNetwork, err)
	}
	switch status.Code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %v", ErrAuthentication, status)
	case http.StatusNotFound, http.StatusGone:
		return fmt.Errorf("%w: %v", ErrUnavailable, status)
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%w: %v", ErrInternetArchiveNetwork, status)
	default:
		return err
	}
}
