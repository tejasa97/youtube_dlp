// Package value implements ordered, heterogeneous metadata values.
package value

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Kind identifies the concrete representation held by a Value.
type Kind uint8

const (
	KindMissing Kind = iota
	KindNull
	KindBool
	KindInt
	KindFloat
	KindString
	KindBytes
	KindList
	KindObject
)

func (kind Kind) String() string {
	switch kind {
	case KindMissing:
		return "missing"
	case KindNull:
		return "null"
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindString:
		return "string"
	case KindBytes:
		return "bytes"
	case KindList:
		return "list"
	case KindObject:
		return "object"
	default:
		return fmt.Sprintf("Kind(%d)", kind)
	}
}

// Value is a dynamic metadata value. Its zero value is Missing.
type Value struct {
	kind   Kind
	bool   bool
	int    int64
	float  float64
	string string
	bytes  []byte
	list   []Value
	object *Object
}

func Missing() Value            { return Value{kind: KindMissing} }
func Null() Value               { return Value{kind: KindNull} }
func Bool(value bool) Value     { return Value{kind: KindBool, bool: value} }
func Int(value int64) Value     { return Value{kind: KindInt, int: value} }
func Float(value float64) Value { return Value{kind: KindFloat, float: value} }
func String(value string) Value { return Value{kind: KindString, string: value} }

// Bytes copies value so later caller mutations do not change the metadata.
func Bytes(value []byte) Value {
	return Value{kind: KindBytes, bytes: append([]byte(nil), value...)}
}

// List copies the slice. Nested values retain their own immutability rules.
func List(value ...Value) Value {
	return Value{kind: KindList, list: append([]Value(nil), value...)}
}

// ObjectValue wraps an ordered object. A nil object is represented as Null.
func ObjectValue(value *Object) Value {
	if value == nil {
		return Null()
	}
	return Value{kind: KindObject, object: value}
}

func (value Value) Kind() Kind      { return value.kind }
func (value Value) IsMissing() bool { return value.kind == KindMissing }
func (value Value) IsNull() bool    { return value.kind == KindNull }

func (value Value) Bool() (bool, bool) {
	return value.bool, value.kind == KindBool
}

func (value Value) Int() (int64, bool) {
	return value.int, value.kind == KindInt
}

func (value Value) Float() (float64, bool) {
	return value.float, value.kind == KindFloat
}

func (value Value) StringValue() (string, bool) {
	return value.string, value.kind == KindString
}

// BytesValue returns a defensive copy.
func (value Value) BytesValue() ([]byte, bool) {
	if value.kind != KindBytes {
		return nil, false
	}
	return append([]byte(nil), value.bytes...), true
}

// ListValue returns a shallow copy of the list.
func (value Value) ListValue() ([]Value, bool) {
	if value.kind != KindList {
		return nil, false
	}
	return append([]Value(nil), value.list...), true
}

func (value Value) Object() (*Object, bool) {
	return value.object, value.kind == KindObject
}

// Clone recursively copies mutable storage.
func (value Value) Clone() Value {
	switch value.kind {
	case KindBytes:
		return Bytes(value.bytes)
	case KindList:
		cloned := make([]Value, len(value.list))
		for index := range value.list {
			cloned[index] = value.list[index].Clone()
		}
		return List(cloned...)
	case KindObject:
		return ObjectValue(value.object.Clone())
	default:
		return value
	}
}

// MarshalJSON emits deterministic JSON. Missing is encoded as null at the top
// level and in lists; ordered object fields whose value is Missing are omitted.
func (value Value) MarshalJSON() ([]byte, error) {
	switch value.kind {
	case KindMissing, KindNull:
		return []byte("null"), nil
	case KindBool:
		return json.Marshal(value.bool)
	case KindInt:
		return []byte(strconv.FormatInt(value.int, 10)), nil
	case KindFloat:
		if math.IsNaN(value.float) || math.IsInf(value.float, 0) {
			return nil, fmt.Errorf("cannot encode non-finite float %v", value.float)
		}
		return []byte(strconv.FormatFloat(value.float, 'g', -1, 64)), nil
	case KindString:
		return json.Marshal(value.string)
	case KindBytes:
		return json.Marshal(value.bytes)
	case KindList:
		return json.Marshal(value.list)
	case KindObject:
		return value.object.MarshalJSON()
	default:
		return nil, fmt.Errorf("cannot encode unknown value kind %d", value.kind)
	}
}

// UnmarshalJSON decodes JSON while preserving object field order. JSON numbers
// without a decimal point or exponent must fit in int64; other numbers become
// float64 values.
func (value *Value) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	decoded, err := decodeValue(decoder)
	if err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON token")
		}
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	*value = decoded
	return nil
}

func decodeValue(decoder *json.Decoder) (Value, error) {
	token, err := decoder.Token()
	if err != nil {
		return Missing(), fmt.Errorf("decode JSON token: %w", err)
	}

	switch token := token.(type) {
	case nil:
		return Null(), nil
	case bool:
		return Bool(token), nil
	case string:
		return String(token), nil
	case json.Number:
		if strings.ContainsAny(token.String(), ".eE") {
			parsed, err := strconv.ParseFloat(token.String(), 64)
			if err != nil {
				return Missing(), fmt.Errorf("decode float %q: %w", token, err)
			}
			return Float(parsed), nil
		}
		parsed, err := strconv.ParseInt(token.String(), 10, 64)
		if err != nil {
			return Missing(), fmt.Errorf("decode integer %q: %w", token, err)
		}
		return Int(parsed), nil
	case json.Delim:
		switch token {
		case '[':
			var values []Value
			for decoder.More() {
				item, err := decodeValue(decoder)
				if err != nil {
					return Missing(), err
				}
				values = append(values, item)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return Missing(), fmt.Errorf("decode list end: token %v: %w", end, err)
			}
			return List(values...), nil
		case '{':
			object := NewObject()
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return Missing(), fmt.Errorf("decode object key: %w", err)
				}
				key, ok := keyToken.(string)
				if !ok {
					return Missing(), fmt.Errorf("object key has type %T", keyToken)
				}
				item, err := decodeValue(decoder)
				if err != nil {
					return Missing(), fmt.Errorf("decode object field %q: %w", key, err)
				}
				object.Set(key, item)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
				return Missing(), fmt.Errorf("decode object end: token %v: %w", end, err)
			}
			return ObjectValue(object), nil
		default:
			return Missing(), fmt.Errorf("unexpected JSON delimiter %q", token)
		}
	default:
		return Missing(), fmt.Errorf("unexpected JSON token type %T", token)
	}
}
