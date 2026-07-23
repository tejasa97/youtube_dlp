package ytdlp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func validPrintStage(stage PrintStage) bool {
	switch stage {
	case PrintPreProcess, PrintAfterFilter, PrintVideo, PrintBeforeDL,
		PrintPostProcess, PrintAfterMove, PrintAfterVideo, PrintPlaylist:
		return true
	default:
		return false
	}
}

func (operation *operation) hasPrintStageAtOrAfter(stage PrintStage) bool {
	want := printStageRank(stage)
	for _, rule := range operation.request.PrintRules {
		if rule.Stage == PrintPlaylist {
			continue
		}
		if printStageRank(rule.Stage) >= want {
			return true
		}
	}
	return false
}

func printStageRank(stage PrintStage) int {
	switch stage {
	case PrintPreProcess:
		return 0
	case PrintAfterFilter:
		return 1
	case PrintVideo:
		return 2
	case PrintBeforeDL:
		return 3
	case PrintPostProcess:
		return 4
	case PrintAfterMove:
		return 5
	case PrintAfterVideo:
		return 6
	case PrintPlaylist:
		return 7
	default:
		return -1
	}
}

func (operation *operation) capturePrints(
	ctx context.Context,
	stage PrintStage,
	info value.Info,
	selections []mediaformat.Selection,
	filename string,
) ([]PrintOutput, error) {
	outputs := make([]PrintOutput, 0)
	for _, rule := range operation.request.PrintRules {
		if rule.Stage != stage {
			continue
		}
		if err := ctx.Err(); err != nil {
			return outputs, err
		}
		printInfo := value.NewInfo(info.Fields().Clone())
		addPrintFields(&printInfo, selections, filename, printStageRank(stage) >= printStageRank(PrintPostProcess))
		if rule.OmitIfMissing != "" {
			candidate := printInfo.Lookup(rule.OmitIfMissing)
			if candidate.IsMissing() || candidate.IsNull() {
				continue
			}
		}
		rendered, err := outputtemplate.Render(rule.Template, printInfo)
		if err != nil {
			return outputs, err
		}
		outputs = append(outputs, PrintOutput{Stage: stage, Text: rendered})
	}
	return outputs, nil
}

func (operation *operation) validatePrintRules(
	ctx context.Context,
	info value.Info,
	selections []mediaformat.Selection,
	filename string,
	playlist bool,
) error {
	for _, rule := range operation.request.PrintRules {
		if (rule.Stage == PrintPlaylist) != playlist {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		printInfo := value.NewInfo(info.Fields().Clone())
		addPrintFields(
			&printInfo, selections, filename,
			printStageRank(rule.Stage) >= printStageRank(PrintPostProcess),
		)
		if _, err := outputtemplate.Render(rule.Template, printInfo); err != nil {
			return err
		}
	}
	return nil
}

func addPrintFields(info *value.Info, selections []mediaformat.Selection, filename string, includeFilepath bool) {
	if filename != "" {
		info.Set("filename", value.String(filename))
		if includeFilepath {
			info.Set("filepath", value.String(filename))
			if extension := strings.TrimPrefix(filepath.Ext(filename), "."); extension != "" {
				info.Set("ext", value.String(extension))
			}
		}
	}
	if len(selections) > 0 {
		urls := make([]string, 0, len(selections))
		ids := make([]string, 0, len(selections))
		for _, selection := range selections {
			if selection.URL != "" {
				urls = append(urls, selection.URL)
			}
			if selection.ID != "" {
				ids = append(ids, selection.ID)
			}
		}
		if len(urls) > 0 {
			info.Set("urls", value.String(strings.Join(urls, "\n")))
			info.Set("url", value.String(urls[0]))
		}
		if len(ids) > 0 {
			format := strings.Join(ids, "+")
			info.Set("format", value.String(format))
			info.Set("format_id", value.String(format))
		}
		if !includeFilepath || filename == "" {
			info.Set("ext", value.String(mergedOutputExtension(selections)))
		}
	}
	if duration, ok := numericPrintValue(info.Lookup("duration")); ok && duration >= 0 {
		info.Set("duration_string", value.String(formatPrintDuration(duration)))
	}
}

func numericPrintValue(input value.Value) (float64, bool) {
	if integer, ok := input.Int(); ok {
		return float64(integer), true
	}
	return input.Float()
}

func formatPrintDuration(seconds float64) string {
	total := int64(seconds)
	hours, remainder := total/3600, total%3600
	minutes, secs := remainder/60, remainder%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, secs)
	}
	if minutes > 0 {
		return fmt.Sprintf("%d:%02d", minutes, secs)
	}
	return fmt.Sprintf("%d", secs)
}

func (operation *operation) printFilename(info value.Info, selections []mediaformat.Selection) (string, error) {
	pattern := operation.request.OutputTemplate
	if pattern == "" {
		pattern = "%(title)s.%(ext)s"
	}
	outputInfo := value.NewInfo(info.Fields().Clone())
	if len(selections) > 0 {
		outputInfo.Set("ext", value.String(mergedOutputExtension(selections)))
	}
	outputDir := operation.request.OutputDir
	if outputDir == "" {
		outputDir = "."
	}
	filename, err := outputtemplate.Resolve(outputDir, pattern, outputInfo)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filename), nil
}
