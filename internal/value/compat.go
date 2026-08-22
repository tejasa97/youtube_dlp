// Package value preserves the historical internal value surface through
// aliases to the public engine/value owner.
package value

import public "github.com/tejasa97/ytdlp-go/engine/value"

type (
	Kind   = public.Kind
	Value  = public.Value
	Field  = public.Field
	Object = public.Object
	Info   = public.Info
)

const (
	KindMissing = public.KindMissing
	KindNull    = public.KindNull
	KindBool    = public.KindBool
	KindInt     = public.KindInt
	KindFloat   = public.KindFloat
	KindString  = public.KindString
	KindBytes   = public.KindBytes
	KindList    = public.KindList
	KindObject  = public.KindObject
)

func Missing() Value                    { return public.Missing() }
func Null() Value                       { return public.Null() }
func Bool(value bool) Value             { return public.Bool(value) }
func Int(value int64) Value             { return public.Int(value) }
func Float(value float64) Value         { return public.Float(value) }
func String(value string) Value         { return public.String(value) }
func Bytes(value []byte) Value          { return public.Bytes(value) }
func List(values ...Value) Value        { return public.List(values...) }
func ObjectValue(value *Object) Value   { return public.ObjectValue(value) }
func NewObject(fields ...Field) *Object { return public.NewObject(fields...) }
func NewInfo(fields *Object) Info       { return public.NewInfo(fields) }
