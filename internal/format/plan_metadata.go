package format

import (
	"math"
	"net/url"
	"path"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

// planMetadataFor builds the planner-owned Metadata value for one OutputPlan.
// `objects` is the post-multistream-suppression list of *value.Object, in
// retained track order. `prepared` is used only to read the cached `info`
// top-level Fields when a single-track clone needs to inherit non-format
// metadata; it is never mutated.
func planMetadataFor(objects []*value.Object, prepared Prepared) value.Info {
	target := value.NewObject()
	switch len(objects) {
	case 0:
		return value.NewInfo(target)
	case 1:
		buildSingleTrackMetadata(target, objects[0])
	default:
		buildMergedMetadata(target, objects, prepared)
	}
	return value.NewInfo(target)
}

// buildSingleTrackMetadata copies the selected canonical format into a fresh
// defensive object. No requested_formats is added: yt-dlp only emits that
// field for merged outputs.
func buildSingleTrackMetadata(target *value.Object, source *value.Object) {
	if source == nil {
		return
	}
	merged := source.Clone()
	if merged == nil {
		return
	}
	for _, field := range merged.Fields() {
		target.Set(field.Key, field.Value)
	}
}

// buildMergedMetadata constructs the merged-format dictionary documented in
// PR 5 §13. The output is owned by `target`; every value referenced inside
// it is a defensive clone of the retained track objects so that subsequent
// mutations of either side stay isolated.
func buildMergedMetadata(target *value.Object, objects []*value.Object, prepared Prepared) {
	requestedValues := make([]value.Value, 0, len(objects))
	vcodecList := make([]string, 0, len(objects))
	acodecList := make([]string, 0, len(objects))
	vextList := make([]string, 0, len(objects))
	aextList := make([]string, 0, len(objects))
	videoCount, audioCount := 0, 0
	var onlyVideo, onlyAudio *value.Object
	for _, source := range objects {
		clone := source.Clone()
		if clone == nil {
			continue
		}
		requestedValues = append(requestedValues, value.ObjectValue(clone))
		vcodec := readStringField(clone, "vcodec")
		acodec := readStringField(clone, "acodec")
		ext := readStringField(clone, "ext")
		// Mirror yt-dlp: vcodecs/vexts pair over video-bearing formats,
		// acodecs/aexts pair over audio-bearing formats. The two slice
		// pairs may have different lengths; compatibleExtension requires
		// the parallel-pair contract internally.
		if hasMediaKind(vcodec) {
			vcodecList = append(vcodecList, vcodec)
			vextList = append(vextList, ext)
			videoCount++
			onlyVideo = clone
		}
		if hasMediaKind(acodec) {
			acodecList = append(acodecList, acodec)
			aextList = append(aextList, ext)
			audioCount++
			onlyAudio = clone
		}
	}
	target.Set("requested_formats", value.List(requestedValues...))
	target.Set("format", value.String(joinNonEmptyField(requestedValues, "format")))
	target.Set("format_id", value.String(joinNonEmptyField(requestedValues, "format_id")))
	var extensionPreferences []string
	if prepared.options.PreferFreeFormats {
		extensionPreferences = []string{"webm", "mkv"}
	}
	target.Set("ext", value.String(compatibleExtension(vcodecList, acodecList, vextList, aextList, extensionPreferences)))
	target.Set("protocol", value.String(joinProtocols(requestedValues, prepared.info.Lookup("is_live"))))
	setStringOrNull(target, "language", joinUniqueField(requestedValues, "language"))
	setStringOrNull(target, "format_note", joinUniqueField(requestedValues, "format_note"))
	if filesize := sumFirstField(requestedValues, "filesize", "filesize_approx"); filesize != nil {
		target.Set("filesize_approx", *filesize)
	} else {
		target.Set("filesize_approx", value.Null())
	}
	if tbr := sumFirstField(requestedValues, "tbr", "vbr", "abr"); tbr != nil {
		target.Set("tbr", *tbr)
	} else {
		target.Set("tbr", value.Int(0))
	}
	if videoCount == 1 && onlyVideo != nil {
		copyVideoFields(target, onlyVideo)
	}
	if audioCount == 1 && onlyAudio != nil {
		copyAudioFields(target, onlyAudio)
	}
}

func setStringOrNull(target *value.Object, key, text string) {
	if text == "" {
		target.Set(key, value.Null())
		return
	}
	target.Set(key, value.String(text))
}

// copyVideoFields mirrors the yt-dlp single-video-field promotion block. The
// resolution field is derived via formatResolution when missing.
func copyVideoFields(target *value.Object, source *value.Object) {
	for _, key := range []string{
		"width", "height", "fps", "dynamic_range",
		"vcodec", "vbr", "stretched_ratio", "aspect_ratio",
	} {
		copyFieldIfPresent(target, source, key)
	}
	if text, ok := source.Lookup("resolution").StringValue(); ok && text != "" {
		target.Set("resolution", value.String(text))
	} else {
		target.Set("resolution", value.String(formatResolution(source)))
	}
}

// copyAudioFields mirrors the single-audio promotion block.
func copyAudioFields(target *value.Object, source *value.Object) {
	for _, key := range []string{"acodec", "abr", "asr", "audio_channels"} {
		copyFieldIfPresent(target, source, key)
	}
}

func copyFieldIfPresent(target *value.Object, source *value.Object, key string) {
	candidate := source.Lookup(key)
	if candidate.IsMissing() || candidate.IsNull() {
		target.Set(key, value.Null())
		return
	}
	target.Set(key, candidate)
}

// hasMediaKind reports whether the codec is a real media track. An absent
// value is treated as "playable" because the pinned yt-dlp check uses
// format.get(codec) != 'none' which evaluates True for missing codecs.
func hasMediaKind(codec string) bool {
	return codec != "none"
}

func readStringField(object *value.Object, key string) string {
	text, _ := object.Lookup(key).StringValue()
	return text
}

func joinNonEmptyField(values []value.Value, key string) string {
	parts := make([]string, 0, len(values))
	for _, item := range values {
		object, ok := item.Object()
		if !ok || object == nil {
			continue
		}
		text, ok := object.Lookup(key).StringValue()
		if !ok || text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "+")
}

// joinUniqueField joins the unique non-empty values of `key` across `values`,
// preserving first-seen order. It returns "" when no value contributes.
func joinUniqueField(values []value.Value, key string) string {
	seen := make(map[string]struct{}, len(values))
	parts := make([]string, 0, len(values))
	for _, item := range values {
		object, ok := item.Object()
		if !ok || object == nil {
			continue
		}
		text, ok := object.Lookup(key).StringValue()
		if !ok || text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		parts = append(parts, text)
	}
	return strings.Join(parts, "+")
}

// joinProtocols computes the protocol field for the merged dictionary. It
// first uses any explicit non-empty protocol on each requested format; if
// that is missing, it infers from the format URL using the existing project
// rules (rtmp, m3u8/m3u8_native, f4m, scheme). The is_live flag controls
// the m3u8 vs m3u8_native choice.
func joinProtocols(values []value.Value, isLive value.Value) string {
	live, _ := isLive.Bool()
	parts := make([]string, 0, len(values))
	for _, item := range values {
		object, ok := item.Object()
		if !ok || object == nil {
			continue
		}
		if protocol, ok := object.Lookup("protocol").StringValue(); ok && protocol != "" {
			parts = append(parts, protocol)
			continue
		}
		if rawURL, ok := object.Lookup("url").StringValue(); ok && rawURL != "" {
			parts = append(parts, inferProtocolFromURL(rawURL, live))
		}
	}
	return strings.Join(parts, "+")
}

func inferProtocolFromURL(rawURL string, isLive bool) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(parsed.Scheme, "rtmp") {
		return "rtmp"
	}
	switch strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), ".")) {
	case "m3u8":
		if isLive {
			return "m3u8"
		}
		return "m3u8_native"
	case "f4m":
		return "f4m"
	default:
		return parsed.Scheme
	}
}

// sumFirstField returns the sum of the first non-zero numeric value from
// `keys` per requested format. When no format contributes a value it returns
// nil so the caller can decide between null/omit per the oracle.
func sumFirstField(values []value.Value, keys ...string) *value.Value {
	var integerSum int64
	var floatingSum float64
	floating := false
	seen := false
	for _, item := range values {
		object, ok := item.Object()
		if !ok || object == nil {
			continue
		}
		for _, key := range keys {
			candidate := object.Lookup(key)
			if candidate.IsMissing() || candidate.IsNull() {
				continue
			}
			if integer, ok := candidate.Int(); ok {
				if !floating && (integer > 0 && integerSum > math.MaxInt64-integer || integer < 0 && integerSum < math.MinInt64-integer) {
					floating = true
					floatingSum = float64(integerSum)
				}
				if floating {
					floatingSum += float64(integer)
				} else {
					integerSum += integer
				}
				seen = true
				break
			}
			if number, ok := candidate.Float(); ok {
				if !floating {
					floating = true
					floatingSum = float64(integerSum)
				}
				floatingSum += number
				seen = true
				break
			}
		}
	}
	if !seen {
		return nil
	}
	result := value.Int(integerSum)
	if floating {
		result = value.Float(floatingSum)
	}
	return &result
}

// formatResolution mirrors pkg/ytdlp.formatTableResolution for the merged
// metadata construction. Audio-only retains "audio only"; otherwise
// width x height or height p is used.
func formatResolution(format *value.Object) string {
	if format == nil {
		return ""
	}
	vcodec := readStringField(format, "vcodec")
	acodec := readStringField(format, "acodec")
	if vcodec == "none" && acodec != "none" {
		return "audio only"
	}
	if resolution := readStringField(format, "resolution"); resolution != "" {
		return resolution
	}
	width, widthOK := numericField(format.Lookup("width"))
	height, heightOK := numericField(format.Lookup("height"))
	switch {
	case widthOK && width > 0 && heightOK && height > 0:
		return numericString(width) + "x" + numericString(height)
	case heightOK && height > 0:
		return numericString(height) + "p"
	case widthOK && width > 0:
		return numericString(width) + "x?"
	default:
		return ""
	}
}

func numericField(v value.Value) (float64, bool) {
	if integer, ok := v.Int(); ok {
		return float64(integer), true
	}
	return v.Float()
}

func numericString(value float64) string {
	if value == float64(int64(value)) {
		return intToString(int64(value))
	}
	return floatToString(value)
}

func intToString(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func floatToString(value float64) string {
	// Avoid importing strconv into this file's hot path; formatResolution
	// only needs a compact decimal representation for the merged metadata.
	var b strings.Builder
	if value < 0 {
		b.WriteByte('-')
		value = -value
	}
	whole := int64(value)
	frac := value - float64(whole)
	b.WriteString(intToString(whole))
	if frac > 0 {
		b.WriteByte('.')
		for frac > 0 {
			frac *= 10
			digit := int64(frac)
			b.WriteByte(byte('0' + digit))
			frac -= float64(digit)
			if b.Len() > 12 {
				break
			}
		}
	}
	return b.String()
}
