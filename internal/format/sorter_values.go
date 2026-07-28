package format

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// sortScalarKind discriminates the four preference-tuple classes produced by
// the pinned Python comparator. Higher values sort later in worst-to-best
// canonical order.
type sortScalarKind uint8

const (
	sortScalarMissing sortScalarKind = iota
	sortScalarBelowLimit
	sortScalarNumeric
	sortScalarString
)

// sortFieldPreference is the typed comparison key for one canonical field on
// one format. The Python sorter encodes each preference as the tuple
//
//	(-10, 0)                                  -> missing
//	(-1, value, 0)                            -> numeric below a non-reversed limit
//	( 0, -abs(value-limit), ...)              -> numeric closest-to-limit
//	( 0, value, 0)                            -> numeric normal direction, within limit
//	( 0, -value, 0)                           -> numeric reversed or over hard limit
//	( 1, value, 0)                            -> non-numeric string
//
// The Go mirror uses sortScalarKind for the tuple class plus the typed
// primary/secondary numeric values and a string fallback. Mixed numeric and
// string values must put strings above numbers.
type sortFieldPreference struct {
	class     sortScalarKind
	primary   float64
	secondary float64
	text      string
}

func (preference sortFieldPreference) classKey() uint8 { return uint8(preference.class) }

func missingPreference() sortFieldPreference {
	return sortFieldPreference{class: sortScalarMissing}
}

func belowLimitPreference(value float64) sortFieldPreference {
	return sortFieldPreference{class: sortScalarBelowLimit, primary: value}
}

func numericPreference(primary, secondary float64) sortFieldPreference {
	return sortFieldPreference{class: sortScalarNumeric, primary: primary, secondary: secondary}
}

func stringPreference(text string) sortFieldPreference {
	return sortFieldPreference{class: sortScalarString, text: text}
}

// compareFieldPreferences returns -1 when left sorts before right (i.e. is
// worse in canonical worst-to-best order), +1 when left sorts after right, and
// 0 when they tie. The comparator is deterministic and matches the Python
// tuple ordering including mixed numeric/string placement.
func compareFieldPreferences(left, right sortFieldPreference) int {
	if left.class != right.class {
		if left.class < right.class {
			return -1
		}
		return 1
	}
	switch left.class {
	case sortScalarMissing:
		return 0
	case sortScalarBelowLimit:
		switch {
		case left.primary < right.primary:
			return -1
		case left.primary > right.primary:
			return 1
		}
		return 0
	case sortScalarNumeric:
		if left.primary != right.primary {
			if left.primary < right.primary {
				return -1
			}
			return 1
		}
		if left.secondary != right.secondary {
			if left.secondary < right.secondary {
				return -1
			}
			return 1
		}
		return 0
	case sortScalarString:
		return strings.Compare(left.text, right.text)
	default:
		return 0
	}
}

// resolveFieldValue mirrors FormatSorter._resolve_field_value with the
// convert_none flag fixed to true. Numeric ordering returns list_length - i
// so that higher rank means better and therefore sorts later.
func resolveFieldValue(field *sorterFieldSetting, raw value.Value, useFreeOrder bool) (float64, bool) {
	if raw.IsMissing() || raw.IsNull() {
		if field.convert == "ignore" {
			return 0, false
		}
		return 0, true
	}
	if value, ok := raw.Int(); ok {
		switch field.convert {
		case "ignore":
			return 0, false
		case "string":
			return float64(value), true
		case "float_none":
			return float64(value), true
		case "bytes":
			return float64(value), true
		case "order":
			return float64(value), true
		default:
			return float64(value), true
		}
	}
	if value, ok := raw.Float(); ok {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, false
		}
		switch field.convert {
		case "ignore":
			return 0, false
		case "string":
			return value, true
		case "float_none":
			return value, true
		case "bytes":
			return value, true
		case "order":
			return value, true
		default:
			return value, true
		}
	}
	text, ok := raw.StringValue()
	if !ok {
		text = ""
	}
	text = strings.ToLower(text)
	switch field.convert {
	case "ignore":
		return 0, false
	case "string":
		return 0, true
	case "float_none":
		return floatOrNoneText(text)
	case "bytes":
		return parseBytesText(text)
	case "order":
		return orderedRank(field, text, useFreeOrder)
	default:
		return floatOrNoneText(text)
	}
}

func floatOrNoneText(text string) (float64, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

// parseBytesText implements the bounded binary-unit suffix parser used by
// yt_dlp.utils.parse_bytes. The grammar accepts an optional decimal number
// followed by one of the canonical IEC/SI suffixes. The numeric part uses the
// pinned comma-or-dot decimal separator. Empty input returns absent.
func parseBytesText(text string) (float64, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, false
	}
	unitTable := []struct {
		suffix string
		mult   float64
	}{
		{"", 1},
		{"K", 1024},
		{"M", 1024 * 1024},
		{"G", 1024 * 1024 * 1024},
		{"T", 1024 * 1024 * 1024 * 1024},
		{"P", 1024 * 1024 * 1024 * 1024 * 1024},
		{"E", 1024 * 1024 * 1024 * 1024 * 1024 * 1024},
		{"Z", 1024 * 1024 * 1024 * 1024 * 1024 * 1024 * 1024},
		{"Y", 1024 * 1024 * 1024 * 1024 * 1024 * 1024 * 1024 * 1024},
	}
	upper := strings.ToUpper(trimmed)
	for _, unit := range unitTable {
		if unit.suffix != "" && !strings.HasSuffix(upper, unit.suffix) {
			continue
		}
		body := upper
		if unit.suffix != "" {
			body = upper[:len(upper)-len(unit.suffix)]
		}
		body = strings.TrimSpace(body)
		if body == "" {
			return 0, false
		}
		normalized := strings.ReplaceAll(body, ",", ".")
		matched, err := regexp.MatchString(`^[0-9]+(?:\.[0-9]+)?$`, normalized)
		if err != nil || !matched {
			continue
		}
		parsed, err := strconv.ParseFloat(normalized, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, false
		}
		result := parsed * unit.mult
		return math.Round(result), true
	}
	return 0, false
}

// orderedRank resolves an ordered field value to the list_length-i rank. The
// empty string is the not-in-list sentinel position relative to which unknown
// values receive their rank. useFreeOrder switches vext/aext to their free
// ordering when prefer_free_formats is set.
func orderedRank(field *sorterFieldSetting, value string, useFreeOrder bool) (float64, bool) {
	order := field.order
	if useFreeOrder && len(field.orderFree) > 0 {
		order = field.orderFree
	}
	listLength := len(order)
	emptyPos := listLength
	for index, item := range order {
		if item == "" {
			emptyPos = index
			break
		}
	}
	emptyRank := float64(listLength - emptyPos)

	if value == "" {
		return emptyRank, true
	}
	if field.regex {
		for index, pattern := range order {
			if pattern == "" {
				continue
			}
			if regex, ok := pinnedOrderedRegexes[pattern]; ok && regex.MatchString(value) {
				return float64(listLength - index), true
			}
		}
		return emptyRank, true
	}
	for index, item := range order {
		if item == "" {
			continue
		}
		if item == value {
			return float64(listLength - index), true
		}
	}
	return emptyRank, true
}

// deriveFieldValue computes the effective raw value for one canonical field
// against a format object. It returns the value used by the preference tuple
// generator; canonical metadata is not mutated. The extractor and multiple
// types compose their input fields here so the preference calculator only
// deals with a single scalar at comparison time.
func deriveFieldValue(setting *sorterFieldSetting, format *value.Object) value.Value {
	switch setting.typ {
	case fieldTypeExtractor:
		if len(setting.field) == 0 {
			return value.Missing()
		}
		return format.Lookup(setting.field[0])
	case fieldTypeBoolean:
		if len(setting.field) == 0 {
			return value.Missing()
		}
		raw := format.Lookup(setting.field[0])
		text, ok := raw.StringValue()
		if !ok {
			return value.Int(-1)
		}
		if isMember(text, setting.notInList) {
			return value.Int(-1)
		}
		return value.Int(0)
	case fieldTypeOrdered:
		if len(setting.field) == 0 {
			return value.Missing()
		}
		return format.Lookup(setting.field[0])
	case fieldTypeMultiple:
		return deriveMultipleValue(setting, format)
	case fieldTypeCombined:
		return deriveCombinedValue(setting, format)
	default:
		if len(setting.field) == 0 {
			return value.Missing()
		}
		return format.Lookup(setting.field[0])
	}
}

func deriveMultipleValue(setting *sorterFieldSetting, format *value.Object) value.Value {
	fields := setting.field
	switch setting.canonical {
	case fieldRes:
		var nonZero []float64
		for _, name := range fields {
			raw := format.Lookup(name)
			value, ok := floatValue(raw)
			if !ok || value <= 0 {
				continue
			}
			nonZero = append(nonZero, value)
		}
		if len(nonZero) == 0 {
			return value.Int(0)
		}
		min := nonZero[0]
		for _, candidate := range nonZero[1:] {
			if candidate < min {
				min = candidate
			}
		}
		return value.Float(min)
	case fieldBR:
		for _, name := range fields {
			raw := format.Lookup(name)
			if raw.IsMissing() || raw.IsNull() {
				continue
			}
			text, ok := raw.StringValue()
			if ok && strings.TrimSpace(text) == "" {
				continue
			}
			if numeric, present := floatValue(raw); present {
				return value.Float(numeric)
			}
		}
		return value.Missing()
	case fieldSize:
		for _, name := range fields {
			lookup := name
			if subSetting, _, ok := lookupFieldSetting(name); ok && len(subSetting.field) > 0 {
				lookup = subSetting.field[0]
			}
			raw := format.Lookup(lookup)
			if raw.IsMissing() || raw.IsNull() {
				continue
			}
			text, ok := raw.StringValue()
			if ok && strings.TrimSpace(text) == "" {
				continue
			}
			if numeric, present := parseBytesText(text); present {
				return value.Float(numeric)
			}
			if numeric, present := floatValue(raw); present {
				return value.Float(numeric)
			}
		}
		return value.Missing()
	case fieldAudOrVid:
		for _, name := range fields {
			raw := format.Lookup(name)
			text, ok := raw.StringValue()
			if !ok {
				return value.Int(1)
			}
			if text != "none" {
				return value.Int(1)
			}
		}
		return value.Int(0)
	default:
		return value.Missing()
	}
}

func deriveCombinedValue(setting *sorterFieldSetting, format *value.Object) value.Value {
	switch setting.canonical {
	case fieldExt:
		videoRaw := format.Lookup(fieldVideoExt)
		if !videoRaw.IsMissing() && !videoRaw.IsNull() {
			if text, ok := videoRaw.StringValue(); ok && text != "" {
				return value.String(text)
			}
		}
		audioRaw := format.Lookup(fieldAudioExt)
		if !audioRaw.IsMissing() && !audioRaw.IsNull() {
			if text, ok := audioRaw.StringValue(); ok && text != "" {
				return value.String(text)
			}
		}
		return value.Missing()
	default:
		return value.Missing()
	}
}

// floatValue returns the numeric form of the value or absent. Booleans map to
// 0/1; strings use the bounded float_or_none grammar; missing/null return
// absent.
func floatValue(raw value.Value) (float64, bool) {
	if raw.IsMissing() || raw.IsNull() {
		return 0, false
	}
	if integer, ok := raw.Int(); ok {
		return float64(integer), true
	}
	if floating, ok := raw.Float(); ok {
		if math.IsNaN(floating) || math.IsInf(floating, 0) {
			return 0, false
		}
		return floating, true
	}
	if boolean, ok := raw.Bool(); ok {
		if boolean {
			return 1, true
		}
		return 0, true
	}
	if text, ok := raw.StringValue(); ok {
		return floatOrNoneText(text)
	}
	return 0, false
}

func isMember(value string, members []string) bool {
	for _, member := range members {
		if member == value {
			return true
		}
	}
	return false
}

// fillSortingFields derives the pinned observable sorting fields on a single
// canonical format object. It does not mutate the caller's extractor-owned
// metadata; canonical formats are owned by the Prepared clone.
func fillSortingFields(format *value.Object) {
	if isEmptyString(format.Lookup(fieldProtocol)) {
		format.Set(fieldProtocol, value.String(determineProtocol(format)))
	}
	if extRaw := format.Lookup("ext"); isEmptyString(extRaw) {
		if url, ok := format.Lookup("url").StringValue(); ok && url != "" {
			format.Set("ext", value.String(determineExt(url)))
		}
	}
	vcodecRaw := format.Lookup(fieldVCodec)
	acodecRaw := format.Lookup(fieldACodec)
	extRaw := format.Lookup("ext")
	vcodec, _ := vcodecRaw.StringValue()
	acodec, _ := acodecRaw.StringValue()
	ext, _ := extRaw.StringValue()
	if vcodec == "none" {
		if acodec != "none" && ext != "" {
			format.Set(fieldAudioExt, value.String(ext))
		} else {
			format.Set(fieldAudioExt, value.String("none"))
		}
		format.Set(fieldVideoExt, value.String("none"))
	} else {
		format.Set(fieldVideoExt, value.String(ext))
		format.Set(fieldAudioExt, value.String("none"))
	}

	preferenceRaw := format.Lookup(fieldPreference)
	if (preferenceRaw.IsMissing() || preferenceRaw.IsNull()) && ext == "flv" && matchesHEVC(vcodec) {
		format.Set(fieldPreference, value.Int(-100))
	}

	if vcodec == "none" {
		format.Set(fieldVBR, value.Int(0))
	}
	if acodec == "none" {
		format.Set(fieldABR, value.Int(0))
	}
	if isAbsentLikeNumber(format.Lookup(fieldVBR)) && vcodec != "none" {
		if tbr, tbrOK := floatValue(format.Lookup(fieldTBR)); tbrOK {
			if abr, abrOK := floatValue(format.Lookup(fieldABR)); abrOK {
				diff := tbr - abr
				if diff != 0 {
					format.Set(fieldVBR, value.Float(diff))
				} else {
					format.Set(fieldVBR, value.Null())
				}
			}
		}
		if isAbsentLikeNumber(format.Lookup(fieldVBR)) {
			format.Set(fieldVBR, value.Null())
		}
	}
	if isAbsentLikeNumber(format.Lookup(fieldABR)) && acodec != "none" {
		if tbr, tbrOK := floatValue(format.Lookup(fieldTBR)); tbrOK {
			if vbr, vbrOK := floatValue(format.Lookup(fieldVBR)); vbrOK {
				diff := tbr - vbr
				if diff != 0 {
					format.Set(fieldABR, value.Float(diff))
				} else {
					format.Set(fieldABR, value.Null())
				}
			}
		}
		if isAbsentLikeNumber(format.Lookup(fieldABR)) {
			format.Set(fieldABR, value.Null())
		}
	}
	if isAbsentLikeNumber(format.Lookup(fieldTBR)) {
		if vbr, vbrOK := floatValue(format.Lookup(fieldVBR)); vbrOK {
			if abr, abrOK := floatValue(format.Lookup(fieldABR)); abrOK {
				total := vbr + abr
				if total != 0 {
					format.Set(fieldTBR, value.Float(total))
				} else {
					format.Set(fieldTBR, value.Null())
				}
			}
		}
		if isAbsentLikeNumber(format.Lookup(fieldTBR)) {
			format.Set(fieldTBR, value.Null())
		}
	}
}

func isEmptyString(raw value.Value) bool {
	if raw.IsMissing() || raw.IsNull() {
		return true
	}
	text, ok := raw.StringValue()
	return ok && text == ""
}

func isZeroLikeNumber(raw value.Value) bool {
	if raw.IsMissing() || raw.IsNull() {
		return true
	}
	if text, ok := raw.StringValue(); ok {
		return strings.TrimSpace(text) == "" || text == "0" || text == "0.0"
	}
	if integer, ok := raw.Int(); ok {
		return integer == 0
	}
	if floating, ok := raw.Float(); ok {
		return floating == 0
	}
	return false
}

func isAbsentLikeNumber(raw value.Value) bool {
	return raw.IsMissing() || raw.IsNull() || isZeroLikeString(raw)
}

func isZeroLikeString(raw value.Value) bool {
	if text, ok := raw.StringValue(); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func matchesHEVC(vcodec string) bool {
	if vcodec == "" {
		return false
	}
	lower := strings.ToLower(vcodec)
	patterns := []string{`h?265`, `he?vc?`}
	for _, pattern := range patterns {
		matched, err := regexp.MatchString(pattern, lower)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// determineProtocol mirrors yt_dlp.utils.determine_protocol with the
// URL-driven fallbacks. Missing protocol values use the URL scheme; rtmp URLs
// normalize to "rtmp"; m3u8/f4m extensions map to their respective protocols.
func determineProtocol(format *value.Object) string {
	if raw, ok := format.Lookup(fieldProtocol).StringValue(); ok && raw != "" {
		return strings.ToLower(raw)
	}
	url, ok := format.Lookup("url").StringValue()
	if !ok || url == "" {
		return ""
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "rtmp") {
		return "rtmp"
	}
	ext := determineExt(url)
	if ext == "m3u8" {
		if isLive, _ := format.Lookup("is_live").Bool(); isLive {
			return "m3u8"
		}
		return "m3u8_native"
	}
	if ext == "f4m" {
		return "f4m"
	}
	if index := strings.Index(lower, "://"); index > 0 {
		return lower[:index]
	}
	return ""
}

// determineExt mirrors yt_dlp.utils.determine_ext with the unknown_video
// default extension. URLs without an extension segment or with non-alphanumeric
// extension characters fall back to the default.
func determineExt(url string) string {
	const defaultExt = "unknown_video"
	if url == "" || !strings.Contains(url, ".") {
		return defaultExt
	}
	withoutQuery := strings.SplitN(url, "?", 2)[0]
	lastDot := strings.LastIndex(withoutQuery, ".")
	if lastDot < 0 {
		return defaultExt
	}
	guess := withoutQuery[lastDot+1:]
	matched, err := regexp.MatchString(`^[A-Za-z0-9]+$`, guess)
	if err == nil && matched {
		return strings.ToLower(guess)
	}
	trimmed := strings.TrimRight(guess, "/")
	trimmed = strings.ToLower(trimmed)
	if isKnownExtension(trimmed) {
		return trimmed
	}
	return defaultExt
}

var knownExtensions = map[string]struct{}{
	"mp4": {}, "m4a": {}, "webm": {}, "opus": {}, "ogg": {}, "mp3": {}, "aac": {},
	"flv": {}, "mkv": {}, "mov": {}, "wav": {}, "ts": {}, "m3u8": {}, "f4m": {},
	"f4f": {}, "vtt": {}, "srt": {}, "ttml": {}, "json": {}, "xml": {},
}

func isKnownExtension(value string) bool {
	_, ok := knownExtensions[value]
	return ok
}
