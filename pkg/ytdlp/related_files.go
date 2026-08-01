package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"golang.org/x/net/idna"
)

const (
	maxRelatedFileBytes = 16 << 20
	sidecarBaseExt      = "__ytdlp_sidecar_base__"
)

type relatedFile struct {
	extension string
	kind      string
	content   []byte
	linkURL   string
}

func (operation *operation) writeRelatedFiles(ctx context.Context, info value.Info, playlist bool) ([]Artifact, int64, error) {
	options := operation.request.RelatedFiles
	files := make([]relatedFile, 0, 5)
	if options.WriteInfoJSON {
		encoded, err := encodeRelatedInfo(info, cleanInfoJSON(options, playlist))
		if err != nil {
			return nil, 0, err
		}
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, encoded, "", "    "); err != nil {
			return nil, 0, fmt.Errorf("%w: format info JSON", extractor.ErrInvalidMetadata)
		}
		formatted.WriteByte('\n')
		files = append(files, relatedFile{extension: "info.json", kind: "infojson", content: formatted.Bytes()})
	}
	if options.WriteDescription {
		if description, ok := info.Lookup("description").StringValue(); ok {
			files = append(files, relatedFile{extension: "description", kind: "description", content: []byte(description)})
		}
	}
	if !playlist {
		linkTypes := selectedLinkTypes(options)
		if len(linkTypes) > 0 {
			rawURL, _ := info.Lookup("webpage_url").StringValue()
			safeURL, err := safeLinkURL(rawURL)
			if err != nil {
				if emitErr := operation.client.emit(ctx, Event{
					Kind: EventMetadataWarning, Message: "internet shortcut omitted: unsafe or unavailable webpage URL",
				}); emitErr != nil {
					return nil, 0, emitErr
				}
			} else {
				for _, linkType := range linkTypes {
					files = append(files, relatedFile{
						extension: linkType, kind: "link", content: linkContent(linkType, safeURL, ""), linkURL: safeURL,
					})
				}
			}
		}
	}
	if len(files) == 0 {
		return nil, 0, nil
	}

	artifacts := make([]Artifact, 0, len(files))
	var total int64
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return artifacts, total, err
		}
		templateType := outputTemplateTypeForRelatedFile(file.kind, playlist)
		outputRoot := operation.request.outputRoot(outputPathTypeForTemplate(templateType))
		pattern := operation.request.outputTemplate(templateType)
		destination, err := operation.relatedFilePath(outputRoot, pattern, info, file.extension)
		if err != nil {
			return artifacts, total, err
		}
		content := file.content
		if file.extension == "desktop" {
			content = linkContent("desktop", file.linkURL, strings.TrimSuffix(destination, ".desktop"))
		}
		if len(content) > maxRelatedFileBytes {
			return artifacts, total, fmt.Errorf("%w: related file exceeds %d bytes", extractor.ErrInvalidMetadata, maxRelatedFileBytes)
		}
		if err := prepareRelatedDestination(operation.request.outputRoot(OutputPathHome), destination); err != nil {
			return artifacts, total, err
		}
		if err := operation.protectTransactionPath(ctx, destination); err != nil {
			return artifacts, total, err
		}
		size, err := writeAtomicRelatedFile(ctx, destination, content, operation.request.Overwrite)
		if err != nil {
			return artifacts, total, err
		}
		artifacts = append(artifacts, Artifact{Path: destination, Kind: file.kind})
		total += size
	}
	return artifacts, total, nil
}

func cleanInfoJSON(options RelatedFileOptions, playlist bool) bool {
	if options.CleanInfoJSON == nil {
		// The pinned default keeps playlist entries available for playlist
		// metafiles while cleaning private fields from video sidecars.
		return !playlist
	}
	return *options.CleanInfoJSON
}

var cleanedInfoPrivateFields = map[string]struct{}{
	"requested_downloads": {}, "requested_formats": {}, "requested_subtitles": {},
	"requested_entries": {}, "entries": {}, "filepath": {}, "_filename": {},
	"filename": {}, "infojson_filename": {}, "original_url": {}, "playlist_autonumber": {},
}

func encodeRelatedInfo(info value.Info, clean bool) (json.RawMessage, error) {
	fields := info.Fields()
	if clean {
		fields = cleanInfoObject(fields)
	}
	encoded, err := json.Marshal(value.ObjectValue(fields))
	if err != nil {
		return nil, &Error{Category: ErrorInternal, Op: "encode metadata", Err: err}
	}
	return encoded, nil
}

func cleanInfoObject(input *value.Object) *value.Object {
	if input == nil {
		return value.NewObject()
	}
	output := value.NewObject()
	for _, field := range input.Fields() {
		if strings.HasPrefix(field.Key, "__") {
			continue
		}
		if _, private := cleanedInfoPrivateFields[field.Key]; private || field.Value.IsNull() {
			continue
		}
		output.Set(field.Key, cleanInfoValue(field.Value))
	}
	return output
}

func cleanInfoValue(input value.Value) value.Value {
	switch input.Kind() {
	case value.KindObject:
		object, _ := input.Object()
		return value.ObjectValue(cleanInfoObject(object))
	case value.KindList:
		items, _ := input.ListValue()
		cleaned := make([]value.Value, 0, len(items))
		for _, item := range items {
			cleaned = append(cleaned, cleanInfoValue(item))
		}
		return value.List(cleaned...)
	default:
		return input.Clone()
	}
}

func outputTemplateTypeForRelatedFile(kind string, playlist bool) OutputTemplateType {
	switch kind {
	case "description":
		if playlist {
			return OutputTemplatePLDescription
		}
		return OutputTemplateDescription
	case "infojson":
		if playlist {
			return OutputTemplatePLInfoJSON
		}
		return OutputTemplateInfoJSON
	case "link":
		return OutputTemplateLink
	default:
		return OutputTemplateDefault
	}
}

func selectedLinkTypes(options RelatedFileOptions) []string {
	selected := map[string]bool{
		"url": options.WriteURLLink, "webloc": options.WriteWeblocLink, "desktop": options.WriteDesktopLink,
	}
	if options.WriteLink {
		switch runtime.GOOS {
		case "darwin":
			selected["webloc"] = true
		case "linux":
			selected["desktop"] = true
		default:
			selected["url"] = true
		}
	}
	result := make([]string, 0, 3)
	for _, kind := range []string{"url", "webloc", "desktop"} {
		if selected[kind] {
			result = append(result, kind)
		}
	}
	return result
}

func safeLinkURL(rawURL string) (string, error) {
	if rawURL == "" || len(rawURL) > 16<<10 || strings.ContainsAny(rawURL, "\x00\r\n") {
		return "", errors.New("unsafe link URL")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("unsafe link URL")
	}
	hostname, err := idna.Lookup.ToASCII(parsed.Hostname())
	if err != nil || hostname == "" {
		return "", errors.New("unsafe link URL")
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	normalized := parsed.String()
	if len(normalized) > 16<<10 {
		return "", errors.New("unsafe link URL")
	}
	return normalized, nil
}

func linkContent(kind, rawURL, name string) []byte {
	switch kind {
	case "url":
		return []byte("[InternetShortcut]\r\nURL=" + rawURL + "\r\n")
	case "webloc":
		return []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
			"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n" +
			"<plist version=\"1.0\">\n<dict>\n\t<key>URL</key>\n\t<string>" + html.EscapeString(rawURL) +
			"</string>\n</dict>\n</plist>\n")
	default:
		return []byte("[Desktop Entry]\nEncoding=UTF-8\nName=" + desktopEscape(name) +
			"\nType=Link\nURL=" + rawURL + "\nIcon=text-html\n")
	}
}

func desktopEscape(input string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", `\r`, " ", `\s`)
	return replacer.Replace(input)
}

func (operation *operation) relatedFilePath(outputRoot, pattern string, info value.Info, extension string) (string, error) {
	outputInfo := value.NewInfo(info.Fields().Clone())
	outputInfo.Set("ext", value.String(sidecarBaseExt))
	base, err := operation.resolveOutputPath(outputRoot, pattern, outputInfo)
	if err != nil {
		return "", err
	}
	sentinel := "." + sidecarBaseExt
	if strings.HasSuffix(base, sentinel) {
		base = strings.TrimSuffix(base, sentinel)
	}
	return base + "." + extension, nil
}

func prepareRelatedDestination(outputRoot, destination string) error {
	root, err := filepath.Abs(outputRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("unsafe related-file output root")
	}
	parent := filepath.Dir(destination)
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return outputtemplate.ErrUnsafePath
	}
	current := root
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unsafe related-file parent")
		}
	}
	return nil
}

func writeAtomicRelatedFile(ctx context.Context, destination string, content []byte, overwrite bool) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return 0, fmt.Errorf("unsafe related-file destination")
		}
		if !overwrite {
			return info.Size(), nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".ytdlp-related-*")
	if err != nil {
		return 0, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return 0, err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return 0, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return 0, err
	}
	if err := temporary.Close(); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !overwrite {
		if err := os.Link(temporaryPath, destination); err != nil {
			if info, statErr := os.Lstat(destination); statErr == nil && info.Mode().IsRegular() {
				return info.Size(), nil
			}
			return 0, err
		}
		return int64(len(content)), nil
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return 0, err
	}
	return int64(len(content)), nil
}
