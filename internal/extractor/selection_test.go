package extractor

import (
	"context"
	"errors"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

type explicitSelectionExtractor struct{ namedExtractor }

func (*explicitSelectionExtractor) ExplicitOnly() {}

func TestRegistrySelectionRulesPreserveOrderAliasesCaseAndExclusions(t *testing.T) {
	first := namedExtractor{name: "first", suitable: true}
	second := namedExtractor{name: "Second", suitable: true}
	generic := namedExtractor{name: "generic", suitable: true}
	registry := NewRegistry(first, second, generic)
	if err := registry.ConfigureSelection([]string{"second", "FIRST", "-SECOND", "generic"}); err != nil {
		t.Fatal(err)
	}
	selected, err := registry.Select("https://example.invalid/video")
	if err != nil {
		t.Fatal(err)
	}
	if got := selected.Name(); got != "first" {
		t.Fatalf("selected = %q, want first after exclusion", got)
	}

	if err := registry.ConfigureSelection([]string{"default", "-GENERIC"}); err != nil {
		t.Fatal(err)
	}
	selected, err = registry.Select("https://example.invalid/video")
	if err != nil || selected.Name() != "first" {
		t.Fatalf("default selection = %v, %v", selected, err)
	}
}

func TestRegistrySelectionRulesPreserveGenericFallbackPrecedence(t *testing.T) {
	registry := NewRegistry(namedExtractor{name: "native", suitable: true}, NewGeneric())
	selected, err := registry.Select("https://example.invalid/video")
	if err != nil || selected.Name() != "native" {
		t.Fatalf("default registry selection = %v, %v", selected, err)
	}
	if err := registry.ConfigureSelection([]string{"default", "-native"}); err != nil {
		t.Fatal(err)
	}
	selected, err = registry.Select("https://example.invalid/video")
	if err != nil || selected.Name() != "generic" {
		t.Fatalf("generic fallback selection = %v, %v", selected, err)
	}
}

func TestRegistrySelectionRulesUnknownMalformedAndEnd(t *testing.T) {
	registry := NewRegistry(namedExtractor{name: "first", suitable: true})
	for _, rules := range [][]string{{"[malformed"}, {"-"}} {
		if err := registry.ConfigureSelection(rules); !errors.Is(err, ErrInvalidSelection) {
			t.Errorf("ConfigureSelection(%q) = %v, want ErrInvalidSelection", rules, err)
		}
	}
	if err := registry.ConfigureSelection([]string{"missing", "first"}); err != nil {
		t.Fatal(err)
	}
	if selected, err := registry.Select("https://example.invalid/video"); err != nil || selected.Name() != "first" {
		t.Fatalf("zero-match rule = %v, %v", selected, err)
	}
	if err := registry.ConfigureSelection([]string{"end"}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Select("https://example.invalid/video"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("end selection error = %v", err)
	}
}

func TestRegistrySelectionEndStopsAtItsOrderedPosition(t *testing.T) {
	first := namedExtractor{name: "first", suitable: false}
	generic := namedExtractor{name: "generic", suitable: true}
	for _, test := range []struct {
		name  string
		rules []string
		want  string
	}{
		{name: "explicit position", rules: []string{"first", "end", "generic"}},
		{name: "regex end", rules: []string{"^end$", "generic"}},
		{name: "all final position", rules: []string{"all", "generic"}, want: "generic"},
		{name: "all candidates unsuitable", rules: []string{"all", "generic"}},
		{name: "excluded end", rules: []string{"all", "-end"}, want: "generic"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry(first, generic)
			if test.name == "all candidates unsuitable" {
				registry = NewRegistry(first, namedExtractor{name: "generic", suitable: false})
			}
			if err := registry.ConfigureSelection(test.rules); err != nil {
				t.Fatal(err)
			}
			selected, err := registry.Select("https://example.invalid/video")
			if test.want == "" {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("selection crossed end sentinel: %v", err)
				}
				return
			}
			if err != nil || selected.Name() != test.want {
				t.Fatalf("selected=%v err=%v want=%q", selected, err, test.want)
			}
		})
	}
}

func TestRegistrySelectionRulesDisableAutomaticAndURLResultReentry(t *testing.T) {
	first := namedExtractor{name: "first", suitable: true}
	second := namedExtractor{name: "second", suitable: true}
	registry := NewRegistry(first, second)
	if err := registry.ConfigureSelection([]string{"first"}); err != nil {
		t.Fatal(err)
	}
	if selected, err := registry.Select("https://example.invalid/video"); err != nil || selected.Name() != "first" {
		t.Fatalf("automatic selection = %v, %v", selected, err)
	}
	if _, err := registry.SelectFor("https://example.invalid/video", "second"); !errors.Is(err, ErrSelectionDisabled) || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("disabled explicit selection error = %v", err)
	}
	if _, err := registry.SelectFor("https://example.invalid/video", "unknown"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown explicit selection error = %v", err)
	}
}

func TestRegistrySelectionRulesKeepExplicitOnlyPluginSelection(t *testing.T) {
	plugin := &explicitSelectionExtractor{namedExtractor{name: "signed.example", suitable: false}}
	registry := NewRegistry(namedExtractor{name: "native", suitable: true}, plugin)
	if err := registry.ConfigureSelection([]string{"default"}); err != nil {
		t.Fatal(err)
	}
	selected, err := registry.SelectFor("https://example.invalid/video", "SIGNED.EXAMPLE")
	if err != nil || selected != plugin {
		t.Fatalf("explicit-only selection = %v, %v", selected, err)
	}
}

func (explicitSelectionExtractor) Extract(context.Context, Request) (Extraction, error) {
	return Media(value.NewInfo(value.NewObject())), nil
}

var _ Extractor = (*explicitSelectionExtractor)(nil)
var _ ExplicitOnlyExtractor = (*explicitSelectionExtractor)(nil)
