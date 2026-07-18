package config

import (
	"reflect"
	"testing"
)

func TestExpandAliasesDynamicRepeatedAndPreset(t *testing.T) {
	origin := Token{Source: "aliases.conf", Line: 3, Column: 1}
	tokens := stringTokens([]string{
		"--alias", "audio,-X", "-f {0} -x", "--audio", "best audio",
		"-X", "second", "--preset-alias", "mkv",
	}, origin)
	got, err := ExpandAliases(tokens, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "best audio", "-x", "-f", "second", "-x", "--merge-output-format", "mkv", "--remux-video", "mkv"}
	if !reflect.DeepEqual(values(got), want) {
		t.Fatalf("got %#v, want %#v", values(got), want)
	}
}

func TestExpandAliasesRecursionAndMalformedDefinitions(t *testing.T) {
	origin := Token{Source: "aliases.conf", Line: 1, Column: 1}
	limits := DefaultLimits()
	limits.MaxAliasTriggers = 3
	_, err := ExpandAliases(stringTokens([]string{"--alias", "loop", "--loop", "--loop"}, origin), limits)
	if !IsCategory(err, ErrorRecursion) {
		t.Fatalf("expected recursion error, got %v", err)
	}
	_, err = ExpandAliases(stringTokens([]string{"--alias", "bad", "--x {name}"}, origin), DefaultLimits())
	if !IsCategory(err, ErrorAlias) {
		t.Fatalf("expected alias error, got %v", err)
	}
}

func TestExpandAliasesBoundsCommandLineTokens(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxTokenBytes = 3
	_, err := ExpandAliases([]Token{{Value: "four", Source: "<command-line>", Line: 1, Column: 1}}, limits)
	if !IsCategory(err, ErrorResource) {
		t.Fatalf("expected resource error, got %v", err)
	}
}

func TestNoConfigLocationsResetsEarlierLocations(t *testing.T) {
	origin := Token{Source: "config", Line: 1, Column: 1}
	controls, err := scanControls(stringTokens([]string{
		"--config-locations", "one", "--config-locations=two", "--no-config-locations", "--config-location", "three",
	}, origin), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(controls.locations, []string{"three"}) {
		t.Fatalf("got %#v", controls.locations)
	}
}
