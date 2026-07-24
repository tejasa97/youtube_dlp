package sponsorblock

import (
	"encoding/json"
	"strings"
)

// decodeSegment parses one segment and validates category/action.
// Malformed individual segments are dropped by the caller; here we
// return ok=false to signal that.
func decodeSegment(raw map[string]interface{}) (RawSegment, bool) {
	tuple, ok := raw["segment"].([]interface{})
	if !ok || len(tuple) != 2 {
		return RawSegment{}, false
	}
	start, ok := decodeFloat(tuple[0])
	if !ok {
		return RawSegment{}, false
	}
	end, ok := decodeFloat(tuple[1])
	if !ok {
		return RawSegment{}, false
	}
	category, err := decodeBoundedString(raw["category"], 64)
	if err != nil {
		return RawSegment{}, false
	}
	if category == "" {
		return RawSegment{}, false
	}
	if !IsValidCategory(category) {
		// Unknown category: drop the segment rather than
		// rejecting the whole response.
		return RawSegment{}, false
	}
	action, err := decodeBoundedString(raw["actionType"], 64)
	if err != nil {
		return RawSegment{}, false
	}
	if action == "" {
		return RawSegment{}, false
	}
	if !IsValidAction(action) {
		return RawSegment{}, false
	}
	videoDuration, _ := decodeFloat(raw["videoDuration"])
	description, err := decodeBoundedString(raw["description"], MaxStringBytes)
	if err != nil {
		return RawSegment{}, false
	}
	uuid, err := decodeBoundedString(raw["UUID"], MaxStringBytes)
	if err != nil {
		return RawSegment{}, false
	}
	return RawSegment{
		Segment:       [2]float64{start, end},
		Category:      category,
		ActionType:    action,
		VideoDuration: videoDuration,
		Description:   description,
		UUID:          uuid,
	}, true
}

// decodeBoundedString returns the value of raw when it is a JSON string
// of at most max bytes. Larger values, wrong types, or any error
// return an error wrapping ErrInvalidMetadata.
func decodeBoundedString(raw interface{}, max int) (string, error) {
	if raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", errStringType
	}
	if len(value) > max {
		return "", errStringLength
	}
	return value, nil
}

// decodeFloat returns the value of raw when it is a finite JSON number.
// Booleans and strings are rejected; null and absent values are zero.
func decodeFloat(raw interface{}) (float64, bool) {
	if raw == nil {
		return 0, true
	}
	switch value := raw.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	}
	return 0, false
}

// errStringType and errStringLength are private sentinel errors used
// only inside decodeSegment. They are never returned to callers.
var (
	errStringType   = stringError("not a string")
	errStringLength = stringError("string too long")
)

type stringError string

func (err stringError) Error() string { return string(err) }

// decodeArrayValue streams body through a standard-library decoder and
// caps the array length to the SponsorBlock envelope budget. The
// decoder is configured to reject trailing data and to surface numeric
// values without precision loss for the ranges the API can produce.
func decodeArrayValue(body []byte) ([]interface{}, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, errorf(ErrInvalidMetadata, "decode array")
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return nil, errorf(ErrInvalidMetadata, "non-array body")
	}
	values := []interface{}{}
	for decoder.More() {
		if len(values) > 64 {
			return nil, errorf(ErrInvalidMetadata, "too many groups")
		}
		value, err := decodeValue(decoder, 0)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errorf(ErrInvalidMetadata, "decode end")
	}
	if _, err := decoder.Token(); err == nil {
		return nil, errorf(ErrInvalidMetadata, "trailing tokens")
	}
	return values, nil
}
