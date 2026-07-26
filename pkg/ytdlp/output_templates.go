package ytdlp

import (
	"fmt"
	"sort"
	"strings"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
)

// OutputTemplateType identifies the artifact class rendered by an output
// template. The supported values are the yt-dlp types currently produced by
// this Go port.
type OutputTemplateType string

const (
	OutputTemplateDefault       OutputTemplateType = "default"
	OutputTemplateSubtitle      OutputTemplateType = "subtitle"
	OutputTemplateThumbnail     OutputTemplateType = "thumbnail"
	OutputTemplateDescription   OutputTemplateType = "description"
	OutputTemplateInfoJSON      OutputTemplateType = "infojson"
	OutputTemplateLink          OutputTemplateType = "link"
	OutputTemplatePLDescription OutputTemplateType = "pl_description"
	OutputTemplatePLInfoJSON    OutputTemplateType = "pl_infojson"
	OutputTemplatePLThumbnail   OutputTemplateType = "pl_thumbnail"
)

var orderedOutputTemplateTypes = []OutputTemplateType{
	OutputTemplateDefault, OutputTemplateSubtitle, OutputTemplateThumbnail, OutputTemplateDescription,
	OutputTemplateInfoJSON, OutputTemplateLink, OutputTemplatePLDescription,
	OutputTemplatePLInfoJSON, OutputTemplatePLThumbnail,
}

var supportedOutputTemplateTypes = map[OutputTemplateType]struct{}{
	OutputTemplateDefault: {}, OutputTemplateSubtitle: {}, OutputTemplateThumbnail: {}, OutputTemplateDescription: {},
	OutputTemplateInfoJSON: {}, OutputTemplateLink: {}, OutputTemplatePLDescription: {},
	OutputTemplatePLInfoJSON: {}, OutputTemplatePLThumbnail: {},
}

// OutputTemplates maps an artifact type to its filename template. A missing
// artifact type falls back to default.
type OutputTemplates map[OutputTemplateType]string

func (request Request) outputTemplate(templateType OutputTemplateType) string {
	if pattern := request.OutputTemplates[templateType]; pattern != "" {
		return pattern
	}
	if pattern := request.OutputTemplates[OutputTemplateDefault]; pattern != "" {
		return pattern
	}
	if request.OutputTemplate != "" {
		return request.OutputTemplate
	}
	return "%(title)s.%(ext)s"
}

func cloneOutputTemplates(input OutputTemplates) OutputTemplates {
	if len(input) == 0 {
		return nil
	}
	output := make(OutputTemplates, len(input))
	for templateType, pattern := range input {
		output[templateType] = pattern
	}
	return output
}

func validateOutputTemplates(request Request) error {
	if request.OutputTemplate != "" {
		if err := outputtemplate.Validate(request.OutputTemplate); err != nil {
			return err
		}
	}
	if len(request.OutputTemplates) > len(supportedOutputTemplateTypes) {
		return fmt.Errorf("too many output template types")
	}
	unknown := make([]string, 0)
	for templateType, pattern := range request.OutputTemplates {
		if _, supported := supportedOutputTemplateTypes[templateType]; !supported {
			unknown = append(unknown, string(templateType))
		} else if pattern == "" || strings.ContainsRune(pattern, 0) {
			continue
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unsupported output template type %q", unknown[0])
	}
	for _, templateType := range orderedOutputTemplateTypes {
		pattern, present := request.OutputTemplates[templateType]
		if !present {
			continue
		}
		if pattern == "" || strings.ContainsRune(pattern, 0) {
			return fmt.Errorf("empty or unsafe output template for %q", templateType)
		}
		if err := outputtemplate.Validate(pattern); err != nil {
			return fmt.Errorf("%s: %w", templateType, err)
		}
	}
	return nil
}
