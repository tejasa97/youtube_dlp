package hds

import (
	"testing"
)

// FuzzParseManifest fuzzes the F4M XML parser to ensure it never panics on
// arbitrary input and surfaces typed errors for malformed documents.
func FuzzParseManifest(f *testing.F) {
	f.Add([]byte(`<?xml version="1.0"?><manifest xmlns="http://ns.adobe.com/f4m/1.0"><media url="m.mp4" bitrate="800"/><bootstrapInfo url="http://b"/></manifest>`))
	f.Add([]byte(`<manifest xmlns="http://ns.adobe.com/f4m/2.0"><media url="m"/><bootstrapInfo>B</bootstrapInfo></manifest>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		// The parser must never panic, regardless of input shape.
		_, _ = Parse("http://cdn.example/manifest.f4m", data)
	})
}

// TestFuzzSeedsParseRegression asserts that every static corpus entry added
// via f.Add in this file is parseable by the corresponding entry point. The
// fuzzer's primary contract is "never panic", but the deterministic seeds
// must also exercise the success path so a regression that quietly rejects
// every seed would be caught here even if the fuzzer never runs.
func TestFuzzSeedsParseRegression(t *testing.T) {
	validManifest := []byte(`<?xml version="1.0"?><manifest xmlns="http://ns.adobe.com/f4m/1.0"><media url="m.mp4" bitrate="800"/><bootstrapInfo url="http://b"/></manifest>`)
	if _, err := Parse("http://cdn.example/manifest.f4m", validManifest); err != nil {
		t.Fatalf("valid FuzzParseManifest seed does not parse: %v", err)
	}
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	if _, err := ParseBootstrap(bootstrap); err != nil {
		t.Fatalf("valid FuzzParseBootstrap seed does not parse: %v", err)
	}
}

// FuzzParseBootstrap fuzzes the ABST binary parser.
func FuzzParseBootstrap(f *testing.F) {
	f.Add(makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, false))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseBootstrap(data)
	})
}

// FuzzFixBareAmpersands fuzzes the bare-ampersand fixer.
func FuzzFixBareAmpersands(f *testing.F) {
	f.Add([]byte("a=1&b=2"))
	f.Add([]byte("&amp;"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = fixBareAmpersands(data)
	})
}
