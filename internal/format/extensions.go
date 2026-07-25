package format

// Selection extension sets mirror yt-dlp YoutubeDL._format_selection_exts at
// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 (utils._utils.MEDIA_EXTENSIONS).
var (
	selectionAudioExts = map[string]struct{}{
		"aiff": {}, "alac": {}, "flac": {}, "m4a": {}, "mka": {}, "mp3": {},
		"ogg": {}, "opus": {}, "wav": {},
	}
	selectionVideoExts = map[string]struct{}{
		"avi": {}, "flv": {}, "mkv": {}, "mov": {}, "mp4": {}, "webm": {},
		"3g2": {}, "3gp": {}, "f4v": {}, "mk3d": {}, "divx": {}, "mpg": {},
		"ogv": {}, "m4v": {}, "wmv": {},
	}
	selectionStoryboardExts = map[string]struct{}{
		"mhtml": {},
	}
)

func classifyExtensionToken(text string) (atomKind, bool) {
	switch text {
	case "all":
		return atomAll, true
	case "mergeall":
		return atomMergeAll, true
	}
	if _, ok := selectionAudioExts[text]; ok {
		return atomExtension, true
	}
	if _, ok := selectionVideoExts[text]; ok {
		return atomExtension, true
	}
	if _, ok := selectionStoryboardExts[text]; ok {
		return atomExtension, true
	}
	return 0, false
}

func extensionMediaKind(text string) (video, audio, storyboard bool) {
	if _, ok := selectionStoryboardExts[text]; ok {
		return false, false, true
	}
	if _, ok := selectionAudioExts[text]; ok {
		return false, true, false
	}
	if _, ok := selectionVideoExts[text]; ok {
		return true, false, false
	}
	return false, false, false
}
