package ytdlp

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxPrintTableRows  = 2048
	maxPrintTableCells = 32
	maxPrintTableBytes = 256 << 10
)

func addPrintTableFields(info *value.Info) error {
	formats, err := renderFormatsTable(*info)
	if err != nil {
		return err
	}
	if formats != "" {
		info.Set("formats_table", value.String(formats))
	}
	thumbnails, err := renderThumbnailsTable(info.Lookup("thumbnails"))
	if err != nil {
		return err
	}
	if thumbnails != "" {
		info.Set("thumbnails_table", value.String(thumbnails))
	}
	subtitles, err := renderSubtitlesTable(info.Lookup("subtitles"))
	if err != nil {
		return err
	}
	if subtitles != "" {
		info.Set("subtitles_table", value.String(subtitles))
	}
	automatic, err := renderSubtitlesTable(info.Lookup("automatic_captions"))
	if err != nil {
		return err
	}
	if automatic != "" {
		info.Set("automatic_captions_table", value.String(automatic))
	}
	return nil
}

func renderFormatsTable(info value.Info) (string, error) {
	formats, ok := info.Formats()
	if !ok {
		if _, hasURL := info.Lookup("url").StringValue(); !hasURL {
			return "", nil
		}
		if mediaType, exists := info.Lookup("_type").StringValue(); exists && mediaType != "video" {
			return "", nil
		}
		formats = []value.Value{value.ObjectValue(info.Fields())}
	}
	if len(formats) > maxPrintTableRows {
		return "", printTableLimit("formats table has too many rows")
	}
	rows := make([][]string, 0, len(formats))
	for _, item := range formats {
		format, ok := item.Object()
		if !ok {
			continue
		}
		if preference, exists := tableNumber(format.Lookup("preference")); exists && preference < -1000 {
			continue
		}
		rows = append(rows, formatTableRow(format, info))
	}
	if len(rows) == 0 {
		return "", nil
	}
	headers := []string{
		"ID", "EXT", "RESOLUTION", "\tFPS", "HDR", "CH", "|", "\tFILESIZE", "\tTBR", "PROTO",
		"|", "VCODEC", "\tVBR", "ACODEC", "\tABR", "\tASR", "MORE INFO",
	}
	return renderPrintTable(headers, rows, true, "-", 0)
}

func formatTableRow(format *value.Object, info value.Info) []string {
	extension := tableString(format.Lookup("ext"))
	fileSize := ""
	if size, ok := tableNumber(format.Lookup("filesize")); ok && size >= 0 {
		fileSize = " \t" + formatTableBytes(size)
	} else if size, ok := tableNumber(format.Lookup("filesize_approx")); ok && size >= 0 {
		fileSize = "≈\t" + formatTableBytes(size)
	} else if bitrate, bitrateOK := tableNumber(format.Lookup("tbr")); bitrateOK {
		if duration, durationOK := tableNumber(info.Lookup("duration")); durationOK {
			fileSize = "~\t" + formatTableBytes(duration*bitrate*125)
		}
	}
	return []string{
		tableStringOrNumber(format.Lookup("format_id")),
		extension,
		formatTableResolution(format),
		tableRoundedField(format.Lookup("fps"), "\t", ""),
		strings.ReplaceAll(tableIgnoredString(format.Lookup("dynamic_range"), "SDR"), "HDR", ""),
		tablePrefixedString(format.Lookup("audio_channels"), "\t"),
		"|",
		fileSize,
		tableRoundedField(format.Lookup("tbr"), "\t", "k"),
		shortPrintProtocol(tableString(format.Lookup("protocol"))),
		"|",
		simplifiedPrintCodec(format, "vcodec"),
		tableRoundedField(format.Lookup("vbr"), "\t", "k"),
		simplifiedPrintCodec(format, "acodec"),
		tableRoundedField(format.Lookup("abr"), "\t", "k"),
		tableSampleRate(format.Lookup("asr")),
		formatTableMoreInfo(format, extension),
	}
}

func formatTableResolution(format *value.Object) string {
	vcodec := tableString(format.Lookup("vcodec"))
	acodec := tableString(format.Lookup("acodec"))
	if vcodec == "none" && acodec != "none" {
		return "audio only"
	}
	if resolution := tableStringOrNumber(format.Lookup("resolution")); resolution != "" {
		return resolution
	}
	width, hasWidth := tableNumber(format.Lookup("width"))
	height, hasHeight := tableNumber(format.Lookup("height"))
	switch {
	case hasWidth && width > 0 && hasHeight && height > 0:
		return fmt.Sprintf("%sx%s", tableNumberString(width), tableNumberString(height))
	case hasHeight && height > 0:
		return tableNumberString(height) + "p"
	case hasWidth && width > 0:
		return tableNumberString(width) + "x?"
	default:
		return ""
	}
}

func simplifiedPrintCodec(format *value.Object, field string) string {
	codec := tableString(format.Lookup(field))
	if codec == "" {
		return "unknown"
	}
	if codec != "none" {
		parts := strings.Split(codec, ".")
		if len(parts) > 4 {
			parts = parts[:4]
		}
		return strings.Join(parts, ".")
	}
	if field == "vcodec" && tableString(format.Lookup("acodec")) == "none" {
		return "images"
	}
	if field == "acodec" && tableString(format.Lookup("vcodec")) == "none" {
		return ""
	}
	if field == "vcodec" {
		return "audio only"
	}
	return "video only"
}

func formatTableMoreInfo(format *value.Object, extension string) string {
	notes := make([]string, 0, 6)
	if language := tableString(format.Lookup("language")); language != "" {
		notes = append(notes, "["+language+"]")
	}
	extras := make([]string, 0, 5)
	if extension == "f4f" || extension == "f4m" {
		extras = append(extras, "UNSUPPORTED")
	}
	if drm := format.Lookup("has_drm"); tableString(drm) == "maybe" {
		extras = append(extras, "Maybe DRM")
	} else if tableTruthy(drm) {
		extras = append(extras, "DRM")
	}
	if tableTruthy(format.Lookup("__needs_testing")) {
		extras = append(extras, "Untested")
	}
	if note := tableString(format.Lookup("format_note")); note != "" {
		extras = append(extras, note)
	}
	if container := tableString(format.Lookup("container")); container != "" && container != extension {
		extras = append(extras, container)
	}
	if len(extras) > 0 {
		notes = append(notes, strings.Join(extras, ", "))
	}
	return strings.Join(notes, " ")
}

func renderThumbnailsTable(input value.Value) (string, error) {
	thumbnails, ok := input.ListValue()
	if !ok || len(thumbnails) == 0 {
		return "", nil
	}
	if len(thumbnails) > maxPrintTableRows {
		return "", printTableLimit("thumbnails table has too many rows")
	}
	rows := make([][]string, 0, len(thumbnails))
	for _, item := range thumbnails {
		thumbnail, ok := item.Object()
		if !ok {
			continue
		}
		width := tableStringOrNumber(thumbnail.Lookup("width"))
		if width == "" || width == "0" {
			width = "unknown"
		}
		height := tableStringOrNumber(thumbnail.Lookup("height"))
		if height == "" || height == "0" {
			height = "unknown"
		}
		rows = append(rows, []string{
			tableStringOrNumber(thumbnail.Lookup("id")), width, height, tableString(thumbnail.Lookup("url")),
		})
	}
	if len(rows) == 0 {
		return "", nil
	}
	return renderPrintTable([]string{"ID", "Width", "Height", "URL"}, rows, false, "", 0)
}

func renderSubtitlesTable(input value.Value) (string, error) {
	subtitles, ok := input.Object()
	if !ok || subtitles.Len() == 0 {
		return "", nil
	}
	if subtitles.Len() > maxPrintTableRows {
		return "", printTableLimit("subtitles table has too many rows")
	}
	rows := make([][]string, 0, subtitles.Len())
	totalFormats := 0
	for _, language := range subtitles.Fields() {
		formats, ok := language.Value.ListValue()
		if !ok || len(formats) == 0 {
			continue
		}
		totalFormats += len(formats)
		if totalFormats > maxPrintTableRows {
			return "", printTableLimit("subtitle format list has too many entries")
		}
		extensions := make([]string, 0, len(formats))
		names := make([]string, 0, len(formats))
		for index := len(formats) - 1; index >= 0; index-- {
			format, ok := formats[index].Object()
			if !ok {
				continue
			}
			extensions = append(extensions, tableString(format.Lookup("ext")))
			name := tableString(format.Lookup("name"))
			if name == "" {
				name = "unknown"
			}
			names = append(names, name)
		}
		if len(extensions) == 0 {
			continue
		}
		nameText := strings.Join(names, ", ")
		if allTableStringsEqual(names) {
			if names[0] == "unknown" {
				nameText = ""
			} else {
				nameText = names[0]
			}
		}
		rows = append(rows, []string{language.Key, nameText, strings.Join(extensions, ", ")})
	}
	if len(rows) == 0 {
		return "", nil
	}
	return renderPrintTable([]string{"Language", "Name", "Formats"}, rows, true, "", 0)
}

func renderPrintTable(headers []string, rows [][]string, hideEmpty bool, delimiter string, extraGap int) (string, error) {
	if len(headers) == 0 || len(headers) > maxPrintTableCells || len(rows) > maxPrintTableRows {
		return "", printTableLimit("table dimensions exceed limit")
	}
	for _, row := range rows {
		if len(row) != len(headers) {
			return "", errors.New("invalid synthetic print table row")
		}
	}
	inputBytes := 0
	for _, row := range append([][]string{headers}, rows...) {
		for _, text := range row {
			if len(text) > maxPrintTableBytes-inputBytes {
				return "", printTableLimit("table input exceeds size limit")
			}
			inputBytes += len(text)
		}
	}
	keep := make([]bool, len(headers))
	for index := range keep {
		keep[index] = true
		if hideEmpty {
			keep[index] = false
			for _, row := range rows {
				if tableTextWidth(row[index]) > 0 {
					keep[index] = true
					break
				}
			}
		}
	}
	headers = filterPrintTableRow(headers, keep)
	filtered := make([][]string, len(rows))
	for index, row := range rows {
		filtered[index] = filterPrintTableRow(row, keep)
	}
	if len(headers) == 0 {
		return "", nil
	}
	table := make([][]string, 0, len(filtered)+2)
	table = append(table, headers)
	widths := printTableWidths(append([][]string{headers}, filtered...))
	gap := extraGap + 1
	if delimiter != "" {
		separators := make([]string, len(widths))
		for index, width := range widths {
			separators[index] = strings.Repeat(delimiter, width+gap)
		}
		separators[len(separators)-1] = strings.TrimSuffix(separators[len(separators)-1], strings.Repeat(delimiter, gap))
		table = append(table, separators)
	}
	table = append(table, filtered...)
	var output strings.Builder
	for rowIndex, row := range table {
		var line strings.Builder
		for column, text := range row {
			padding := widths[column] - tableTextWidth(text)
			if strings.Contains(text, "\t") {
				text = strings.ReplaceAll(text, "\t", strings.Repeat(" ", padding))
				text += strings.Repeat(" ", gap)
			} else {
				text += strings.Repeat(" ", padding+gap)
			}
			if line.Len()+len(text) > maxPrintTableBytes {
				return "", printTableLimit("table output exceeds size limit")
			}
			line.WriteString(text)
		}
		rendered := strings.TrimRight(line.String(), " ")
		lineBreakBytes := 0
		if rowIndex+1 < len(table) {
			lineBreakBytes = 1
		}
		if output.Len()+len(rendered)+lineBreakBytes > maxPrintTableBytes {
			return "", printTableLimit("table output exceeds size limit")
		}
		output.WriteString(rendered)
		if rowIndex+1 < len(table) {
			output.WriteByte('\n')
		}
	}
	return output.String(), nil
}

func filterPrintTableRow(row []string, keep []bool) []string {
	filtered := make([]string, 0, len(row))
	for index, text := range row {
		if keep[index] {
			filtered = append(filtered, text)
		}
	}
	return filtered
}

func printTableWidths(table [][]string) []int {
	widths := make([]int, len(table[0]))
	for _, row := range table {
		for column, text := range row {
			widths[column] = max(widths[column], tableTextWidth(text))
		}
	}
	return widths
}

func tableTextWidth(text string) int {
	return utf8.RuneCountInString(strings.ReplaceAll(text, "\t", ""))
}

func printTableLimit(message string) error {
	return fmt.Errorf("%w: %s", outputtemplate.ErrInvalidTemplate, message)
}

func tableString(input value.Value) string {
	text, _ := input.StringValue()
	return text
}

func tableStringOrNumber(input value.Value) string {
	if text, ok := input.StringValue(); ok {
		return text
	}
	if number, ok := tableNumber(input); ok {
		return tableNumberString(number)
	}
	return ""
}

func tableNumber(input value.Value) (float64, bool) {
	if integer, ok := input.Int(); ok {
		return float64(integer), true
	}
	number, ok := input.Float()
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func tableTruthy(input value.Value) bool {
	switch input.Kind() {
	case value.KindBool:
		result, _ := input.Bool()
		return result
	case value.KindInt, value.KindFloat:
		number, ok := tableNumber(input)
		return ok && number != 0
	case value.KindString:
		text, _ := input.StringValue()
		return text != ""
	case value.KindList:
		items, _ := input.ListValue()
		return len(items) != 0
	case value.KindObject:
		object, _ := input.Object()
		return object.Len() != 0
	default:
		return false
	}
}

func tableNumberString(input float64) string {
	return strconv.FormatFloat(input, 'f', -1, 64)
}

func tableRoundedField(input value.Value, prefix, suffix string) string {
	number, ok := tableNumber(input)
	if !ok {
		return ""
	}
	return prefix + strconv.FormatInt(int64(math.RoundToEven(number)), 10) + suffix
}

func tablePrefixedString(input value.Value, prefix string) string {
	text := tableStringOrNumber(input)
	if text == "" {
		return ""
	}
	return prefix + text
}

func tableIgnoredString(input value.Value, ignored string) string {
	text := tableString(input)
	if text == ignored {
		return ""
	}
	return text
}

func tableSampleRate(input value.Value) string {
	number, ok := tableNumber(input)
	if !ok || number < 0 {
		return ""
	}
	suffixes := []string{"", "k", "M", "G", "T", "P", "E", "Z", "Y"}
	exponent := 0
	for exponent+1 < len(suffixes) && number >= 1000 {
		number /= 1000
		exponent++
	}
	return "\t" + strconv.FormatInt(int64(number), 10) + suffixes[exponent]
}

func formatTableBytes(input float64) string {
	if input < 0 {
		return "N/A"
	}
	return formatTableDecimalSuffix(input, 1024, "%.2f%sB")
}

func formatTableDecimalSuffix(input, factor float64, pattern string) string {
	suffixes := []string{"", "k", "M", "G", "T", "P", "E", "Z", "Y"}
	exponent := 0
	for exponent+1 < len(suffixes) && input >= factor {
		input /= factor
		exponent++
	}
	suffix := suffixes[exponent]
	if factor == 1024 && suffix != "" {
		if suffix == "k" {
			suffix = "Ki"
		} else {
			suffix += "i"
		}
	}
	return fmt.Sprintf(pattern, input, suffix)
}

func shortPrintProtocol(protocol string) string {
	if shortened, ok := map[string]string{
		"m3u8_native":                  "m3u8",
		"m3u8":                         "m3u8F",
		"rtmp_ffmpeg":                  "rtmpF",
		"http_dash_segments":           "dash",
		"http_dash_segments_generator": "dashG",
		"websocket_frag":               "WSfrag",
	}[protocol]; ok {
		return shortened
	}
	return protocol
}

func allTableStringsEqual(values []string) bool {
	for _, item := range values[1:] {
		if item != values[0] {
			return false
		}
	}
	return true
}
