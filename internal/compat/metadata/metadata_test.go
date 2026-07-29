package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestPinnedCases(t *testing.T) {
	data, err := os.ReadFile("testdata/pinned_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Reference struct{ Commit, Python string }
		Cases     []struct {
			Name, From, To, Input, Artist, Track string
			E                                    string `json:"é"`
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Reference.Commit != "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8" || fixture.Reference.Python != "CPython 3.12.13" {
		t.Fatalf("bad provenance: %#v", fixture.Reference)
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			info := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String(test.Input)}))
			action, err := ParseFromField(test.From + ":" + test.To)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(&info, []Action{action}); err != nil {
				t.Fatal(err)
			}
			if test.Artist != "" {
				if got, _ := info.Lookup("artist").StringValue(); got != test.Artist {
					t.Fatalf("artist=%q want %q", got, test.Artist)
				}
			}
			if test.Track != "" {
				if got, _ := info.Lookup("track").StringValue(); got != test.Track {
					t.Fatalf("track=%q want %q", got, test.Track)
				}
			}
			if test.E != "" {
				if got, _ := info.Lookup("é").StringValue(); got != test.E {
					t.Fatalf("é=%q want %q", got, test.E)
				}
			}
		})
	}
}

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

func TestPinnedNormalGrammarAndOrdering(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("Café — Song")},
		value.Field{Key: "description", Value: value.String("id=12; id=34")},
	))
	parse, err := ParseFromField("title:%(artist)s — %(track)s")
	if err != nil {
		t.Fatal(err)
	}
	replace, err := ParseReplaceFields("artist", `(.+)`, `\1 (live)`)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseFromField("artist:%(album)s")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(&info, []Action{parse, replace[0], second})
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{"artist": "Café (live)", "track": "Song", "album": "Café (live)"} {
		if got, _ := info.Lookup(field).StringValue(); got != want {
			t.Fatalf("%s = %q, want %q", field, got, want)
		}
	}
	if len(result.Changed) != 4 {
		t.Fatalf("changed = %#v", result.Changed)
	}
}

func TestParseUsesFirstUnescapedDelimiterAndTemplateInput(t *testing.T) {
	action, err := ParseFromField(`%(title)s\: %(id)s:%(artist)s`)
	if err != nil {
		t.Fatal(err)
	}
	if action.From != `%(title)s: %(id)s` || action.To != "%(artist)s" {
		t.Fatalf("action = %#v", action)
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("A")}, value.Field{Key: "id", Value: value.String("9")}))
	if _, err := Apply(&info, []Action{action}); err != nil {
		t.Fatal(err)
	}
	if got, _ := info.Lookup("artist").StringValue(); got != "A: 9" {
		t.Fatalf("artist = %q", got)
	}
}

func TestReplaceFieldsPythonReplacementGrammar(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("a-12 b-34")}))
	actions, err := ParseReplaceFields("title,missing", `(?P<letter>[a-z])-(?P<num>\d+)`, `\g<num>/\g<letter>/$`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(&info, actions)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := info.Lookup("title").StringValue(); got != "12/a/$ 34/b/$" {
		t.Fatalf("title = %q", got)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "video does not have a missing" {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	for _, raw := range []string{`title:(?=x):x`, `title:x:\\q`, `title:(:x`} {
		if _, err := ParseReplace(raw); !errors.Is(err, ErrUnsupportedRegex) && !errors.Is(err, ErrInvalidAction) {
			t.Fatalf("ParseReplace(%q) = %v", raw, err)
		}
	}
}

func TestMissingNullTypeStageCancellationAndBounds(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "nil", Value: value.Null()}, value.Field{Key: "count", Value: value.Int(1)}, value.Field{Key: "title", Value: value.String("x")},
	))
	actions, err := ParseReplaceFields("nil,count,absent", "x", "y")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(&info, actions)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 3 || !strings.Contains(result.Warnings[1], "int") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	stage, err := ParseFromField("video:title:%(artist)s")
	if !errors.Is(err, ErrUnsupportedStage) || stage.Kind != 0 {
		t.Fatalf("stage action=%#v err=%v", stage, err)
	}
	parse, err := ParseFromField("title:%(artist)s")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ApplyContext(ctx, &info, []Action{parse}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	if _, err := ParseFromField(strings.Repeat("x", maxActionBytes+1)); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("bound = %v", err)
	}
}
func TestApplyRejectsConstructedInvalidAndExpansion(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "title", Value: value.String("x")}, value.Field{Key: "description", Value: value.String(strings.Repeat("x", 1024))}))
	for _, action := range []Action{{Kind: Interpret}, {Kind: Replace, Field: "description"}} {
		if _, err := Apply(&info, []Action{action}); !errors.Is(err, ErrInvalidAction) {
			t.Fatalf("Apply(%#v) = %v", action, err)
		}
	}
	action, err := ParseReplace("description:x:" + strings.Repeat("z", 1024))
	if err != nil {
		t.Fatal(err)
	}
	info.Set("description", value.String(strings.Repeat("x", 300)))
	if _, err := Apply(&info, []Action{action}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("bounded expansion error = %v", err)
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
