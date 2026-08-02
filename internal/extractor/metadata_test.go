package extractor

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

type metadataFixtureExtractor struct {
	name        string
	aliases     []string
	description string
	suitableAll bool
}

func (fixture metadataFixtureExtractor) Name() string { return fixture.name }
func (fixture metadataFixtureExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && (fixture.suitableAll || parsed.Hostname() == strings.ToLower(fixture.name)+".example")
}
func (fixture metadataFixtureExtractor) Extract(context.Context, Request) (Extraction, error) {
	return Extraction{}, ErrUnsupported
}
func (fixture metadataFixtureExtractor) ExtractorMetadata() Metadata {
	return Metadata{Aliases: fixture.aliases, Description: fixture.description}
}

func TestRegistryMetadataIsStableBoundedAndGenericLast(t *testing.T) {
	registry := NewRegistry(
		metadataFixtureExtractor{name: "Zulu", aliases: []string{"z-alias", "A-alias", "z-alias", "Zulu"}, description: strings.Repeat("é", 200)},
		NewGeneric(),
		metadataFixtureExtractor{name: "alpha"},
	)
	metadata := registry.Metadata()
	if len(metadata) != 3 {
		t.Fatalf("metadata length = %d, want 3", len(metadata))
	}
	if got := []string{metadata[0].Name, metadata[1].Name, metadata[2].Name}; strings.Join(got, ",") != "alpha,Zulu,generic" {
		t.Fatalf("metadata order = %v", got)
	}
	if metadata[2].Description != "Generic downloader that works on some sites" {
		t.Fatalf("generic description = %q", metadata[2].Description)
	}
	if len(metadata[1].Description) > maxExtractorDescriptionBytes {
		t.Fatalf("description bytes = %d, want <= %d", len(metadata[1].Description), maxExtractorDescriptionBytes)
	}
	if got, want := strings.Join(metadata[1].Aliases, ","), "A-alias,z-alias"; got != want {
		t.Fatalf("aliases = %q, want %q", got, want)
	}
	if metadata[0].Description != "" {
		t.Fatalf("default description = %q", metadata[0].Description)
	}
}

func TestRegistryMetadataDoesNotDuplicateNames(t *testing.T) {
	registry := NewRegistry(
		metadataFixtureExtractor{name: "one"},
		metadataFixtureExtractor{name: "ONE"},
		metadataFixtureExtractor{name: ""},
	)
	metadata := registry.Metadata()
	if len(metadata) != 1 || metadata[0].Name != "one" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestRegistryMetadataForURLsUsesStableDisplayOrderAndGenericRemainder(t *testing.T) {
	registry := NewRegistry(
		metadataFixtureExtractor{name: "zulu"},
		metadataFixtureExtractor{name: "alpha"},
		NewGeneric(),
	)
	metadata := registry.MetadataForURLs([]string{
		"https://zulu.example/video",
		"https://unknown.example/video",
		"https://zulu.example/video",
	})
	if got := strings.Join(metadata[0].URLs, ","); got != "" {
		t.Fatalf("alpha claimed URL = %q", got)
	}
	if got := strings.Join(metadata[1].URLs, ","); got != "https://zulu.example/video" {
		t.Fatalf("zulu URLs = %q", got)
	}
	if got := strings.Join(metadata[2].URLs, ","); got != "https://unknown.example/video" {
		t.Fatalf("generic URLs = %q", got)
	}
}

func TestRegistryMetadataForURLsRetainsOverlappingSuitableMatches(t *testing.T) {
	registry := NewRegistry(
		metadataFixtureExtractor{name: "zulu", suitableAll: true},
		metadataFixtureExtractor{name: "alpha", suitableAll: true},
		NewGeneric(),
	)
	metadata := registry.MetadataForURLs([]string{
		"https://overlap.example/one",
		"https://overlap.example/two",
		"https://overlap.example/one",
	})
	want := "https://overlap.example/one,https://overlap.example/two"
	if got := strings.Join(metadata[0].URLs, ","); got != want {
		t.Fatalf("alpha URLs = %q, want %q", got, want)
	}
	if got := strings.Join(metadata[1].URLs, ","); got != want {
		t.Fatalf("zulu URLs = %q, want %q", got, want)
	}
	if len(metadata[2].URLs) != 0 {
		t.Fatalf("generic unexpectedly received matched URLs: %v", metadata[2].URLs)
	}
}
