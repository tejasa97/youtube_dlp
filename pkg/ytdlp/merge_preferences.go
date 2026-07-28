package ytdlp

import "strings"

// mergeOutputPreferences returns the slash-separated merge-output-format
// preference list. When PreferFreeFormats is set and no explicit preference
// exists, webm/mkv is used to mirror yt-dlp defaults.
func mergeOutputPreferences(explicit string, preferFree bool) []string {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		parts := strings.Split(explicit, "/")
		preferences := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.ToLower(strings.TrimSpace(part))
			if part != "" {
				preferences = append(preferences, part)
			}
		}
		if len(preferences) > 0 {
			return preferences
		}
	}
	if preferFree {
		return []string{"webm", "mkv"}
	}
	return nil
}
