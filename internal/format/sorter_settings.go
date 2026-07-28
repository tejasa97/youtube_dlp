package format

import (
	"regexp"
	"strings"
)

// Field identifiers that the pinned yt-dlp FormatSorter recognizes as canonical
// fields or aliases. The Go sorter uses these constants to map incoming user or
// extractor sort tokens to the underlying lookup target without ever coercing
// the canonical metadata fields.
//
// The list mirrors the canonical names used in yt_dlp/utils/_utils.py:5367-5456
// and is intentionally restricted to the pinned default-order fields plus the
// alias and deprecated-alias surface.
const (
	fieldHidden       = "hidden"
	fieldAudOrVid     = "aud_or_vid"
	fieldHasVid       = "hasvid"
	fieldIEPref       = "ie_pref"
	fieldLang         = "lang"
	fieldQuality      = "quality"
	fieldRes          = "res"
	fieldFPS          = "fps"
	fieldHDR          = "hdr"
	fieldVCodec       = "vcodec"
	fieldChannels     = "channels"
	fieldACodec       = "acodec"
	fieldSize         = "size"
	fieldBR           = "br"
	fieldASR          = "asr"
	fieldProto        = "proto"
	fieldExt          = "ext"
	fieldHasAud       = "hasaud"
	fieldSource       = "source"
	fieldID           = "id"
	fieldVExt         = "vext"
	fieldAExt         = "aext"
	fieldHeight       = "height"
	fieldWidth        = "width"
	fieldTBR          = "tbr"
	fieldVBR          = "vbr"
	fieldABR          = "abr"
	fieldFilesize     = "filesize"
	fieldFSApprox     = "fs_approx"
	fieldFormatID     = "format_id"
	fieldPreference   = "preference"
	fieldLanguagePref = "language_preference"
	fieldSourcePref   = "source_preference"
	fieldProtocol     = "protocol"
	fieldFilesizeAppr = "filesize_approx"
	fieldAudioChans   = "audio_channels"
	fieldDynamicRange = "dynamic_range"
	fieldVideoExt     = "video_ext"
	fieldAudioExt     = "audio_ext"
)

// sortFieldType discriminates the conversion strategy used to derive a numeric
// preference value for one canonical field. The values match the discriminator
// keys used by FormatSorter._get_field_setting(..., 'type').
type sortFieldType uint8

const (
	fieldTypeField sortFieldType = iota
	fieldTypeExtractor
	fieldTypeBoolean
	fieldTypeOrdered
	fieldTypeCombined
	fieldTypeMultiple
)

// sorterFieldSetting captures the static metadata the pinned FormatSorter
// stores for one canonical field. The sorter extends these defaults with
// user-provided reverse/closest/limit data after parsing the raw sort tokens.
type sorterFieldSetting struct {
	canonical  string
	visible    bool
	forced     bool
	priority   bool
	typ        sortFieldType
	field      []string // canonical lookup keys; populated for combined/multiple/extractor lookups.
	notInList  []string
	convert    string // string, float, float_none, bytes, order, float_string, ignore.
	regex      bool
	order      []string
	orderFree  []string
	deprecated bool
	defaultVal float64
	hasDefault bool
	max        *float64
	maxSet     bool
}

// sorterFieldSettings is the pinned metadata table keyed by canonical field
// name. Aliases resolve through sorterAliasTable rather than this table.
var sorterFieldSettings = map[string]*sorterFieldSetting{
	fieldHidden: {
		canonical: fieldHidden, visible: false, forced: true,
		typ: fieldTypeExtractor, field: []string{fieldPreference},
		convert: "float_none", maxSet: true, max: ptrFloat(-1000),
	},
	fieldAudOrVid: {
		canonical: fieldAudOrVid, visible: false, forced: true,
		typ: fieldTypeMultiple, field: []string{fieldVCodec, fieldACodec},
	},
	fieldHasVid: {
		canonical: fieldHasVid, priority: true,
		typ: fieldTypeBoolean, field: []string{fieldVCodec},
		notInList: []string{"none"},
		convert:   "float_none",
	},
	fieldIEPref: {
		canonical: fieldIEPref, priority: true,
		typ: fieldTypeExtractor, field: []string{fieldPreference},
		convert: "float_none",
	},
	fieldLang: {
		canonical: fieldLang,
		typ:       fieldTypeField, field: []string{fieldLanguagePref},
		convert: "float", hasDefault: true, defaultVal: -1,
	},
	fieldQuality: {
		canonical: fieldQuality,
		typ:       fieldTypeField, field: []string{fieldQuality},
		convert: "float", hasDefault: true, defaultVal: -1,
	},
	fieldRes: {
		canonical: fieldRes,
		typ:       fieldTypeMultiple, field: []string{fieldHeight, fieldWidth},
	},
	fieldFPS: {
		canonical: fieldFPS,
		typ:       fieldTypeField, field: []string{fieldFPS},
		convert: "float_none",
	},
	fieldHDR: {
		canonical: fieldHDR,
		typ:       fieldTypeOrdered, regex: true, field: []string{fieldDynamicRange},
		convert: "order",
	},
	fieldVCodec: {
		canonical: fieldVCodec,
		typ:       fieldTypeOrdered, regex: true, field: []string{fieldVCodec},
		convert: "order",
	},
	fieldChannels: {
		canonical: fieldChannels,
		typ:       fieldTypeField, field: []string{fieldAudioChans},
		convert: "float_none",
	},
	fieldACodec: {
		canonical: fieldACodec,
		typ:       fieldTypeOrdered, regex: true, field: []string{fieldACodec},
		convert: "order",
	},
	fieldSize: {
		canonical: fieldSize,
		typ:       fieldTypeMultiple, field: []string{fieldFilesize, fieldFSApprox},
		convert: "bytes",
	},
	fieldBR: {
		canonical: fieldBR,
		typ:       fieldTypeMultiple, field: []string{fieldTBR, fieldVBR, fieldABR},
		convert: "float_none",
	},
	fieldASR: {
		canonical: fieldASR,
		typ:       fieldTypeField, field: []string{fieldASR},
		convert: "float_none",
	},
	fieldProto: {
		canonical: fieldProto,
		typ:       fieldTypeOrdered, regex: true, field: []string{fieldProtocol},
		convert: "order",
	},
	fieldExt: {
		canonical: fieldExt,
		typ:       fieldTypeCombined,
		field:     []string{fieldVExt, fieldAExt},
	},
	fieldHasAud: {
		canonical: fieldHasAud,
		typ:       fieldTypeBoolean, field: []string{fieldACodec},
		notInList: []string{"none"},
		convert:   "float_none",
	},
	fieldSource: {
		canonical: fieldSource,
		typ:       fieldTypeField, field: []string{fieldSourcePref},
		convert: "float", hasDefault: true, defaultVal: -1,
	},
	fieldID: {
		canonical: fieldID,
		typ:       fieldTypeField, field: []string{fieldFormatID},
		convert: "string",
	},
	fieldVExt: {
		canonical: fieldVExt,
		typ:       fieldTypeOrdered, field: []string{fieldVideoExt},
		convert: "order",
	},
	fieldAExt: {
		canonical: fieldAExt,
		typ:       fieldTypeOrdered, regex: true, field: []string{fieldAudioExt},
		convert: "order",
	},
	fieldHeight: {
		canonical: fieldHeight,
		typ:       fieldTypeField, field: []string{fieldHeight},
		convert: "float_none",
	},
	fieldWidth: {
		canonical: fieldWidth,
		typ:       fieldTypeField, field: []string{fieldWidth},
		convert: "float_none",
	},
	fieldTBR: {
		canonical: fieldTBR,
		typ:       fieldTypeField, field: []string{fieldTBR},
		convert: "float_none",
	},
	fieldVBR: {
		canonical: fieldVBR,
		typ:       fieldTypeField, field: []string{fieldVBR},
		convert: "float_none",
	},
	fieldABR: {
		canonical: fieldABR,
		typ:       fieldTypeField, field: []string{fieldABR},
		convert: "float_none",
	},
	fieldFilesize: {
		canonical: fieldFilesize,
		typ:       fieldTypeField, field: []string{fieldFilesize},
		convert: "bytes",
	},
	fieldFSApprox: {
		canonical: fieldFSApprox,
		typ:       fieldTypeField, field: []string{fieldFilesizeAppr},
		convert: "bytes",
	},
}

// sorterAliasTable maps the canonical-name aliases accepted by FormatSorter to
// their underlying field. Deprecated aliases share the same mapping but carry
// the deprecated flag for provenance only; the Go sorter does not emit
// warnings in PR 4.
type sorterAlias struct {
	target     string
	deprecated bool
}

var sorterAliasTable = map[string]sorterAlias{
	fieldFormatID:     {target: fieldID},
	fieldPreference:   {target: fieldIEPref},
	fieldLanguagePref: {target: fieldLang},
	fieldSourcePref:   {target: fieldSource},
	fieldProtocol:     {target: fieldProto},
	fieldFilesizeAppr: {target: fieldFSApprox},
	fieldAudioChans:   {target: fieldChannels},

	"dimension":            {target: fieldRes, deprecated: true},
	"resolution":           {target: fieldRes, deprecated: true},
	"extension":            {target: fieldExt, deprecated: true},
	"bitrate":              {target: fieldBR, deprecated: true},
	"total_bitrate":        {target: fieldTBR, deprecated: true},
	"video_bitrate":        {target: fieldVBR, deprecated: true},
	"audio_bitrate":        {target: fieldABR, deprecated: true},
	"framerate":            {target: fieldFPS, deprecated: true},
	"filesize_estimate":    {target: fieldSize, deprecated: true},
	"samplerate":           {target: fieldASR, deprecated: true},
	fieldVideoExt:          {target: fieldVExt, deprecated: true},
	fieldAudioExt:          {target: fieldAExt, deprecated: true},
	"video_codec":          {target: fieldVCodec, deprecated: true},
	"audio_codec":          {target: fieldACodec, deprecated: true},
	"video":                {target: fieldHasVid, deprecated: true},
	"has_video":            {target: fieldHasVid, deprecated: true},
	"audio":                {target: fieldHasAud, deprecated: true},
	"has_audio":            {target: fieldHasAud, deprecated: true},
	"extractor":            {target: fieldIEPref, deprecated: true},
	"extractor_preference": {target: fieldIEPref, deprecated: true},
}

// pinnedDefaultOrder mirrors the FormatSorter.default tuple exactly. Hidden
// and aud_or_vid are forced; hasvid and ie_pref are priority; the remaining
// fields fill in the canonical positional defaults that apply when the user
// did not provide a custom ordering.
var pinnedDefaultOrder = []string{
	fieldHidden, fieldAudOrVid, fieldHasVid, fieldIEPref, fieldLang,
	fieldQuality, fieldRes, fieldFPS, fieldHDR + ":12", fieldVCodec,
	fieldChannels, fieldACodec, fieldSize, fieldBR, fieldASR,
	fieldProto, fieldExt, fieldHasAud, fieldSource, fieldID,
}

var (
	pythonNoneOrder = "\x00python-none"

	// pinnedVideoCodecOrder is parsed once at package init. Each entry is a
	// pinned regular expression evaluated with Go RE2 semantics. Unknown codecs
	// receive the not-in-list rank derived from the empty-string position.
	pinnedVideoCodecOrder = []string{
		`av0?1`, `vp0?9\.0?2`, `vp0?9`, `[hx]265|he?vc?`, `[hx]264|avc`,
		`vp0?8`, `mp4v|h263`, `theora`, ``, pythonNoneOrder, `none`,
	}

	pinnedAudioCodecOrder = []string{
		`[af]lac`, `wav|aiff`, `opus`, `vorbis|ogg`, `aac`, `mp?4a?`,
		`mp3`, `ac-?4`, `e-?a?c-?3`, `ac-?3`, `dts`, ``, pythonNoneOrder, `none`,
	}

	pinnedHDROrder = []string{
		`dv`, `(hdr)?12`, `(hdr)?10\+`, `(hdr)?10`, `hlg`, ``, `sdr`, pythonNoneOrder,
	}

	pinnedProtocolOrder = []string{
		`(ht|f)tps`, `(ht|f)tp$`, `m3u8.*`, `.*dash`, `websocket_frag`,
		`rtmpe?`, ``, `ws|websocket`, `f4`,
	}

	pinnedVideoExtOrder = []string{"mp4", "mov", "webm", "flv", "", "none"}
	pinnedVideoExtFree  = []string{"webm", "mp4", "mov", "flv", "", "none"}

	pinnedAudioExtOrder = []string{"m4a", "aac", "mp3", "ogg", "opus", "web[am]", "", "none"}
	pinnedAudioExtFree  = []string{"ogg", "opus", "web[am]", "mp3", "m4a", "aac", "", "none"}
)

func init() {
	sorterFieldSettings[fieldVCodec].order = pinnedVideoCodecOrder
	sorterFieldSettings[fieldACodec].order = pinnedAudioCodecOrder
	sorterFieldSettings[fieldHDR].order = pinnedHDROrder
	sorterFieldSettings[fieldProto].order = pinnedProtocolOrder
	sorterFieldSettings[fieldVExt].order = pinnedVideoExtOrder
	sorterFieldSettings[fieldVExt].orderFree = pinnedVideoExtFree
	sorterFieldSettings[fieldAExt].order = pinnedAudioExtOrder
	sorterFieldSettings[fieldAExt].orderFree = pinnedAudioExtFree
}

// pinnedOrderedRegexes compiles the pinned ordered regular expressions once.
// Unknown entries receive the not-in-list rank relative to the empty-string
// position in the underlying order list.
var pinnedOrderedRegexes = map[string]*regexp.Regexp{}

func init() {
	compileOrdered := func(list []string) {
		for _, pattern := range list {
			if pattern == "" || pattern == pythonNoneOrder {
				continue
			}
			pinnedOrderedRegexes[pattern] = regexp.MustCompile("(?i)^(?:" + pattern + ")")
		}
	}
	compileOrdered(pinnedVideoCodecOrder)
	compileOrdered(pinnedAudioCodecOrder)
	compileOrdered(pinnedHDROrder)
	compileOrdered(pinnedProtocolOrder)
	compileOrdered(pinnedAudioExtOrder)
	compileOrdered(pinnedAudioExtFree)
}

// lookupFieldSetting returns the metadata for one canonical field. It accepts
// the canonical name or an alias and resolves the latter through
// sorterAliasTable. Unknown fields return a sentinel metadata structure whose
// kind is fieldTypeField; that mirrors the Python sorter falling back to a
// generic float-or-string field.
func lookupFieldSetting(name string) (*sorterFieldSetting, string, bool) {
	if setting, ok := sorterFieldSettings[name]; ok {
		return setting, name, true
	}
	if alias, ok := sorterAliasTable[name]; ok {
		if setting, found := sorterFieldSettings[alias.target]; found {
			// Copy the metadata so per-instance reverse/closest/limit overrides
			// applied during composition do not leak across aliases.
			copy := *setting
			copy.deprecated = alias.deprecated
			return &copy, alias.target, true
		}
	}
	return nil, "", false
}

// hasField reports whether the supplied token resolves to a canonical field or
// an alias of a canonical field.
func hasField(name string) bool {
	_, _, ok := lookupFieldSetting(name)
	return ok
}

// resolveAlias returns the canonical field name and the deprecated flag for
// the supplied token. Unknown tokens are returned unchanged without a
// deprecated flag.
func resolveAlias(name string) (canonical string, deprecated bool) {
	if setting, ok := sorterFieldSettings[name]; ok {
		return name, setting.deprecated
	}
	if alias, ok := sorterAliasTable[name]; ok {
		return alias.target, alias.deprecated
	}
	return name, false
}

// compareCanonicalNames orders two canonical field names using the pinned
// default order. Names not present in the default order sort after every
// pinned field.
func compareCanonicalNames(left, right string) int {
	leftIndex := indexOf(pinnedDefaultOrder, left)
	rightIndex := indexOf(pinnedDefaultOrder, right)
	switch {
	case leftIndex < 0 && rightIndex < 0:
		return strings.Compare(left, right)
	case leftIndex < 0:
		return 1
	case rightIndex < 0:
		return -1
	case leftIndex < rightIndex:
		return -1
	case leftIndex > rightIndex:
		return 1
	default:
		return 0
	}
}

func indexOf(list []string, target string) int {
	for index, item := range list {
		if item == target {
			return index
		}
	}
	return -1
}

func ptrFloat(value float64) *float64 { return &value }
