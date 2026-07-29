package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

var errUnsafePrintFile = errors.New("unsafe print-to-file destination")

var selectedFormatFieldNames = []string{
	"url", "manifest_url", "manifest_stream_number", "ext", "format", "format_id", "format_note", "available_at",
	"width", "height", "aspect_ratio", "resolution", "dynamic_range", "tbr", "abr", "acodec", "asr", "audio_channels",
	"vbr", "fps", "vcodec", "container", "filesize", "filesize_approx", "rows", "columns", "hls_media_playlist_data",
	"player_url", "protocol", "fragment_base_url", "fragments", "is_from_start", "is_dash_periods", "request_data",
	"preference", "language", "language_preference", "quality", "source_preference", "cookies",
	"http_headers", "stretched_ratio", "no_resume", "has_drm", "extra_param_to_segment_url", "extra_param_to_key_url",
	"hls_aes", "downloader_options", "impersonate", "page_url", "app", "play_path", "tc_url", "flash_version",
	"rtmp_live", "rtmp_conn", "rtmp_protocol", "rtmp_real_time",
}

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
	plan *mediaformat.OutputPlan,
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
		includeFilepath := printStageRank(stage) >= printStageRank(PrintPostProcess)
		if err := operation.addPrintFields(&printInfo, plan, selections, filename, includeFilepath); err != nil {
			return outputs, err
		}
		if !includeFilepath {
			operation.applyThumbnailEmbeddingOutputExtension(&printInfo, selections)
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
	plan *mediaformat.OutputPlan,
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
		includeFilepath := printStageRank(rule.Stage) >= printStageRank(PrintPostProcess)
		if err := operation.addPrintFields(&printInfo, plan, selections, filename, includeFilepath); err != nil {
			return err
		}
		if !includeFilepath {
			operation.applyThumbnailEmbeddingOutputExtension(&printInfo, selections)
		}
		if _, err := outputtemplate.Render(rule.Template, printInfo); err != nil {
			return err
		}
		if rule.FileTemplate != "" {
			outputRoot := operation.request.outputRoot(OutputPathHome)
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
	plan *mediaformat.OutputPlan,
	selections []mediaformat.Selection,
	filename string,
) ([]Artifact, int64, error) {
	if operation.request.Simulate {
		return nil, 0, nil
	}
	outputRoot := operation.request.outputRoot(OutputPathHome)
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
		includeFilepath := printStageRank(stage) >= printStageRank(PrintPostProcess)
		if err := operation.addPrintFields(&printInfo, plan, selections, filename, includeFilepath); err != nil {
			return artifacts, total, err
		}
		if !includeFilepath {
			operation.applyThumbnailEmbeddingOutputExtension(&printInfo, selections)
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

func (operation *operation) addPrintFields(
	info *value.Info,
	plan *mediaformat.OutputPlan,
	selections []mediaformat.Selection,
	filename string,
	includeFilepath bool,
) error {
	if filename != "" {
		info.Set("filename", value.String(filename))
		if includeFilepath {
			info.Set("filepath", value.String(filename))
			if extension := strings.TrimPrefix(filepath.Ext(filename), "."); extension != "" {
				info.Set("ext", value.String(extension))
			}
		}
	}
	if plan != nil && len(plan.Tracks) > 0 {
		operation.applyPrintSelectionFields(info, plan)
	} else if len(selections) > 0 {
		*info = selectedFormatInfo(*info, selections)
	}
	if includeFilepath && filename != "" {
		if extension := strings.TrimPrefix(filepath.Ext(filename), "."); extension != "" {
			info.Set("ext", value.String(extension))
		}
	}
	if duration, ok := numericPrintValue(info.Lookup("duration")); ok && duration >= 0 {
		info.Set("duration_string", value.String(formatPrintDuration(duration)))
	}
	return addPrintTableFields(info)
}

func (operation *operation) applyPrintSelectionFields(info *value.Info, plan *mediaformat.OutputPlan) {
	if info == nil || plan == nil || len(plan.Tracks) == 0 {
		return
	}
	*info = selectedPlanInfo(*info, *plan)
	if prefs := operation.mergeOutputPreferences(); len(prefs) > 0 && len(plan.Tracks) > 1 {
		info.Set("ext", value.String(plannedOutputExtension(*plan, prefs)))
	}
}

func addSelectedFormatFields(info *value.Info, selections []mediaformat.Selection) {
	urls := make([]string, 0, len(selections))
	ids := make([]string, 0, len(selections))
	protocols := make([]string, 0, len(selections))
	var filesize, height int64
	var tbr float64
	var vcodec, acodec, fallbackVCodec, fallbackACodec string
	for _, selection := range selections {
		if selection.URL != "" {
			urls = append(urls, selection.URL)
		}
		if selection.ID != "" {
			ids = append(ids, selection.ID)
		}
		if selection.Protocol != "" && !containsString(protocols, selection.Protocol) {
			protocols = append(protocols, selection.Protocol)
		}
		if selection.Filesize > 0 {
			filesize += selection.Filesize
		}
		if selection.Height > height {
			height = selection.Height
		}
		if selection.TBR > 0 {
			tbr += selection.TBR
		}
		if vcodec == "" && selection.VCodec != "" && selection.VCodec != "none" {
			vcodec = selection.VCodec
		}
		if fallbackVCodec == "" && selection.VCodec != "" {
			fallbackVCodec = selection.VCodec
		}
		if acodec == "" && selection.ACodec != "" && selection.ACodec != "none" {
			acodec = selection.ACodec
		}
		if fallbackACodec == "" && selection.ACodec != "" {
			fallbackACodec = selection.ACodec
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
	info.Set("ext", value.String(mergedOutputExtension(selections)))
	if filesize > 0 {
		info.Set("filesize", value.Int(filesize))
	}
	if height > 0 {
		info.Set("height", value.Int(height))
	}
	if tbr > 0 {
		info.Set("tbr", value.Float(tbr))
	}
	if vcodec == "" {
		vcodec = fallbackVCodec
	}
	if acodec == "" {
		acodec = fallbackACodec
	}
	if vcodec != "" {
		info.Set("vcodec", value.String(vcodec))
	}
	if acodec != "" {
		info.Set("acodec", value.String(acodec))
	}
	if len(protocols) > 0 {
		info.Set("protocol", value.String(strings.Join(protocols, "+")))
	}
}

// selectedPlanInfo is the plan-aware merged-metadata helper introduced in
// PR 5. The canonical merged-format dictionary is owned by the format
// planner, so when an OutputPlan is already available the product layer
// should consume plan.Metadata instead of reconstructing merged fields
// from []Selection. Use this helper wherever an OutputPlan is available.
//
// selectedPlanInfo clones the top-level video info, clears stale
// selected-format fields using the existing field list, and merges the
// planner-owned plan.Metadata into the clone. The returned Info never
// shares mutable ownership with the supplied plan or info.
func selectedPlanInfo(info value.Info, plan mediaformat.OutputPlan) value.Info {
	selected := value.NewInfo(info.Fields().Clone())
	for _, key := range selectedFormatFieldNames {
		selected.Fields().Delete(key)
	}
	if plan.Metadata.Fields().Len() == 0 {
		return selected
	}
	selected.Fields().Merge(plan.Metadata.Fields(), false)
	return selected
}

func selectedFormatInfo(info value.Info, selections []mediaformat.Selection) value.Info {
	selected := value.NewInfo(info.Fields().Clone())
	if len(selections) == 0 {
		return selected
	}
	for _, key := range selectedFormatFieldNames {
		selected.Fields().Delete(key)
	}
	formats, _ := info.Formats()
	objects := make([]*value.Object, len(selections))
	for index, selection := range selections {
		objects[index] = findSelectedFormatObject(formats, selection)
	}
	if len(selections) == 1 {
		if objects[0] != nil {
			selected.Fields().Merge(objects[0], false)
		}
		descriptiveFormat, hasDescriptiveFormat := selected.Lookup("format").StringValue()
		normalized := append([]mediaformat.Selection(nil), selections...)
		normalized[0].Protocol = selectedFormatProtocol(selected, normalized[0], objects[0])
		addSelectedFormatFields(&selected, normalized)
		if hasDescriptiveFormat {
			selected.Set("format", value.String(descriptiveFormat))
		}
		return selected
	}
	addMergedSelectedFormatFields(&selected, selections, objects)
	return selected
}

func findSelectedFormatObject(formats []value.Value, selection mediaformat.Selection) *value.Object {
	if index, ok := selection.NormalizedFormatIndex(); ok && index >= 0 && index < len(formats) {
		if object, ok := formats[index].Object(); ok && selectedFormatObjectMatches(object, selection) {
			return object
		}
	}
	for _, candidate := range formats {
		object, ok := candidate.Object()
		if ok && selectedFormatObjectMatches(object, selection) {
			return object
		}
	}
	if _, normalized := selection.NormalizedFormatIndex(); !normalized {
		if source, ok := selection.SourceFormatIndex(); ok && source >= 0 && source < len(formats) {
			if object, ok := formats[source].Object(); ok {
				return object
			}
		}
	}
	return nil
}

func selectedFormatObjectMatches(object *value.Object, selection mediaformat.Selection) bool {
	id, _ := object.Lookup("format_id").StringValue()
	if id != selection.ID {
		return false
	}
	rawURL, _ := object.Lookup("url").StringValue()
	return selection.URL == "" || rawURL == selection.URL
}

func addMergedSelectedFormatFields(
	info *value.Info,
	selections []mediaformat.Selection,
	objects []*value.Object,
) {
	ids, formats, protocols := make([]string, 0, len(selections)), make([]string, 0, len(selections)), make([]string, 0, len(selections))
	languages, notes := make([]string, 0, len(selections)), make([]string, 0, len(selections))
	urls := make([]string, 0, len(selections))
	var filesizeApprox, tbr float64
	var videoIndex, audioIndex = -1, -1
	videoCount, audioCount := 0, 0
	for index, selection := range selections {
		if selection.ID != "" {
			ids = append(ids, selection.ID)
		}
		if selection.URL != "" {
			urls = append(urls, selection.URL)
		}
		if protocol := selectedFormatProtocol(*info, selection, objects[index]); protocol != "" {
			protocols = append(protocols, protocol)
		}
		if selection.VCodec != "" && selection.VCodec != "none" {
			videoCount++
			videoIndex = index
		}
		if selection.ACodec != "" && selection.ACodec != "none" {
			audioCount++
			audioIndex = index
		}
		if objects[index] == nil {
			continue
		}
		if text, ok := objects[index].Lookup("format").StringValue(); ok && text != "" {
			formats = append(formats, text)
		}
		appendUniqueObjectString(&languages, objects[index], "language")
		appendUniqueObjectString(&notes, objects[index], "format_note")
		if number, ok := firstObjectNumber(objects[index], "filesize", "filesize_approx"); ok {
			filesizeApprox += number
		}
		if number, ok := firstObjectNumber(objects[index], "tbr", "vbr", "abr"); ok {
			tbr += number
		}
	}
	if len(formats) == 0 {
		formats = append(formats, ids...)
	}
	setJoinedField(info, "format", formats)
	setJoinedField(info, "format_id", ids)
	info.Set("ext", value.String(mergedOutputExtension(selections)))
	setJoinedField(info, "protocol", protocols)
	setJoinedField(info, "language", languages)
	setJoinedField(info, "format_note", notes)
	if filesizeApprox > 0 {
		info.Set("filesize_approx", value.Float(filesizeApprox))
	}
	if tbr > 0 {
		info.Set("tbr", value.Float(tbr))
	}
	if len(urls) > 0 {
		info.Set("urls", value.String(strings.Join(urls, "\n")))
		info.Set("url", value.String(urls[0]))
	}
	if videoCount == 1 && objects[videoIndex] != nil {
		copyObjectFields(info, objects[videoIndex],
			"width", "height", "resolution", "fps", "dynamic_range",
			"vcodec", "vbr", "stretched_ratio", "aspect_ratio")
		if info.Lookup("resolution").IsMissing() || info.Lookup("resolution").IsNull() {
			if resolution := formatTableResolution(objects[videoIndex]); resolution != "" {
				info.Set("resolution", value.String(resolution))
			}
		}
	}
	if audioCount == 1 && objects[audioIndex] != nil {
		copyObjectFields(info, objects[audioIndex], "acodec", "abr", "asr", "audio_channels")
	}
}

func selectedFormatProtocol(info value.Info, selection mediaformat.Selection, object *value.Object) string {
	if selection.Protocol != "" {
		return selection.Protocol
	}
	if object != nil {
		if protocol, ok := object.Lookup("protocol").StringValue(); ok && protocol != "" {
			return protocol
		}
	}
	parsed, err := url.Parse(selection.URL)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(parsed.Scheme, "rtmp") {
		return "rtmp"
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(parsed.Path), ".")) {
	case "m3u8":
		if live, _ := info.Lookup("is_live").Bool(); live {
			return "m3u8"
		}
		return "m3u8_native"
	case "f4m":
		return "f4m"
	default:
		return parsed.Scheme
	}
}

func appendUniqueObjectString(result *[]string, object *value.Object, key string) {
	text, ok := object.Lookup(key).StringValue()
	if ok && text != "" && !containsString(*result, text) {
		*result = append(*result, text)
	}
}

func firstObjectNumber(object *value.Object, keys ...string) (float64, bool) {
	for _, key := range keys {
		if number, ok := numericPrintValue(object.Lookup(key)); ok {
			return number, true
		}
	}
	return 0, false
}

func setJoinedField(info *value.Info, key string, values []string) {
	if len(values) > 0 {
		info.Set(key, value.String(strings.Join(values, "+")))
	}
}

func copyObjectFields(info *value.Info, object *value.Object, keys ...string) {
	for _, key := range keys {
		candidate := object.Lookup(key)
		if !candidate.IsMissing() && !candidate.IsNull() {
			info.Set(key, candidate)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, candidate := range values {
		if candidate == want {
			return true
		}
	}
	return false
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
	return operation.renderFilename(selectedFormatInfo(info, selections), selections)
}

func (operation *operation) planDestinationOutputInfo(info value.Info, plan mediaformat.OutputPlan) value.Info {
	outputInfo := selectedPlanInfo(info, plan)
	if len(plan.Tracks) > 1 {
		ext := plannedOutputExtension(plan, operation.mergeOutputPreferences())
		if ext != "" {
			outputInfo.Set("ext", value.String(ext))
		}
	}
	operation.applyThumbnailEmbeddingOutputExtension(&outputInfo, plan.Tracks)
	return outputInfo
}

func (operation *operation) printFilenameForPlan(info value.Info, plan mediaformat.OutputPlan) (string, error) {
	outputInfo := operation.planDestinationOutputInfo(info, plan)
	destination, err := operation.renderFilenameBase(outputInfo)
	if err != nil {
		return "", err
	}
	return thumbnailEmbeddingDestination(
		operation.request, plan.Tracks, destination, outputInfo,
	), nil
}

func (operation *operation) renderFilenameBase(outputInfo value.Info) (string, error) {
	pattern := operation.request.outputTemplate(OutputTemplateDefault)
	outputDir := operation.request.outputRoot(OutputPathHome)
	filename, err := outputtemplate.Resolve(outputDir, pattern, outputInfo)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filename), nil
}

func (operation *operation) renderFilename(outputInfo value.Info, selections []mediaformat.Selection) (string, error) {
	operation.applyThumbnailEmbeddingOutputExtension(&outputInfo, selections)
	destination, err := operation.renderFilenameBase(outputInfo)
	if err != nil {
		return "", err
	}
	return thumbnailEmbeddingDestination(
		operation.request, selections, destination, outputInfo,
	), nil
}
