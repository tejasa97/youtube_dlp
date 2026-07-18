package metadata

import (
	"errors"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"testing"
)

func TestInterpretAndReplace(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("Artist - Song")}, value.Field{Key: "description", Value: value.String("http://old.example")}))
	parse, err := ParseFromField("title:%(artist)s - %(track)s")
	if err != nil {
		t.Fatal(err)
	}
	replace, err := ParseReplace(`description:old\\.example:new.example`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(&info, []Action{parse, replace})
	if err != nil {
		t.Fatal(err)
	}
	if artist, _ := info.Lookup("artist").StringValue(); artist != "Artist" {
		t.Fatalf("artist = %q", artist)
	}
	if description, _ := info.Lookup("description").StringValue(); description != "http://new.example" {
		t.Fatalf("description = %q", description)
	}
	if len(result.Changed) != 3 {
		t.Fatalf("result = %#v", result)
	}
}
func TestWarningsAndErrors(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("x")}))
	action, _ := ParseFromField("title:%(artist)s - %(track)s")
	if result, err := Apply(&info, []Action{action}); err != nil || len(result.Warnings) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, raw := range []string{"", "title", "x:("} {
		_, err := ParseFromField(raw)
		if !errors.Is(err, ErrInvalidAction) {
			t.Fatalf("ParseFromField(%q)=%v", raw, err)
		}
	}
}
func FuzzActions(f *testing.F) {
	f.Add("title:%(artist)s - %(track)s")
	f.Add("x:y:z")
	f.Fuzz(func(t *testing.T, raw string) { _, _ = ParseFromField(raw); _, _ = ParseReplace(raw) })
}
