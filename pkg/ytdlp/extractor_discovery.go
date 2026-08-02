package ytdlp

// ExtractorMetadata is the bounded, deterministic display metadata for one
// built-in extractor. It contains no URL patterns, runtime state, or network
// derived data and is safe to use for offline discovery.
type ExtractorMetadata struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description"`
	URLs        []string `json:"urls,omitempty"`
}

// BuiltInExtractorMetadata returns native extractors in stable display order.
// It does not discover plugins and does not perform network I/O. The separate
// BuiltInExtractorIDs function remains the routing-priority view used by
// telemetry and other machine-facing callers.
func BuiltInExtractorMetadata() []ExtractorMetadata {
	return builtInExtractorMetadata(nil)
}

// BuiltInExtractorMetadataForURLs returns the same stable catalog while
// assigning deduplicated URLs to every suitable display entry. Matching
// invokes only native Suitable methods; it performs no extraction or network
// I/O. It exists for the reference-compatible --list-extractors output.
func BuiltInExtractorMetadataForURLs(urls []string) []ExtractorMetadata {
	return builtInExtractorMetadata(urls)
}

func builtInExtractorMetadata(urls []string) []ExtractorMetadata {
	entries := productRegistry().MetadataForURLs(urls)
	result := make([]ExtractorMetadata, 0, len(entries))
	for _, entry := range entries {
		result = append(result, ExtractorMetadata{
			Name:        entry.Name,
			Aliases:     append([]string(nil), entry.Aliases...),
			Description: entry.Description,
			URLs:        append([]string(nil), entry.URLs...),
		})
	}
	return result
}
