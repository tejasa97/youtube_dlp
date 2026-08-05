package value

import (
	"bytes"
	"encoding/json"
)

// Field is one key/value pair in an ordered Object.
type Field struct {
	Key   string
	Value Value
}

// Object preserves field insertion order while providing indexed lookup.
type Object struct {
	fields []Field
	index  map[string]int
}

func NewObject(fields ...Field) *Object {
	object := &Object{index: make(map[string]int, len(fields))}
	for _, field := range fields {
		object.Set(field.Key, field.Value)
	}
	return object
}

func (object *Object) Len() int {
	if object == nil {
		return 0
	}
	return len(object.fields)
}

// Set inserts a field at the end or updates it without changing its position.
func (object *Object) Set(key string, value Value) {
	if index, exists := object.index[key]; exists {
		object.fields[index].Value = value
		return
	}
	object.index[key] = len(object.fields)
	object.fields = append(object.fields, Field{Key: key, Value: value})
}

func (object *Object) Get(key string) (Value, bool) {
	if object == nil {
		return Missing(), false
	}
	index, exists := object.index[key]
	if !exists {
		return Missing(), false
	}
	return object.fields[index].Value, true
}

// Lookup returns Missing for an absent field.
func (object *Object) Lookup(key string) Value {
	value, _ := object.Get(key)
	return value
}

func (object *Object) Delete(key string) bool {
	if object == nil {
		return false
	}
	index, exists := object.index[key]
	if !exists {
		return false
	}
	delete(object.index, key)
	object.fields = append(object.fields[:index], object.fields[index+1:]...)
	for position := index; position < len(object.fields); position++ {
		object.index[object.fields[position].Key] = position
	}
	return true
}

// Fields returns a copy of the ordered field slice.
func (object *Object) Fields() []Field {
	if object == nil {
		return nil
	}
	return append([]Field(nil), object.fields...)
}

func (object *Object) Clone() *Object {
	if object == nil {
		return nil
	}
	cloned := NewObject()
	for _, field := range object.fields {
		cloned.Set(field.Key, field.Value.Clone())
	}
	return cloned
}

// Merge copies fields from other. Missing values are ignored. Existing fields
// are replaced only when overwrite is true, and replacement preserves order.
func (object *Object) Merge(other *Object, overwrite bool) {
	if object == nil || other == nil {
		return
	}
	for _, field := range other.fields {
		if field.Value.IsMissing() {
			continue
		}
		if _, exists := object.index[field.Key]; exists && !overwrite {
			continue
		}
		object.Set(field.Key, field.Value.Clone())
	}
}

// MarshalJSON omits fields explicitly marked Missing and preserves the order
// of all remaining fields.
func (object *Object) MarshalJSON() ([]byte, error) {
	if object == nil {
		return []byte("null"), nil
	}
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	wroteField := false
	for _, field := range object.fields {
		if field.Value.IsMissing() {
			continue
		}
		key, err := json.Marshal(field.Key)
		if err != nil {
			return nil, err
		}
		encoded, err := field.Value.MarshalJSON()
		if err != nil {
			return nil, err
		}
		if wroteField {
			buffer.WriteByte(',')
		}
		buffer.Write(key)
		buffer.WriteByte(':')
		buffer.Write(encoded)
		wroteField = true
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}
