package value

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestMissingAndNullAreDistinct(t *testing.T) {
	object := NewObject()
	object.Set("null", Null())

	if got := object.Lookup("missing"); !got.IsMissing() {
		t.Fatalf("missing field kind = %s", got.Kind())
	}
	if got := object.Lookup("null"); !got.IsNull() {
		t.Fatalf("null field kind = %s", got.Kind())
	}
}

func TestObjectOrderAndUpdate(t *testing.T) {
	object := NewObject()
	object.Set("title", String("first"))
	object.Set("id", String("123"))
	object.Set("title", String("updated"))

	fields := object.Fields()
	if got := []string{fields[0].Key, fields[1].Key}; !reflect.DeepEqual(got, []string{"title", "id"}) {
		t.Fatalf("field order = %v", got)
	}
	if got, _ := object.Lookup("title").StringValue(); got != "updated" {
		t.Fatalf("title = %q", got)
	}
}

func TestObjectDeleteRepairsIndex(t *testing.T) {
	object := NewObject(Field{"a", Int(1)}, Field{"b", Int(2)}, Field{"c", Int(3)})
	if !object.Delete("b") {
		t.Fatal("Delete() = false")
	}
	if got, _ := object.Lookup("c").Int(); got != 3 {
		t.Fatalf("c = %d", got)
	}
	object.Set("d", Int(4))
	if got := object.Len(); got != 3 {
		t.Fatalf("Len() = %d", got)
	}
}

func TestDeterministicJSONAndMissingOmission(t *testing.T) {
	object := NewObject()
	object.Set("title", String("café"))
	object.Set("missing", Missing())
	object.Set("none", Null())
	object.Set("formats", List(ObjectValue(NewObject(Field{"url", String("https://media.invalid/a")}))))

	encoded, err := json.Marshal(ObjectValue(object))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"title":"café","none":null,"formats":[{"url":"https://media.invalid/a"}]}`
	if got := string(encoded); got != want {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func TestJSONRoundTripPreservesOrderAndKinds(t *testing.T) {
	input := `{"id":"x","count":3,"ratio":1.5,"active":true,"none":null,"items":[1,"two"]}`
	var value Value
	if err := json.Unmarshal([]byte(input), &value); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if got := string(encoded); got != input {
		t.Fatalf("round trip = %s, want %s", got, input)
	}
}

func TestJSONRejectsIntegerOverflow(t *testing.T) {
	var value Value
	if err := json.Unmarshal([]byte(`9223372036854775808`), &value); err == nil {
		fatalKind(t, value, "integer overflow was accepted")
	}
}

func TestJSONRejectsNonFiniteFloat(t *testing.T) {
	if _, err := json.Marshal(Float(math.Inf(1))); err == nil {
		t.Fatal("Marshal() accepted infinity")
	}
}

func TestMergeRulesAndClone(t *testing.T) {
	base := NewObject(Field{"title", String("old")}, Field{"id", String("x")})
	incoming := NewObject(Field{"title", String("new")}, Field{"skip", Missing()}, Field{"ext", String("mp4")})

	base.Merge(incoming, false)
	if got, _ := base.Lookup("title").StringValue(); got != "old" {
		t.Fatalf("non-overwrite title = %q", got)
	}
	if !base.Lookup("skip").IsMissing() {
		t.Fatal("missing merge value was inserted")
	}
	base.Merge(incoming, true)
	if got, _ := base.Lookup("title").StringValue(); got != "new" {
		t.Fatalf("overwrite title = %q", got)
	}
	if got, _ := base.Lookup("ext").StringValue(); got != "mp4" {
		t.Fatalf("ext = %q", got)
	}
}

func TestInfoAccessorsPreserveUnknownFields(t *testing.T) {
	info := NewInfo(NewObject(Field{"id", String("fixture")}, Field{"custom", Int(42)}))
	if got, ok := info.ID(); !ok || got != "fixture" {
		t.Fatalf("ID() = %q, %v", got, ok)
	}
	if got, ok := info.Lookup("custom").Int(); !ok || got != 42 {
		t.Fatalf("custom = %d, %v", got, ok)
	}
	var zero Info
	zero.Set("title", String("zero value works"))
	if got, ok := zero.Title(); !ok || got != "zero value works" {
		t.Fatalf("zero-value Info title = %q, %v", got, ok)
	}
}

func FuzzValueJSON(f *testing.F) {
	f.Add([]byte(`{"id":"fixture","formats":[{"url":"https://example.invalid"}]}`))
	f.Add([]byte(`[null,true,1,1.5,"x"]`))
	f.Add([]byte(`{"duplicate":1,"duplicate":2}`))

	f.Fuzz(func(t *testing.T, input []byte) {
		var value Value
		err := json.Unmarshal(input, &value)
		if err != nil {
			return
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("valid decoded value did not encode: %v", err)
		}
		if !json.Valid(encoded) {
			t.Fatalf("encoded invalid JSON: %s", encoded)
		}
	})
}

func fatalKind(t *testing.T, value Value, message string) {
	t.Helper()
	t.Fatalf("%s; kind = %s", message, value.Kind())
}

func TestUnknownKindString(t *testing.T) {
	if got := Kind(255).String(); !strings.Contains(got, "255") {
		t.Fatalf("Kind.String() = %q", got)
	}
}
