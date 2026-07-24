package sponsorblock

import (
	"strings"
)

// group is one decoded SponsorBlock group from the JSON response. The
// schema is intentionally minimal; the network client ignores any
// extra fields the API might add in the future.
type group struct {
	VideoID  string
	Segments []RawSegment
}

// decodeResponse parses the bounded response body. The custom decoder
// rejects hostile or oversized envelopes without depending on a
// third-party JSON library.
func decodeResponse(body []byte, _ string) ([]group, error) {
	if len(body) == 0 {
		return nil, errorf(ErrInvalidMetadata, "empty body")
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, errorf(ErrInvalidMetadata, "empty body")
	}
	if !strings.HasPrefix(trimmed, "[") {
		return nil, errorf(ErrInvalidMetadata, "non-array body")
	}
	values, err := decodeArrayValue(body)
	if err != nil {
		return nil, err
	}
	if len(values) > 64 {
		return nil, errorf(ErrInvalidMetadata, "too many groups")
	}
	decoded := make([]group, 0, len(values))
	for _, item := range values {
		entry, ok := item.(map[string]interface{})
		if !ok {
			return nil, errorf(ErrInvalidMetadata, "group not object")
		}
		groupID, err := decodeBoundedString(entry["videoID"], MaxStringBytes)
		if err != nil {
			return nil, errorf(ErrInvalidMetadata, "videoID")
		}
		if groupID == "" {
			return nil, errorf(ErrInvalidMetadata, "missing videoID")
		}
		segmentsValue, present := entry["segments"]
		if !present {
			return nil, errorf(ErrInvalidMetadata, "missing segments")
		}
		rawSegments, ok := segmentsValue.([]interface{})
		if !ok {
			return nil, errorf(ErrInvalidMetadata, "segments not array")
		}
		if len(rawSegments) > MaxSegmentCount {
			return nil, errorf(ErrInvalidMetadata, "too many segments")
		}
		segments := make([]RawSegment, 0, len(rawSegments))
		for _, rawItem := range rawSegments {
			segment, ok := rawItem.(map[string]interface{})
			if !ok {
				return nil, errorf(ErrInvalidMetadata, "segment not object")
			}
			parsed, ok := decodeSegment(segment)
			if !ok {
				// Drop a malformed segment; the
				// envelope is still well-formed.
				continue
			}
			segments = append(segments, parsed)
		}
		decoded = append(decoded, group{VideoID: groupID, Segments: segments})
	}
	return decoded, nil
}
