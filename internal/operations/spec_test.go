package operations

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "operations", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureSuite(t testing.TB) Suite {
	t.Helper()
	suite, err := DecodeSuite(context.Background(), bytes.NewReader(fixture(t, "canary_suite_v1.json")), 4096)
	if err != nil {
		t.Fatal(err)
	}
	return suite
}

func TestCanarySuiteCanonicalClassesAndSecretHandles(t *testing.T) {
	suite := fixtureSuite(t)
	if len(suite.Canaries) != 3 || suite.Canaries[0].Class != ClassCredential || suite.Canaries[1].Class != ClassPublic || suite.Canaries[2].Class != ClassRegion {
		t.Fatalf("canonical canaries = %#v", suite.Canaries)
	}
	if suite.Canaries[0].Secret != (SecretHandle{Provider: "keychain", Name: "youtube.fixture"}) {
		t.Fatalf("credential handle = %#v", suite.Canaries[0].Secret)
	}
	data, err := MarshalSuite(suite)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(fixture(t, "canary_suite_v1.json"))) != string(data) {
		t.Fatalf("suite is not canonical:\n%s", data)
	}
}

func TestCanarySpecsRejectEmbeddedSecretsURLsAndResources(t *testing.T) {
	base := CanarySpec{ID: "public.test", Class: ClassPublic, Extractor: "generic", TargetRef: "public.fixture", Capabilities: []string{"extract"}, TimeoutMS: 1000}
	tests := []CanarySpec{
		{ID: base.ID, Class: base.Class, Extractor: base.Extractor, TargetRef: "https://example.test/path?token=secret", Capabilities: base.Capabilities, TimeoutMS: base.TimeoutMS},
		{ID: "credential.test", Class: ClassCredential, Extractor: "generic", TargetRef: "private.fixture", Capabilities: base.Capabilities, TimeoutMS: 1000},
		{ID: "region.test", Class: ClassRegion, Extractor: "generic", TargetRef: "region.fixture", Capabilities: base.Capabilities, Region: "gb", TimeoutMS: 1000},
		{ID: base.ID, Class: base.Class, Extractor: base.Extractor, TargetRef: base.TargetRef, Capabilities: []string{"extract", "extract"}, TimeoutMS: 1000},
		{ID: base.ID, Class: base.Class, Extractor: base.Extractor, TargetRef: base.TargetRef, Capabilities: base.Capabilities, Secret: SecretHandle{Provider: "vault", Name: "must-not-be-here"}, TimeoutMS: 1000},
	}
	for _, spec := range tests {
		if _, err := NewSuite([]CanarySpec{spec}); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("NewSuite(%#v) error = %v", spec, err)
		}
	}
	overflow := base
	overflow.Capabilities = make([]string, MaxCapabilities+1)
	for index := range overflow.Capabilities {
		overflow.Capabilities[index] = "cap." + strings.Repeat("a", index%50) + string(rune('a'+index%26))
	}
	if _, err := NewSuite([]CanarySpec{overflow}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("capability bound error = %v", err)
	}
}

func TestDecodeSuiteIsStrictBoundedCanceledAndRedacted(t *testing.T) {
	const secret = "password=must-not-leak"
	malformed := []string{
		``,
		`{"schema_version":1,"canaries":[]}`,
		`{"schema_version":2,"canaries":[]}`,
		`{"schema_version":1,"canaries":[],"password":"` + secret + `"}`,
		string(fixture(t, "canary_suite_v1.json")) + `{}`,
	}
	for _, input := range malformed {
		_, err := DecodeSuite(context.Background(), strings.NewReader(input), 4096)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("DecodeSuite() error = %v", err)
		}
	}
	if _, err := DecodeSuite(context.Background(), bytes.NewReader(fixture(t, "canary_suite_v1.json")), 8); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("decode byte limit error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeSuite(ctx, bytes.NewReader(fixture(t, "canary_suite_v1.json")), 4096); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled decode error = %v", err)
	}
}

func TestSuiteOrderingProperty(t *testing.T) {
	property := func(reverse bool) bool {
		specs := append([]CanarySpec(nil), fixtureSuite(t).Canaries...)
		if reverse {
			for left, right := 0, len(specs)-1; left < right; left, right = left+1, right-1 {
				specs[left], specs[right] = specs[right], specs[left]
			}
		}
		suite, err := NewSuite(specs)
		if err != nil {
			return false
		}
		actual, _ := MarshalSuite(suite)
		return string(actual) == strings.TrimSpace(string(fixture(t, "canary_suite_v1.json")))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Fatal(err)
	}
}

func FuzzDecodeSuite(f *testing.F) {
	f.Add(fixture(f, "canary_suite_v1.json"))
	f.Add([]byte(`{"schema_version":1,"canaries":[]}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		suite, err := DecodeSuite(context.Background(), bytes.NewReader(data), 1<<20)
		if err != nil {
			return
		}
		canonical, err := MarshalSuite(suite)
		if err != nil {
			t.Fatalf("decoded suite did not marshal: %v", err)
		}
		if _, err := DecodeSuite(context.Background(), bytes.NewReader(canonical), 1<<20); err != nil {
			t.Fatalf("canonical suite did not decode: %v", err)
		}
	})
}
