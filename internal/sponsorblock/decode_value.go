package sponsorblock

import (
	"encoding/json"
)

// maxDecodeDepth caps the recursive JSON walk so a hostile response
// cannot exhaust the stack. SponsorBlock payloads never nest beyond a
// handful of levels.
const maxDecodeDepth = 16

// decodeValue walks one JSON value, capping recursion depth. The
// number of decoded values is bounded by the group and segment caps at
// the call site.
func decodeValue(decoder *json.Decoder, depth int) (interface{}, error) {
	if depth > maxDecodeDepth {
		return nil, errorf(ErrInvalidMetadata, "too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, errorf(ErrInvalidMetadata, "decode value")
	}
	switch value := token.(type) {
	case nil:
		return nil, nil
	case bool:
		return value, nil
	case string:
		if len(value) > MaxStringBytes {
			return nil, errorf(ErrInvalidMetadata, "string too long")
		}
		return value, nil
	case json.Number:
		if value == "" {
			return nil, errorf(ErrInvalidMetadata, "empty number")
		}
		raw := string(value)
		hasDecimal := false
		for _, r := range raw {
			if r == '.' || r == 'e' || r == 'E' {
				hasDecimal = true
				break
			}
		}
		if hasDecimal {
			parsed, err := value.Float64()
			if err != nil {
				return nil, errorf(ErrInvalidMetadata, "decode float")
			}
			return parsed, nil
		}
		parsed, err := value.Int64()
		if err != nil {
			return nil, errorf(ErrInvalidMetadata, "decode int")
		}
		return parsed, nil
	case json.Delim:
		switch value {
		case '[':
			out := []interface{}{}
			for decoder.More() {
				item, err := decodeValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, item)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, errorf(ErrInvalidMetadata, "decode end")
			}
			return out, nil
		case '{':
			out := map[string]interface{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, errorf(ErrInvalidMetadata, "decode key")
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errorf(ErrInvalidMetadata, "non-string key")
				}
				if len(key) > MaxStringBytes {
					return nil, errorf(ErrInvalidMetadata, "key too long")
				}
				item, err := decodeValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				out[key] = item
			}
			if _, err := decoder.Token(); err != nil {
				return nil, errorf(ErrInvalidMetadata, "decode end")
			}
			return out, nil
		default:
			return nil, errorf(ErrInvalidMetadata, "unexpected delimiter")
		}
	default:
		return nil, errorf(ErrInvalidMetadata, "unexpected token")
	}
}
