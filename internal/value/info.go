package value

// Info is the normalized metadata envelope. It provides typed accessors for
// common fields without discarding extractor-specific fields.
type Info struct {
	fields *Object
}

func NewInfo(fields *Object) Info {
	if fields == nil {
		fields = NewObject()
	}
	return Info{fields: fields}
}

// Fields returns the underlying ordered metadata object.
func (info Info) Fields() *Object {
	if info.fields == nil {
		return NewObject()
	}
	return info.fields
}

func (info Info) Lookup(key string) Value { return info.Fields().Lookup(key) }

func (info *Info) Set(key string, value Value) {
	if info.fields == nil {
		info.fields = NewObject()
	}
	info.fields.Set(key, value)
}

func (info Info) stringField(key string) (string, bool) {
	return info.Lookup(key).StringValue()
}

func (info Info) ID() (string, bool)         { return info.stringField("id") }
func (info Info) Title() (string, bool)      { return info.stringField("title") }
func (info Info) Extension() (string, bool)  { return info.stringField("ext") }
func (info Info) WebpageURL() (string, bool) { return info.stringField("webpage_url") }

func (info Info) Formats() ([]Value, bool) {
	return info.Lookup("formats").ListValue()
}
