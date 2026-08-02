package ytdlp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// OutputPathType identifies a directory class used by an artifact producer.
type OutputPathType string

const (
	OutputPathHome          OutputPathType = "home"
	OutputPathSubtitle      OutputPathType = "subtitle"
	OutputPathThumbnail     OutputPathType = "thumbnail"
	OutputPathDescription   OutputPathType = "description"
	OutputPathInfoJSON      OutputPathType = "infojson"
	OutputPathLink          OutputPathType = "link"
	OutputPathPLDescription OutputPathType = "pl_description"
	OutputPathPLInfoJSON    OutputPathType = "pl_infojson"
	OutputPathPLThumbnail   OutputPathType = "pl_thumbnail"
	OutputPathChapter       OutputPathType = "chapter"
)

var orderedOutputPathTypes = []OutputPathType{
	OutputPathHome, OutputPathSubtitle, OutputPathThumbnail, OutputPathDescription,
	OutputPathInfoJSON, OutputPathLink, OutputPathPLDescription, OutputPathPLInfoJSON,
	OutputPathPLThumbnail, OutputPathChapter,
}

var supportedOutputPathTypes = func() map[OutputPathType]struct{} {
	result := make(map[OutputPathType]struct{}, len(orderedOutputPathTypes))
	for _, pathType := range orderedOutputPathTypes {
		result[pathType] = struct{}{}
	}
	return result
}()

// OutputPaths maps produced artifact types to directories. Non-home paths are
// relative to home and remain confined beneath it.
type OutputPaths map[OutputPathType]string

func cloneOutputPaths(input OutputPaths) OutputPaths {
	if len(input) == 0 {
		return nil
	}
	output := make(OutputPaths, len(input))
	for pathType, path := range input {
		output[pathType] = path
	}
	return output
}

func validateOutputPaths(request Request) error {
	if len(request.OutputPaths) > len(supportedOutputPathTypes) {
		return fmt.Errorf("too many output path types")
	}
	unknown := make([]string, 0)
	for pathType := range request.OutputPaths {
		if _, ok := supportedOutputPathTypes[pathType]; !ok {
			unknown = append(unknown, string(pathType))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unsupported output path type %q", unknown[0])
	}
	for _, pathType := range orderedOutputPathTypes {
		path, present := request.OutputPaths[pathType]
		if !present {
			continue
		}
		if strings.ContainsRune(path, 0) {
			return fmt.Errorf("unsafe output path for %q", pathType)
		}
		path = strings.TrimSpace(path)
		if pathType == OutputPathHome || path == "" || filepath.Clean(path) == "." {
			continue
		}
		if filepath.IsAbs(path) {
			return fmt.Errorf("typed output path %q must be relative", pathType)
		}
		clean := filepath.Clean(path)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("typed output path %q escapes home", pathType)
		}
	}
	return nil
}

func (request Request) outputRoot(pathType OutputPathType) string {
	home, hasHome := request.OutputPaths[OutputPathHome]
	home = strings.TrimSpace(home)
	if !hasHome || home == "" {
		home = request.OutputDir
	}
	if home == "" {
		home = "."
	}
	if pathType == "" || pathType == OutputPathHome {
		return home
	}
	if child := strings.TrimSpace(request.OutputPaths[pathType]); child != "" && filepath.Clean(child) != "." {
		return filepath.Join(home, child)
	}
	return home
}

func outputPathTypeForTemplate(templateType OutputTemplateType) OutputPathType {
	switch templateType {
	case OutputTemplateSubtitle:
		return OutputPathSubtitle
	case OutputTemplateThumbnail:
		return OutputPathThumbnail
	case OutputTemplateDescription:
		return OutputPathDescription
	case OutputTemplateInfoJSON:
		return OutputPathInfoJSON
	case OutputTemplateLink:
		return OutputPathLink
	case OutputTemplatePLDescription:
		return OutputPathPLDescription
	case OutputTemplatePLInfoJSON:
		return OutputPathPLInfoJSON
	case OutputTemplatePLThumbnail:
		return OutputPathPLThumbnail
	case OutputTemplateChapter:
		return OutputPathChapter
	default:
		return OutputPathHome
	}
}
