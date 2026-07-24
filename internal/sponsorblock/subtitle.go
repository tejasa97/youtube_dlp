package sponsorblock

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxSubtitleBytes = 4 << 20
	maxSubtitleCues  = 4096
)

var (
	srtTiming    = regexp.MustCompile(`(?i)^\s*(\d{1,2}:\d{2}:\d{2}[,.]\d{1,3})\s*-->\s*(\d{1,2}:\d{2}:\d{2}[,.]\d{1,3})(.*)$`)
	vttTiming    = regexp.MustCompile(`(?i)^\s*((?:\d{1,2}:)?\d{1,2}:\d{2}\.\d{1,3})\s*-->\s*((?:\d{1,2}:)?\d{1,2}:\d{2}\.\d{1,3})(.*)$`)
	assDialogue  = regexp.MustCompile(`(?i)^(Dialogue:\s*)(\d+)\s*,\s*([^,]+)\s*,\s*([^,]+)\s*,(.*)$`)
	lrcTimestamp = regexp.MustCompile(`\[(\d{1,3}):(\d{2})(?:\.(\d{1,3}))?\]`)
)

// RemapCueInterval maps a cue onto the post-cut timeline. Cues that collapse
// to empty after removing cut ranges are dropped.
func RemapCueInterval(start, end float64, cuts []Range) (float64, float64, bool) {
	if !finite(start) || !finite(end) || end <= start {
		return 0, 0, false
	}
	mappedStart := MapTimeThroughCuts(start, cuts)
	mappedEnd := MapTimeThroughCuts(end, cuts)
	if !finite(mappedStart) || !finite(mappedEnd) || mappedEnd <= mappedStart {
		return 0, 0, false
	}
	return mappedStart, mappedEnd, true
}

// CutSubtitle rewrites a supported subtitle sidecar so cues are removed or
// remapped through cuts. ext is the lowercase extension without a dot.
func CutSubtitle(ext string, data []byte, cuts []Range) ([]byte, error) {
	if len(data) > maxSubtitleBytes {
		return nil, errorf(ErrInvalidInput, "subtitle too large")
	}
	if !utf8.Valid(data) {
		return nil, errorf(ErrInvalidInput, "subtitle encoding")
	}
	switch strings.ToLower(ext) {
	case "srt":
		return cutSRT(data, cuts)
	case "vtt":
		return cutVTT(data, cuts)
	case "ass", "ssa":
		return cutASS(data, cuts)
	case "lrc":
		return cutLRC(data, cuts)
	default:
		return nil, errorf(ErrUnsupported, "subtitle format")
	}
}

func cutSRT(data []byte, cuts []Range) ([]byte, error) {
	blocks := splitSubtitleBlocks(string(data))
	if len(blocks) > maxSubtitleCues {
		return nil, errorf(ErrInvalidInput, "subtitle cue limit")
	}
	var out strings.Builder
	index := 1
	for _, block := range blocks {
		lines := splitKeepNonEmpty(block)
		if len(lines) == 0 {
			continue
		}
		timingLine := lines[0]
		body := lines[1:]
		if srtTiming.FindStringSubmatch(timingLine) == nil && len(lines) >= 2 {
			if _, err := strconv.Atoi(strings.TrimSpace(timingLine)); err == nil {
				timingLine = lines[1]
				body = lines[2:]
			}
		}
		match := srtTiming.FindStringSubmatch(timingLine)
		if match == nil {
			return nil, errorf(ErrInvalidInput, "srt cue")
		}
		start, err := parseSRTTimestamp(match[1])
		if err != nil {
			return nil, err
		}
		end, err := parseSRTTimestamp(match[2])
		if err != nil {
			return nil, err
		}
		mappedStart, mappedEnd, keep := RemapCueInterval(start, end, cuts)
		if !keep {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(strconv.Itoa(index))
		out.WriteByte('\n')
		out.WriteString(formatSRTTimestamp(mappedStart))
		out.WriteString(" --> ")
		out.WriteString(formatSRTTimestamp(mappedEnd))
		out.WriteString(match[3])
		out.WriteByte('\n')
		out.WriteString(strings.Join(body, "\n"))
		index++
	}
	if out.Len() == 0 {
		return []byte{}, nil
	}
	out.WriteByte('\n')
	return []byte(out.String()), nil
}

func cutVTT(data []byte, cuts []Range) ([]byte, error) {
	text := string(data)
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var header []string
	i := 0
	for i < len(lines) {
		header = append(header, lines[i])
		if strings.TrimSpace(lines[i]) == "" && i > 0 {
			i++
			break
		}
		i++
		if i > 64 {
			break
		}
	}
	if len(header) == 0 || !strings.HasPrefix(strings.TrimSpace(header[0]), "WEBVTT") {
		// Accept missing magic only when the body still has VTT timings.
		header = []string{"WEBVTT", ""}
		i = 0
	}
	var body strings.Builder
	cues := 0
	for i < len(lines) {
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= len(lines) {
			break
		}
		startLine := i
		timingIdx := i
		if vttTiming.FindStringSubmatch(lines[i]) == nil && i+1 < len(lines) && vttTiming.FindStringSubmatch(lines[i+1]) != nil {
			timingIdx = i + 1
		}
		match := vttTiming.FindStringSubmatch(lines[timingIdx])
		if match == nil {
			if isVTTMetadataBlock(lines[startLine]) {
				if body.Len() > 0 {
					body.WriteByte('\n')
				}
				for j := startLine; j < len(lines); j++ {
					if j > startLine && strings.TrimSpace(lines[j]) == "" {
						break
					}
					body.WriteString(lines[j])
					body.WriteByte('\n')
				}
				i = startLine + 1
				for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
					i++
				}
				continue
			}
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
				i++
			}
			continue
		}
		i = timingIdx + 1
		var cueBody []string
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			cueBody = append(cueBody, lines[i])
			i++
		}
		cues++
		if cues > maxSubtitleCues {
			return nil, errorf(ErrInvalidInput, "subtitle cue limit")
		}
		start, err := parseVTTTimestamp(match[1])
		if err != nil {
			return nil, err
		}
		end, err := parseVTTTimestamp(match[2])
		if err != nil {
			return nil, err
		}
		mappedStart, mappedEnd, keep := RemapCueInterval(start, end, cuts)
		if !keep {
			continue
		}
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		if timingIdx > startLine {
			body.WriteString(lines[startLine])
			body.WriteByte('\n')
		}
		body.WriteString(formatVTTTimestamp(mappedStart))
		body.WriteString(" --> ")
		body.WriteString(formatVTTTimestamp(mappedEnd))
		body.WriteString(match[3])
		body.WriteByte('\n')
		if len(cueBody) > 0 {
			body.WriteString(strings.Join(cueBody, "\n"))
			body.WriteByte('\n')
		}
	}
	var out strings.Builder
	out.WriteString(strings.Join(header, "\n"))
	if !strings.HasSuffix(out.String(), "\n") {
		out.WriteByte('\n')
	}
	out.WriteString(body.String())
	return []byte(out.String()), nil
}

func cutASS(data []byte, cuts []Range) ([]byte, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out strings.Builder
	cues := 0
	for index, line := range lines {
		match := assDialogue.FindStringSubmatch(line)
		if match == nil {
			out.WriteString(line)
			if index+1 < len(lines) {
				out.WriteByte('\n')
			}
			continue
		}
		cues++
		if cues > maxSubtitleCues {
			return nil, errorf(ErrInvalidInput, "subtitle cue limit")
		}
		start, err := parseASSTimestamp(match[3])
		if err != nil {
			return nil, err
		}
		end, err := parseASSTimestamp(match[4])
		if err != nil {
			return nil, err
		}
		mappedStart, mappedEnd, keep := RemapCueInterval(start, end, cuts)
		if !keep {
			continue
		}
		out.WriteString(match[1])
		out.WriteString(match[2])
		out.WriteByte(',')
		out.WriteString(formatASSTimestamp(mappedStart))
		out.WriteByte(',')
		out.WriteString(formatASSTimestamp(mappedEnd))
		out.WriteByte(',')
		out.WriteString(match[5])
		if index+1 < len(lines) {
			out.WriteByte('\n')
		}
	}
	return []byte(out.String()), nil
}

func cutLRC(data []byte, cuts []Range) ([]byte, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var out strings.Builder
	cues := 0
	for index, line := range lines {
		matches := lrcTimestamp.FindAllStringSubmatchIndex(line, -1)
		if len(matches) == 0 {
			out.WriteString(line)
			if index+1 < len(lines) {
				out.WriteByte('\n')
			}
			continue
		}
		cues += len(matches)
		if cues > maxSubtitleCues {
			return nil, errorf(ErrInvalidInput, "subtitle cue limit")
		}
		var rebuilt strings.Builder
		kept := 0
		textStart := 0
		for _, match := range matches {
			min := line[match[2]:match[3]]
			sec := line[match[4]:match[5]]
			frac := ""
			if match[6] >= 0 {
				frac = line[match[6]:match[7]]
			}
			textStart = match[1]
			start, err := parseLRCTimestamp(min, sec, frac)
			if err != nil {
				return nil, err
			}
			// LRC cues are points; treat as a one-centisecond mark for survival.
			mappedStart, _, keep := RemapCueInterval(start, start+0.01, cuts)
			if !keep {
				continue
			}
			rebuilt.WriteString(formatLRCTimestamp(mappedStart))
			kept++
		}
		if kept == 0 {
			continue
		}
		out.WriteString(rebuilt.String())
		out.WriteString(line[textStart:])
		if index+1 < len(lines) {
			out.WriteByte('\n')
		}
	}
	return []byte(out.String()), nil
}

func isVTTMetadataBlock(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	switch {
	case upper == "NOTE" || strings.HasPrefix(upper, "NOTE "):
		return true
	case upper == "STYLE" || strings.HasPrefix(upper, "STYLE "):
		return true
	case upper == "REGION" || strings.HasPrefix(upper, "REGION "):
		return true
	default:
		return false
	}
}

func splitSubtitleBlocks(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	parts := strings.Split(normalized, "\n\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func splitKeepNonEmpty(block string) []string {
	raw := strings.Split(block, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		out = append(out, line)
	}
	return out
}

func parseSRTTimestamp(value string) (float64, error) {
	value = strings.ReplaceAll(value, ",", ".")
	return parseHMSTimestamp(value, true)
}

func formatSRTTimestamp(seconds float64) string {
	h, m, s, ms := splitTimestamp(seconds, 3)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

func parseVTTTimestamp(value string) (float64, error) {
	return parseHMSTimestamp(value, true)
}

func formatVTTTimestamp(seconds float64) string {
	h, m, s, ms := splitTimestamp(seconds, 3)
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
	}
	return fmt.Sprintf("%02d:%02d.%03d", m, s, ms)
}

func parseASSTimestamp(value string) (float64, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0, errorf(ErrInvalidInput, "ass timestamp")
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil || hours < 0 {
		return 0, errorf(ErrInvalidInput, "ass timestamp")
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil || minutes < 0 || minutes > 59 {
		return 0, errorf(ErrInvalidInput, "ass timestamp")
	}
	secParts := strings.SplitN(parts[2], ".", 2)
	seconds, err := strconv.Atoi(secParts[0])
	if err != nil || seconds < 0 || seconds > 59 {
		return 0, errorf(ErrInvalidInput, "ass timestamp")
	}
	frac := 0.0
	if len(secParts) == 2 {
		digits := secParts[1]
		if len(digits) > 2 {
			digits = digits[:2]
		}
		for len(digits) < 2 {
			digits += "0"
		}
		centi, err := strconv.Atoi(digits)
		if err != nil {
			return 0, errorf(ErrInvalidInput, "ass timestamp")
		}
		frac = float64(centi) / 100
	}
	return float64(hours*3600+minutes*60+seconds) + frac, nil
}

func formatASSTimestamp(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	totalCenti := int(math.Round(seconds * 100))
	h := totalCenti / 360000
	totalCenti %= 360000
	m := totalCenti / 6000
	totalCenti %= 6000
	s := totalCenti / 100
	c := totalCenti % 100
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, c)
}

func parseLRCTimestamp(min, sec, frac string) (float64, error) {
	minutes, err := strconv.Atoi(min)
	if err != nil || minutes < 0 {
		return 0, errorf(ErrInvalidInput, "lrc timestamp")
	}
	seconds, err := strconv.Atoi(sec)
	if err != nil || seconds < 0 || seconds > 59 {
		return 0, errorf(ErrInvalidInput, "lrc timestamp")
	}
	fraction := 0.0
	if frac != "" {
		digits := frac
		if len(digits) > 3 {
			digits = digits[:3]
		}
		scale := math.Pow10(len(digits))
		value, err := strconv.Atoi(digits)
		if err != nil {
			return 0, errorf(ErrInvalidInput, "lrc timestamp")
		}
		fraction = float64(value) / scale
	}
	return float64(minutes*60+seconds) + fraction, nil
}

func formatLRCTimestamp(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	totalMs := int(math.Round(seconds * 1000))
	m := totalMs / 60000
	totalMs %= 60000
	s := totalMs / 1000
	ms := totalMs % 1000
	return fmt.Sprintf("[%02d:%02d.%03d]", m, s, ms)
}

func parseHMSTimestamp(value string, allowShort bool) (float64, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	var hours, minutes int
	var secPart string
	switch len(parts) {
	case 3:
		var err error
		hours, err = strconv.Atoi(parts[0])
		if err != nil || hours < 0 {
			return 0, errorf(ErrInvalidInput, "timestamp")
		}
		minutes, err = strconv.Atoi(parts[1])
		if err != nil || minutes < 0 || minutes > 59 {
			return 0, errorf(ErrInvalidInput, "timestamp")
		}
		secPart = parts[2]
	case 2:
		if !allowShort {
			return 0, errorf(ErrInvalidInput, "timestamp")
		}
		var err error
		minutes, err = strconv.Atoi(parts[0])
		if err != nil || minutes < 0 {
			return 0, errorf(ErrInvalidInput, "timestamp")
		}
		secPart = parts[1]
	default:
		return 0, errorf(ErrInvalidInput, "timestamp")
	}
	secParts := strings.SplitN(secPart, ".", 2)
	seconds, err := strconv.Atoi(secParts[0])
	if err != nil || seconds < 0 || seconds > 59 {
		return 0, errorf(ErrInvalidInput, "timestamp")
	}
	frac := 0.0
	if len(secParts) == 2 {
		digits := secParts[1]
		if len(digits) > 3 {
			digits = digits[:3]
		}
		for len(digits) < 3 {
			digits += "0"
		}
		ms, err := strconv.Atoi(digits)
		if err != nil {
			return 0, errorf(ErrInvalidInput, "timestamp")
		}
		frac = float64(ms) / 1000
	}
	return float64(hours*3600+minutes*60+seconds) + frac, nil
}

func splitTimestamp(seconds float64, fracDigits int) (h, m, s, frac int) {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		seconds = 0
	}
	scale := math.Pow10(fracDigits)
	total := int(math.Round(seconds * scale))
	den := int(scale)
	h = total / (3600 * den)
	total %= 3600 * den
	m = total / (60 * den)
	total %= 60 * den
	s = total / den
	frac = total % den
	return h, m, s, frac
}

// SupportedSubtitleExt reports whether CutSubtitle handles ext.
func SupportedSubtitleExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "srt", "vtt", "ass", "ssa", "lrc":
		return true
	default:
		return false
	}
}
