package format

import "strings"

// compatibleExtension ports yt-dlp's get_compatible_ext helper. It is pure:
// no filesystem access, no subprocess execution, no network. Inputs describe
// the retained requested_formats; the helper joins them into a single output
// container name.
//
// The function intentionally accepts exactly four parallel slices plus an
// optional preference list. It panics on len mismatch because such a mismatch
// is an internal invariant violation, not a caller error.
//
// preferences may be nil/empty to fall back to yt-dlp's hard-coded MP4 / WebM
// pair. "mkv" in preferences (or an empty preference list) enables the
// multi-stream MKV fallback.
//
// The implementation follows utils/_utils.get_compatible_ext at
// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 exactly:
//   - allowMKV is computed from preferences,
//   - if allowMKV and either codec list has more than one entry, return "mkv",
//   - sanitize_codec drops ".0" and lowercases the first segment,
//   - COMPATIBLE_CODECS is consulted before the extension-family fallback,
//   - the second fallback iterates preferences or vexts and uses the
//     COMPATIBLE_EXTS families,
//   - the final-preference branch returns "mkv" when allowMKV, otherwise
//     preferences[-1].
//
// Empty / malformed preference slices never panic; they fall through to the
// deterministic safe result documented below.
func compatibleExtension(
	vcodecs []string,
	acodecs []string,
	vexts []string,
	aexts []string,
	preferences []string,
) string {
	if len(vcodecs) != len(vexts) || len(acodecs) != len(aexts) {
		panic("format.compatibleExtension: parallel slice length mismatch")
	}

	preferenceList := preferences
	allowMKV := len(preferenceList) == 0
	if !allowMKV {
		for _, item := range preferenceList {
			if item == "mkv" {
				allowMKV = true
				break
			}
		}
	}
	if allowMKV && (len(vcodecs) > 1 || len(acodecs) > 1) {
		return "mkv"
	}

	const mp4Set = "av1,hevc,avc1,mp4a,ac-4,h264,aacl,ec-3"
	const webmSet = "av1,vp9,vp8,opus,vrbs,vp9x,vp8x"
	compatibleCodecs := map[string]string{
		"mp4":  mp4Set,
		"webm": webmSet,
	}

	vcodec := sanitizeCodec(firstOrEmpty(vcodecs))
	acodec := sanitizeCodec(firstOrEmpty(acodecs))

	codecIteration := preferenceList
	if len(codecIteration) == 0 {
		codecIteration = []string{"mp4", "webm"}
	}
	for _, ext := range codecIteration {
		if ext == "mkv" {
			return "mkv"
		}
		setText, known := compatibleCodecs[ext]
		if !known {
			continue
		}
		set := csvToSet(setText)
		if set.contains(vcodec) && set.contains(acodec) {
			return strings.ToLower(ext)
		}
	}

	compatibleExts := []setType{
		csvToSet("mp3,mp4,m4a,m4p,m4b,m4r,m4v,ismv,isma,mov"),
		csvToSet("webm,weba"),
	}

	candidateExts := preferenceList
	if len(candidateExts) == 0 {
		candidateExts = append([]string(nil), vexts...)
	}
	for _, ext := range candidateExts {
		if ext == "mkv" {
			return "mkv"
		}
		current := csvToSet(ext + "," + strings.Join(vexts, ",") + "," + strings.Join(aexts, ","))
		if current.size() == 1 && current.contains(ext) {
			return ext
		}
		for _, family := range compatibleExts {
			if family.containsAll(current) {
				return ext
			}
		}
	}

	if allowMKV {
		return "mkv"
	}
	if len(preferenceList) > 0 {
		return preferenceList[len(preferenceList)-1]
	}
	return ""
}

// sanitizeCodec mirrors Python's sanitize_codec: split at the first ".",
// remove every "0" rune, lowercase the result. A missing codec returns "".
func sanitizeCodec(codec string) string {
	if codec == "" {
		return ""
	}
	head := codec
	if index := strings.IndexByte(codec, '.'); index >= 0 {
		head = codec[:index]
	}
	head = strings.ToLower(head)
	head = strings.ReplaceAll(head, "0", "")
	return head
}

func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// setType is a tiny ordered-string set. The Python helper uses set literals
// directly; Go does not have a built-in ordered set, so we keep one for the
// few places where membership order matters (current_exts in the second pass).
type setType struct {
	members []string
	index   map[string]int
}

func csvToSet(csv string) setType {
	parts := strings.Split(csv, ",")
	set := setType{index: make(map[string]int, len(parts))}
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if _, exists := set.index[trimmed]; exists {
			continue
		}
		set.index[trimmed] = len(set.members)
		set.members = append(set.members, trimmed)
	}
	return set
}

func (s setType) contains(needle string) bool {
	_, ok := s.index[needle]
	return ok
}

func (s setType) containsAll(other setType) bool {
	if len(other.members) == 0 {
		return true
	}
	if s.size() < other.size() {
		return false
	}
	for _, member := range other.members {
		if !s.contains(member) {
			return false
		}
	}
	return true
}

func (s setType) size() int { return len(s.members) }
