package ytdlp_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestBuiltInExtractorMetadataIsDeterministicAndOffline(t *testing.T) {
	first := ytdlp.BuiltInExtractorMetadata()
	second := ytdlp.BuiltInExtractorMetadata()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("metadata changed between calls")
	}
	if len(first) == 0 {
		t.Fatal("built-in metadata is empty")
	}
	seen := make(map[string]bool, len(first))
	for index, entry := range first {
		if entry.Name == "" || seen[entry.Name] {
			t.Fatalf("invalid or duplicate entry at %d: %#v", index, entry)
		}
		seen[entry.Name] = true
		if len(entry.Description) > 256 || strings.ContainsAny(entry.Description, "\r\n") {
			t.Fatalf("invalid description for %q: %q", entry.Name, entry.Description)
		}
		if index > 0 && strings.ToLower(first[index-1].Name) > strings.ToLower(entry.Name) && entry.Name != "generic" {
			t.Fatalf("metadata is not sorted at %d: %q before %q", index, first[index-1].Name, entry.Name)
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(alias, entry.Name) {
				t.Fatalf("canonical name repeated as alias for %q", entry.Name)
			}
		}
	}
	if first[len(first)-1].Name != "generic" || first[len(first)-1].Description != "Generic downloader that works on some sites" {
		t.Fatalf("generic is not last: %q", first[len(first)-1].Name)
	}

	first[0].Aliases = append(first[0].Aliases, "caller-mutation")
	third := ytdlp.BuiltInExtractorMetadata()
	if reflect.DeepEqual(first, third) {
		t.Fatal("metadata result unexpectedly aliases caller mutation")
	}
}
