package extractor

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	podcastTrackingPrefix = regexp.MustCompile(`(?i)` +
		`(?:` +
		`(?:` +
		`chtbl\.com/track|` +
		`media\.blubrry\.com|` +
		`play\.podtrac\.com|` +
		`chrt\.fm/track|` +
		`mgln\.ai/e` +
		`)(?:/[^/.]+)?|` +
		`(?:dts|www)\.podtrac\.com/(?:pts/)?redirect\.[0-9a-z]{3,4}|` +
		`flex\.acast\.com|` +
		`pd(?:cn\.co|st\.fm)/e|` +
		`[0-9]\.gum\.fm|` +
		`pscrb\.fm/rss/p` +
		`)/`)
	podcastDoubleScheme = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://([a-z][a-z0-9+.-]*://)`)
)

// cleanPodcastMediaURL implements the pinned clean_podcast_url tracking-prefix
// and double-scheme behavior, then applies this port's public-host URL policy.
func cleanPodcastMediaURL(raw string, maxBytes int) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxBytes {
		return "", false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	cleaned := podcastTrackingPrefix.ReplaceAllString(raw, "")
	cleaned = podcastDoubleScheme.ReplaceAllString(cleaned, "$1")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" || len(cleaned) > maxBytes {
		return "", false
	}
	parsed, err := url.Parse(cleaned)
	if err != nil {
		return "", false
	}
	// Fragments do not participate in an HTTP media request. Removing them
	// before strict validation preserves the existing Apple Podcasts policy.
	parsed.Fragment = ""
	parsed.RawFragment = ""
	cleaned = parsed.String()
	if len(cleaned) == 0 || len(cleaned) > maxBytes || !strictValidHostedHTTPURL(cleaned) {
		return "", false
	}
	return cleaned, true
}
