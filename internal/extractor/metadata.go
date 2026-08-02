package extractor

import (
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxExtractorDescriptionBytes = 256

// Metadata is the deterministic, non-routing description of one extractor.
// It is intentionally separate from Extractor so registry routing remains
// compatible with small third-party and test implementations.
type Metadata struct {
	Name        string
	Aliases     []string
	Description string
	URLs        []string
}

// MetadataProvider is an optional extractor contract for bounded discovery
// metadata. It must not perform I/O or inspect a request.
type MetadataProvider interface {
	ExtractorMetadata() Metadata
}

// Metadata returns a stable display catalog without calling Suitable or
// Extract. The registry's slice remains the routing-priority authority; this
// view is sorted for human-facing discovery and keeps generic last, matching
// the pinned reference's list order.
func (registry *Registry) Metadata() []Metadata {
	if registry == nil {
		return nil
	}
	metadata := make([]Metadata, 0, len(registry.extractors))
	seen := make(map[string]struct{}, len(registry.extractors))
	for _, candidate := range registry.extractors {
		if candidate == nil {
			continue
		}
		name := normalizeMetadataToken(candidate.Name())
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		entry := Metadata{Name: name}
		if provider, ok := candidate.(MetadataProvider); ok {
			provided := provider.ExtractorMetadata()
			entry.Aliases = normalizeMetadataAliases(provided.Aliases, name)
			entry.Description = boundExtractorDescription(provided.Description)
		}
		metadata = append(metadata, entry)
	}
	sort.SliceStable(metadata, func(left, right int) bool {
		leftGeneric := metadata[left].Name == "generic"
		rightGeneric := metadata[right].Name == "generic"
		if leftGeneric != rightGeneric {
			return !leftGeneric
		}
		leftKey, rightKey := strings.ToLower(metadata[left].Name), strings.ToLower(metadata[right].Name)
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return metadata[left].Name < metadata[right].Name
	})
	return metadata
}

// MetadataForURLs assigns deduplicated input URLs to every suitable display
// entry without invoking extraction. Display order is intentionally used here
// because it matches the pinned list-extractors behavior; routing selection
// remains owned by Registry.Select and its registration order. Generic gets
// only URLs that no concrete display entry matched.
func (registry *Registry) MetadataForURLs(urls []string) []Metadata {
	metadata := registry.Metadata()
	if len(metadata) == 0 || len(urls) == 0 {
		return metadata
	}

	uniqueURLs := make([]string, 0, len(urls))
	seenURLs := make(map[string]struct{}, len(urls))
	for _, rawURL := range urls {
		if _, exists := seenURLs[rawURL]; exists {
			continue
		}
		seenURLs[rawURL] = struct{}{}
		uniqueURLs = append(uniqueURLs, rawURL)
	}

	byName := make(map[string]Extractor, len(registry.extractors))
	for _, candidate := range registry.extractors {
		if candidate == nil {
			continue
		}
		name := normalizeMetadataToken(candidate.Name())
		key := strings.ToLower(name)
		if _, exists := byName[key]; !exists {
			byName[key] = candidate
		}
	}
	matchedAny := make(map[string]bool, len(uniqueURLs))
	for index := range metadata {
		if metadata[index].Name == "generic" {
			continue
		}
		candidate := byName[strings.ToLower(metadata[index].Name)]
		if candidate == nil {
			continue
		}
		for _, rawURL := range uniqueURLs {
			parsed, err := url.Parse(rawURL)
			if err != nil || !candidate.Suitable(parsed) {
				continue
			}
			metadata[index].URLs = append(metadata[index].URLs, rawURL)
			matchedAny[rawURL] = true
		}
	}
	for index := range metadata {
		if metadata[index].Name != "generic" {
			continue
		}
		for _, rawURL := range uniqueURLs {
			if !matchedAny[rawURL] {
				metadata[index].URLs = append(metadata[index].URLs, rawURL)
			}
		}
		break
	}
	return metadata
}

func normalizeMetadataAliases(aliases []string, name string) []string {
	seen := map[string]struct{}{strings.ToLower(name): {}}
	result := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = normalizeMetadataToken(alias)
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, alias)
	}
	sort.SliceStable(result, func(left, right int) bool {
		leftKey, rightKey := strings.ToLower(result[left]), strings.ToLower(result[right])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return result[left] < result[right]
	})
	return result
}

func normalizeMetadataToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return ""
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return ""
		}
	}
	return value
}

func boundExtractorDescription(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) {
		return ""
	}
	var builder strings.Builder
	lastWasSpace := false
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			if !lastWasSpace {
				builder.WriteByte(' ')
				lastWasSpace = true
			}
			continue
		}
		builder.WriteRune(character)
		lastWasSpace = false
	}
	return truncateUTF8(strings.TrimSpace(builder.String()), maxExtractorDescriptionBytes)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	value = value[:limit-3]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "..."
}
