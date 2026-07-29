package upstreamdelta

import (
	"path/filepath"
	"testing"
)

func TestFormatSelectorCurrentUpstreamDelta(t *testing.T) {
	path := filepath.Join("..", "..", "conformance", "upstream-delta", "format-selector-current.json")
	record, err := LoadFormatSelectorCurrentFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if record.To != "fdcc954df4955267ec1627cbeb347b661a110e7c" {
		t.Fatalf("target = %s", record.To)
	}
}

func TestFormatSelectorCurrentRejectsChangedNormativeBlob(t *testing.T) {
	path := filepath.Join("..", "..", "conformance", "upstream-delta", "format-selector-current.json")
	record, err := LoadFormatSelectorCurrentFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record.NormativeSources[0].AfterBlob = "1111111111111111111111111111111111111111"
	if err := record.Validate(); err == nil {
		t.Fatal("Validate accepted changed normative source without executable disposition evidence")
	}
}
