package extractor

// Format-fidelity normalization for single YouTube videos. These helpers
// mirror the pinned reference's format assembly in
// `_extract_formats_and_subtitles` and `parse_codecs`: bounded, pure, and
// fail-closed on malformed service input.

import (
	"net/url"
	"strconv"
	"strings"
)

// youtubeQualityLadder ranks YouTube quality strings in the pinned reference's
// order (lowest first). Unknown qualities have no rank.
var youtubeQualityLadder = []string{
	"tiny",
	"audio_quality_ultralow", "audio_quality_low", "audio_quality_medium", "audio_quality_high",
	"small", "medium", "large", "hd720", "hd1080", "hd1440", "hd2160", "hd2880", "highres",
}

var youtubeVideoCodecFamilies = map[string]bool{
	"avc1": true, "avc2": true, "avc3": true, "avc4": true,
	"vp9": true, "vp8": true, "hev1": true, "hev2": true,
	"h263": true, "h264": true, "mp4v": true, "hvc1": true,
	"av1": true, "theora": true, "dvh1": true, "dvhe": true,
}

var youtubeAudioCodecFamilies = map[string]bool{
	"flac": true, "mp4a": true, "opus": true, "vorbis": true,
	"mp3": true, "aac": true, "ac-4": true, "ac-3": true,
	"ec-3": true, "eac3": true, "dtsc": true, "dtse": true,
	"dtsh": true, "dtsl": true,
}

// stripZeroRuns mirrors the pinned `re.sub(r'0+(?=\d)', ”, codec)`: maximal
// runs of '0' followed by a digit are removed anywhere in the codec string
// (so 'av01' becomes 'av1', while an isolated '.0.' segment survives).
func stripZeroRuns(segment string) string {
	var out strings.Builder
	for index := 0; index < len(segment); {
		if segment[index] == '0' {
			end := index
			for end < len(segment) && segment[end] == '0' {
				end++
			}
			if end < len(segment) && segment[end] >= '0' && segment[end] <= '9' {
				index = end
				continue
			}
		}
		out.WriteByte(segment[index])
		index++
	}
	return out.String()
}

// youtubeParseCodecs mirrors the pinned `parse_codecs`: known video and audio
// families are classified (first video and first audio codec win), 'none' is
// emitted for the missing kind, dynamic range is derived from the codec
// profile, and exactly two unrecognized codecs fall back to raw positional
// vcodec/acodec. No recognized codec yields empty strings.
func youtubeParseCodecs(codecs string) (vcodec, acodec, dynamicRange string) {
	codecs = strings.TrimSpace(codecs)
	if codecs == "" {
		return "", "", ""
	}
	split := make([]string, 0, 2)
	for _, codec := range strings.Split(codecs, ",") {
		if codec = strings.TrimSpace(codec); codec != "" {
			split = append(split, codec)
		}
	}
	if len(split) == 0 {
		return "", "", ""
	}
	var video, audio string
	for _, fullCodec := range split {
		segments := strings.Split(stripZeroRuns(fullCodec), ".")
		family := strings.ToLower(segments[0])
		switch {
		case youtubeVideoCodecFamilies[family]:
			if video != "" {
				continue
			}
			video = fullCodec
			switch {
			case family == "dvh1" || family == "dvhe":
				dynamicRange = "DV"
			case family == "av1" && len(segments) > 3 && segments[3] == "10":
				dynamicRange = "HDR10"
			case family == "vp9" && len(segments) > 1 && segments[1] == "2":
				dynamicRange = "HDR10"
			}
		case youtubeAudioCodecFamilies[family]:
			if audio == "" {
				audio = fullCodec
			}
		}
	}
	if video != "" || audio != "" {
		vcodec, acodec = "none", "none"
		if video != "" {
			vcodec = video
		}
		if audio != "" {
			acodec = audio
		}
		return vcodec, acodec, dynamicRange
	}
	if len(split) == 2 {
		return split[0], split[1], ""
	}
	return "", "", ""
}

// youtubeFormatQuality derives the quality string the pinned reference
// normalizes: the format quality, falling back to the lowercased audioQuality,
// with the 3gp format (17) forced to "tiny".
func youtubeFormatQuality(quality, audioQuality, itag string) string {
	if quality == "" || quality == "tiny" {
		quality = strings.ToLower(audioQuality)
	}
	if itag == "17" {
		return "tiny"
	}
	return quality
}

// youtubeQualityRank returns the pinned quality-ladder rank for a quality
// string, or ok=false when unknown.
func youtubeQualityRank(quality string) (int64, bool) {
	for index, candidate := range youtubeQualityLadder {
		if candidate == quality {
			return int64(index), true
		}
	}
	return 0, false
}

// youtubeFormatName is the pinned display name: the quality label, or the
// quality string with the audio_quality_ prefix removed.
func youtubeFormatName(qualityLabel, quality string) string {
	if qualityLabel != "" {
		return qualityLabel
	}
	return strings.TrimPrefix(quality, "audio_quality_")
}

// youtubeSuperResolution reports whether the format URL carries the
// xtags=sr=1 super-resolution marker, mirroring the pinned is_super_resolution
// helper (the xtags value is itself a query string).
func youtubeSuperResolution(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	xtags := parsed.Query().Get("xtags")
	if xtags == "" || len(xtags) > 1024 {
		return false
	}
	values, err := url.ParseQuery(xtags)
	if err != nil {
		return false
	}
	return values.Get("sr") == "1"
}

// youtubeAudioLanguage derives the format language and preference from the
// audio track identity, mirroring get_language_code_and_preference:
// descriptive tracks get a -desc suffix and -10, original tracks 10, default
// tracks 5, and everything else -1. ok=false when no track identity exists.
func youtubeAudioLanguage(audioTrack *youtubeAudioTrack) (language string, preference int64, ok bool) {
	if audioTrack == nil || audioTrack.ID == "" {
		return "", 0, false
	}
	language = strings.Split(audioTrack.ID, ".")[0]
	displayName := strings.ToLower(audioTrack.DisplayName)
	switch {
	case strings.Contains(displayName, "descriptive"):
		return joinYouTubeFormatLanguage(language, "desc"), -10, true
	case strings.Contains(displayName, "original"):
		return language, 10, true
	case audioTrack.AudioIsDefault:
		return language, 5, true
	default:
		return language, -1, true
	}
}

func joinYouTubeFormatLanguage(language, suffix string) string {
	if language == "" {
		return suffix
	}
	return language + "-" + suffix
}

// youtubeFormatNote assembles the pinned comma-separated format note from the
// audio display name, quality name, DRC/AI-upscaled markers, projection and
// spatial audio types, and the damaged marker.
func youtubeFormatNote(audioTrack *youtubeAudioTrack, name string, isDRC, superResolution, damaged bool, projectionType, spatialAudioType string) string {
	var parts []string
	if audioTrack != nil && audioTrack.DisplayName != "" {
		parts = append(parts, audioTrack.DisplayName)
		if audioTrack.AudioIsDefault {
			parts = append(parts, "(default)")
		}
	}
	if name != "" {
		parts = append(parts, name)
	}
	if isDRC {
		parts = append(parts, "DRC")
	}
	if superResolution {
		parts = append(parts, "AI-upscaled")
	}
	if projection := strings.ReplaceAll(strings.ToLower(projectionType), "rectangular", ""); projection != "" {
		parts = append(parts, projection)
	}
	if spatial := strings.ReplaceAll(strings.ToLower(spatialAudioType), "spatial_audio_type_", ""); spatial != "" {
		parts = append(parts, spatial)
	}
	if damaged {
		parts = append(parts, "DAMAGED")
	}
	return strings.Join(parts, ", ")
}

// youtubeDamagedFormat mirrors the pinned is_damaged heuristic: a format whose
// approximate duration is less than half the video duration is deprioritized.
func youtubeDamagedFormat(approxDurationMS int64, duration int64, hasDuration bool) bool {
	if !hasDuration || duration <= 0 || approxDurationMS <= 0 {
		return false
	}
	return float64(approxDurationMS)/1000 < float64(duration)/2
}

// youtubeFilesizeApprox mirrors the pinned filesize_from_tbr helper:
// duration seconds * tbr kbps * 1000/8 bits-to-bytes.
func youtubeFilesizeApprox(tbr float64, approxDurationMS int64) (int64, bool) {
	if tbr <= 0 || approxDurationMS <= 0 {
		return 0, false
	}
	duration := float64(approxDurationMS) / 1000
	return int64(duration * tbr * (1000.0 / 8.0)), true
}

// youtubeFormatItag returns the itag as a decimal string, rejecting
// unreasonable values rather than interpolating them.
func youtubeFormatItag(itag int) (string, bool) {
	if itag <= 0 || itag > 1<<20 {
		return "", false
	}
	return strconv.Itoa(itag), true
}
