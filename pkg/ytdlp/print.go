package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

var errUnsafePrintFile = errors.New("unsafe print-to-file destination")

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
		if rule.Stage != stage || rule.FileTemplate != "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return outputs, err
		}
		printInfo := value.NewInfo(info.Fields().Clone())
		if err := addPrintFields(&printInfo, selections, filename, printStageRank(stage) >= printStageRank(PrintPostProcess)); err != nil {
			return outputs, err
		}
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
		if err := addPrintFields(
			&printInfo, selections, filename,
			printStageRank(rule.Stage) >= printStageRank(PrintPostProcess),
		); err != nil {
			return err
		}
		if _, err := outputtemplate.Render(rule.Template, printInfo); err != nil {
			return err
		}
		if rule.FileTemplate != "" {
			outputRoot := operation.request.OutputDir
			if outputRoot == "" {
				outputRoot = "."
			}
			if _, err := outputtemplate.Resolve(outputRoot, rule.FileTemplate, printInfo); err != nil {
				return err
			}
		}
	}
	return nil
}

func (operation *operation) writePrintFiles(
	ctx context.Context,
	stage PrintStage,
	info value.Info,
	selections []mediaformat.Selection,
	filename string,
) ([]Artifact, int64, error) {
	if operation.request.Simulate {
		return nil, 0, nil
	}
	outputRoot := operation.request.OutputDir
	if outputRoot == "" {
		outputRoot = "."
	}
	artifacts := make([]Artifact, 0)
	var total int64
	for _, rule := range operation.request.PrintRules {
		if rule.Stage != stage || rule.FileTemplate == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return artifacts, total, err
		}
		printInfo := value.NewInfo(info.Fields().Clone())
		if err := addPrintFields(&printInfo, selections, filename, printStageRank(stage) >= printStageRank(PrintPostProcess)); err != nil {
			return artifacts, total, err
		}
		if rule.OmitIfMissing != "" {
			candidate := printInfo.Lookup(rule.OmitIfMissing)
			if candidate.IsMissing() || candidate.IsNull() {
				continue
			}
		}
		rendered, err := outputtemplate.Render(rule.Template, printInfo)
		if err != nil {
			return artifacts, total, err
		}
		destination, err := outputtemplate.Resolve(outputRoot, rule.FileTemplate, printInfo)
		if err != nil {
			return artifacts, total, err
		}
		if err := prepareRelatedDestination(outputRoot, destination); err != nil {
			return artifacts, total, fmt.Errorf("%w: %v", errUnsafePrintFile, err)
		}
		written, err := appendPrintLine(ctx, destination, rendered)
		if err != nil {
			return artifacts, total, err
		}
		artifacts = appendUniqueArtifact(artifacts, Artifact{Path: destination, Kind: "print"})
		total += written
	}
	return artifacts, total, nil
}

func appendPrintLine(ctx context.Context, destination, rendered string) (int64, error) {
	lineBreak := "\n"
	if runtime.GOOS == "windows" {
		lineBreak = "\r\n"
	}
	line := rendered + lineBreak
	if len(line) > 1<<20 {
		return 0, fmt.Errorf("%w: print-to-file line exceeds size limit", outputtemplate.ErrInvalidTemplate)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return 0, errUnsafePrintFile
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	file, err := openPrintAppendFile(destination)
	if err != nil {
		return 0, err
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		file.Close()
		if statErr != nil {
			return 0, statErr
		}
		return 0, errUnsafePrintFile
	}
	if err := ctx.Err(); err != nil {
		file.Close()
		return 0, err
	}
	written, writeErr := io.WriteString(file, line)
	if writeErr == nil && written != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return int64(written), writeErr
	}
	if closeErr != nil {
		return int64(written), closeErr
	}
	return int64(written), nil
}

func appendUniqueArtifact(artifacts []Artifact, artifact Artifact) []Artifact {
	for _, existing := range artifacts {
		if existing.Path == artifact.Path && existing.Kind == artifact.Kind {
			return artifacts
		}
	}
	return append(artifacts, artifact)
}

func mergePrintArtifacts(existing, added []Artifact) []Artifact {
	for _, artifact := range added {
		existing = appendUniqueArtifact(existing, artifact)
	}
	return existing
}

func addPrintFileArtifacts(result *Result, artifacts []Artifact, bytes int64) {
	result.Artifacts = mergePrintArtifacts(result.Artifacts, artifacts)
	result.Bytes += bytes
	result.Downloaded = result.Downloaded || len(artifacts) > 0
}

func addPrintFields(info *value.Info, selections []mediaformat.Selection, filename string, includeFilepath bool) error {
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
	return addPrintTableFields(info)
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
