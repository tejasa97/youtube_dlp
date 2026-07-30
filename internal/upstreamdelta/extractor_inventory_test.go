package upstreamdelta

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRegisteredExtractors(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "_extractors.py")
	body := "from .alpha import AlphaIE\nfrom .beta import (\n    BetaIE,\n    BetaListIE,\n)\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := parseRegisteredExtractors(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Module != "alpha" || got[0].Class != "AlphaIE" ||
		got[2].Module != "beta" || got[2].Class != "BetaListIE" {
		t.Fatalf("unexpected registry: %#v", got)
	}
}

func TestClassifyExtractorPrecedence(t *testing.T) {
	goIDs := map[string]string{"alpha": "alpha"}
	goModules := map[string]bool{"family": true}
	tests := []struct {
		name   string
		module string
		class  string
		block  string
		status string
	}{
		{"obsolete", "old", "OldIE", "class OldIE(InfoExtractor):\n    _WORKING = False\n", ExtractorObsolete},
		{"supported", "alpha", "AlphaIE", "class AlphaIE(InfoExtractor):\n", ExtractorAlreadySupported},
		{"partial", "family", "FamilyListIE", "class FamilyListIE(InfoExtractor):\n", ExtractorPartiallySupported},
		{"auth", "authsite", "AuthIE", "class AuthIE(InfoExtractor):\n    _NETRC_MACHINE = 'x'\n", ExtractorAuthOrAntiBot},
		{"backend", "adapter", "AdapterIE", "class AdapterIE(InfoExtractor):\n    return BrightcoveNewIE.ie_key()\n", ExtractorExistingBackend},
		{"new", "novel", "NovelIE", "class NovelIE(InfoExtractor):\n", ExtractorNewBackend},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyExtractor(test.module, test.class, test.block, goIDs, goModules)
			if got.Status != test.status {
				t.Fatalf("status=%q want=%q: %#v", got.Status, test.status, got)
			}
		})
	}
}

func TestClassBlockDoesNotLeakFollowingClassState(t *testing.T) {
	source := `class WorkingIE(InfoExtractor):
    _VALID_URL = "working"

class BrokenIE(InfoExtractor):
    _WORKING = False
`
	working := classBlock(source, "WorkingIE")
	if strings.Contains(working, "_WORKING = False") {
		t.Fatalf("following class leaked into block: %q", working)
	}
	broken := classBlock(source, "BrokenIE")
	if !strings.Contains(broken, "_WORKING = False") {
		t.Fatalf("broken state missing from block: %q", broken)
	}
}

func TestWriteExtractorInventoryCSVQuotesFields(t *testing.T) {
	var output bytes.Buffer
	entries := []ExtractorInventoryEntry{{
		Module: "sample", Class: "SampleIE", Key: "Sample",
		Status: ExtractorNewBackend, Confidence: "low",
		Rationale: "manual review, including commas",
	}}
	if err := WriteExtractorInventoryCSV(&output, entries); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"manual review, including commas"`) {
		t.Fatalf("CSV did not quote rationale: %q", output.String())
	}
}

func FuzzImportedClass(f *testing.F) {
	f.Add("SampleIE,")
	f.Add("not a class")
	f.Fuzz(func(t *testing.T, input string) {
		got := importedClass(input)
		if got != "" && (!strings.HasSuffix(got, "IE") || strings.ContainsAny(got, " \t\r\n,()")) {
			t.Fatalf("invalid imported class %q from %q", got, input)
		}
	})
}

func TestReconciledExactAliasMappings(t *testing.T) {
	goIDs, goModules, err := parseGoExtractorInventory(filepath.Join("..", "..", "internal", "extractor"))
	if err != nil {
		t.Fatal(err)
	}
	reconciled := map[string]string{
		"BandcampAlbumIE":    "bandcamp",
		"BrightcoveNewIE":    "brightcove",
		"DacastVODIE":        "dacast",
		"ImgurAlbumIE":       "imgur",
		"ImgurGalleryIE":     "imgur",
		"KickClipIE":         "kick",
		"KickVODIE":          "kick",
		"MixcloudPlaylistIE": "mixcloud",
		"MixcloudUserIE":     "mixcloud",
		"RumbleChannelIE":    "rumble",
		"RumbleEmbedIE":      "rumble",
	}
	for class, wantGo := range reconciled {
		if got := exactAliases[class]; got != wantGo {
			t.Fatalf("exactAliases[%s]=%q want %q", class, got, wantGo)
		}
		if _, ok := goIDs[normalizeExtractorKey(wantGo)]; !ok {
			t.Fatalf("reconciled Go extractor %q for %s is not registered", wantGo, class)
		}
		entry := classifyExtractor("fixture", class, "class "+class+"(InfoExtractor):\n", goIDs, goModules)
		if entry.Status != ExtractorAlreadySupported || entry.GoExtractor != wantGo {
			t.Fatalf("%s: got status=%q go_extractor=%q", class, entry.Status, entry.GoExtractor)
		}
	}
	stillPartial := []struct {
		module string
		class  string
	}{
		{"brightcove", "BrightcoveLegacyIE"},
		{"panopto", "PanoptoListIE"},
		{"bbc", "BBCIE"},
		{"soundcloud", "SoundcloudPlaylistIE"},
	}
	for _, test := range stillPartial {
		entry := classifyExtractor(test.module, test.class, "class "+test.class+"(InfoExtractor):\n", goIDs, goModules)
		if entry.Status != ExtractorPartiallySupported {
			t.Fatalf("%s:%s status=%q want %q", test.module, test.class, entry.Status, ExtractorPartiallySupported)
		}
	}
}

func TestCheckedInExtractorInventoryIsCompleteAndConsistent(t *testing.T) {
	path := filepath.Join("..", "..", "conformance", "extractors", "upstream_master_checklist.csv")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1752 {
		t.Fatalf("rows=%d want header + 1751 registered extractors", len(rows))
	}
	validStatuses := map[string]bool{
		ExtractorAlreadySupported:   true,
		ExtractorPartiallySupported: true,
		ExtractorExistingBackend:    true,
		ExtractorNewBackend:         true,
		ExtractorAuthOrAntiBot:      true,
		ExtractorObsolete:           true,
	}
	seen := make(map[string]bool)
	for index, row := range rows[1:] {
		if len(row) != 9 {
			t.Fatalf("row %d has %d columns", index+2, len(row))
		}
		identity := row[0] + ":" + row[1]
		if seen[identity] {
			t.Fatalf("duplicate extractor %s", identity)
		}
		seen[identity] = true
		if !validStatuses[row[3]] {
			t.Fatalf("%s has invalid status %q", identity, row[3])
		}
		if row[3] == ExtractorAlreadySupported && row[4] == "" {
			t.Fatalf("%s is supported without a Go extractor mapping", identity)
		}
		if row[3] == ExtractorExistingBackend && row[5] == "" {
			t.Fatalf("%s is a backend adapter without a backend mapping", identity)
		}
		if row[3] == ExtractorAuthOrAntiBot && row[6] == "" {
			t.Fatalf("%s is auth/anti-bot without a risk flag", identity)
		}
	}
}
