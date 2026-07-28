package format

// PlannerCapabilities describes the runtime capabilities the planner may rely
// on when picking its default selector. It is supplied by the product layer
// (CLI/FFmpeg probing is performed outside the format package and the
// resulting flags are injected here).
type PlannerCapabilities struct {
	CanMergeFormats bool
	OutputToStdout  bool
}

// DefaultSelectorContext describes the live context that influences the
// default selector recommendation. It mirrors the
// `_default_format_spec` inputs in yt-dlp's YoutubeDL.
type DefaultSelectorContext struct {
	IsLive           bool
	LiveFromStart    bool
	LegacyFormatSpec bool
}

// DefaultSelectorSpec returns the selector string the planner will evaluate
// for the default format request. It is pure: no FFmpeg probing, no stdout
// detection, no IO. Warning policy intentionally lives outside this function
// and is handled by the product layer that produced PlannerCapabilities.
//
// preferBest follows yt-dlp's pinned rule:
//
//	preferBest := capabilities.OutputToStdout
//	             || (context.IsLive && !context.LiveFromStart)
//
//	preferBest = preferBest || !capabilities.CanMergeFormats
//
// compatibilityMode is true when allow_multiple_audio_streams is set or
// the legacy "format-spec" compat opt is on.
//
// Returned string:
//
//	preferBest            -> "best/bestvideo+bestaudio"
//	compatibilityMode      -> "bestvideo+bestaudio/best"
//	default (VOD merger)   -> "bestvideo*+bestaudio/best"
func DefaultSelectorSpec(
	capabilities PlannerCapabilities,
	context DefaultSelectorContext,
	options Options,
) string {
	preferBest := capabilities.OutputToStdout || (context.IsLive && !context.LiveFromStart)
	if !preferBest && !capabilities.CanMergeFormats {
		preferBest = true
	}
	compatibilityMode := options.AllowMultipleAudioStreams || context.LegacyFormatSpec
	switch {
	case preferBest:
		return "best/bestvideo+bestaudio"
	case compatibilityMode:
		return "bestvideo+bestaudio/best"
	default:
		return "bestvideo*+bestaudio/best"
	}
}
