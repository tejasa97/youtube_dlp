package ytdlp

import "strings"

// mergeOutputFormatPreferences parses Request.MergeOutputFormat into an ordered
// preference list. PreferFreeFormats is owned by the planner and is not parsed
// here.
func mergeOutputFormatPreferences(explicit string) []string {
	explicit = strings.TrimSpace(explicit)
	if explicit == "" {
		return nil
	}
	parts := strings.Split(explicit, "/")
	preferences := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			preferences = append(preferences, part)
		}
	}
	if len(preferences) == 0 {
		return nil
	}
	return preferences
}
