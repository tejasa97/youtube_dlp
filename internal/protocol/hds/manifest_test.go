package hds

import (
	"errors"
	"strings"
	"testing"
)

const f4mV1 = `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="%s">
  <baseURL>http://cdn.example/</baseURL>
  <pv-2.0>abc=1;</pv-2.0>
  <media url="media.mp4" bitrate="800" width="640" height="360"/>
  <media url="media2.mp4" bitrate="1600" width="1280" height="720"/>
  <bootstrapInfo url="http://cdn.example/bootstrap.bin"/>
</manifest>`

func TestParseSelectsHighestBitrate(t *testing.T) {
	body := []byte(fmtxml(f4mV1, f4mNamespaceV1))
	manifest, err := Parse("http://cdn.example/manifest.f4m", body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	selected, err := manifest.SelectMedia(0)
	if err != nil {
		t.Fatalf("SelectMedia: %v", err)
	}
	if selected.Bitrate != 1600 {
		t.Fatalf("bitrate = %d, want 1600", selected.Bitrate)
	}
}

func TestParseSelectsRequestedBitrate(t *testing.T) {
	body := []byte(fmtxml(f4mV1, f4mNamespaceV1))
	manifest, err := Parse("http://cdn.example/manifest.f4m", body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	selected, err := manifest.SelectMedia(800)
	if err != nil {
		t.Fatalf("SelectMedia: %v", err)
	}
	if selected.Bitrate != 800 {
		t.Fatalf("bitrate = %d, want 800", selected.Bitrate)
	}
}

func TestParseRejectsUnscopedDRMHeader(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <drmAdditionalHeader>payload</drmAdditionalHeader>
  <media url="http://cdn.example/media.mp4" bitrate="800"/>
</manifest>`
	_, err := Parse("http://cdn.example/manifest.f4m", []byte(doc))
	if !errors.Is(err, ErrUnsupportedDRM) {
		t.Fatalf("err = %v, want ErrUnsupportedDRM", err)
	}
}

func TestParseFiltersDRMLinkedMedia(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <drmAdditionalHeader id="encrypted">payload</drmAdditionalHeader>
  <media url="http://cdn.example/encrypted.mp4" bitrate="800" drmAdditionalHeaderId="encrypted"/>
  <media url="http://cdn.example/clear.mp4" bitrate="1600"/>
  <bootstrapInfo url="http://cdn.example/bootstrap.bin"/>
</manifest>`
	manifest, err := Parse("http://cdn.example/manifest.f4m", []byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(manifest.Media) != 1 || manifest.Media[0].Bitrate != 1600 {
		t.Fatalf("Media = %+v, want exactly the clear entry", manifest.Media)
	}
}

func TestParseRequiresF4MNamespace(t *testing.T) {
	doc := `<manifest xmlns="http://example.invalid/not-f4m"><media url="x" bitrate="1"/></manifest>`
	_, err := Parse("http://cdn.example/manifest.f4m", []byte(doc))
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err = %v, want ErrInvalidManifest", err)
	}
	if !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("err = %v, want namespace mention", err)
	}
}

func TestParseAcceptsBareAmpersandInQuery(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <media url="media.mp4?a=1&b=2" bitrate="800"/>
  <bootstrapInfo url="http://cdn.example/bootstrap.bin"/>
</manifest>`
	manifest, err := Parse("http://cdn.example/manifest.f4m", []byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !strings.Contains(manifest.Media[0].URL, "a=1") || !strings.Contains(manifest.Media[0].URL, "b=2") {
		t.Fatalf("URL lost query: %q", manifest.Media[0].URL)
	}
}

func TestFixBareAmpersandsPreservesEntities(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ampEntity", "A &amp; B", "A &amp; B"},
		{"ltEntity", "1 &lt; 2", "1 &lt; 2"},
		{"numericDecimal", "&#65;", "&#65;"},
		{"numericHex", "&#x41;", "&#x41;"},
		{"bareAnd", "a=1&b=2", "a=1&amp;b=2"},
		{"mixed", "OK&amp;raw&end", "OK&amp;raw&amp;end"},
		{"unknownEntityEscaped", "&unknown;", "&amp;unknown;"},
		{"semicolonOnlyIsBare", "&; and& or", "&amp;; and&amp; or"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(fixBareAmpersands([]byte(tc.in)))
			if got != tc.want {
				t.Fatalf("fixBareAmpersands(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseAllowsTrailingWhitespace(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <media url="media.mp4" bitrate="800"/>
  <bootstrapInfo url="http://cdn.example/bootstrap.bin"/>
</manifest>
   `
	if _, err := Parse("http://cdn.example/manifest.f4m", []byte(doc)); err != nil {
		t.Fatalf("Parse with trailing whitespace: %v", err)
	}
}

func TestParseAllowsTrailingComment(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <media url="media.mp4" bitrate="800"/>
  <bootstrapInfo url="http://cdn.example/bootstrap.bin"/>
</manifest>
<!-- final note -->`
	if _, err := Parse("http://cdn.example/manifest.f4m", []byte(doc)); err != nil {
		t.Fatalf("Parse with trailing comment: %v", err)
	}
}

func TestParseRejectsTrailingElement(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <media url="media.mp4" bitrate="800"/>
  <bootstrapInfo url="http://cdn.example/bootstrap.bin"/>
</manifest>
<extra/>`
	if _, err := Parse("http://cdn.example/manifest.f4m", []byte(doc)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err = %v, want ErrInvalidManifest", err)
	}
}

func TestParseRejectsTrailingText(t *testing.T) {
	doc := `<manifest xmlns="http://ns.adobe.com/f4m/1.0"><media url="media.mp4" bitrate="800"/><bootstrapInfo url="http://cdn.example/bootstrap.bin"/></manifest>tail`
	if _, err := Parse("http://cdn.example/manifest.f4m", []byte(doc)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err = %v, want ErrInvalidManifest", err)
	}
}

func TestParseResolvesRelativeBaseURL(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>/streams/abc/</baseURL>
  <media url="media.mp4" bitrate="800"/>
  <bootstrapInfo url="http://cdn.example/bootstrap.bin"/>
</manifest>`
	manifest, err := Parse("http://cdn.example/path/manifest.f4m", []byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if manifest.Media[0].URL != "http://cdn.example/streams/abc/media.mp4" {
		t.Fatalf("URL = %q, want cdn resolved", manifest.Media[0].URL)
	}
}

func TestParseAcceptsCrossHostSchemeRelativeMedia(t *testing.T) {
	// Scheme-relative refs inherit the manifest URL's scheme (http here), not
	// https. Cross-host acceptance is intentional: HDS media is commonly
	// served from a different CDN than the manifest, and the downloader
	// scrubs credential headers for foreign hosts via sanitizeHeaders.
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>http://cdn.example/</baseURL>
  <media url="//other-cdn.example/x.mp4" bitrate="800"/>
  <bootstrapInfo url="http://cdn.example/bootstrap.bin"/>
</manifest>`
	manifest, err := Parse("http://cdn.example/manifest.f4m", []byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if manifest.Media[0].URL != "http://other-cdn.example/x.mp4" {
		t.Fatalf("URL = %q, want http://other-cdn.example/x.mp4 (inherited scheme)", manifest.Media[0].URL)
	}
}

func TestParseRejectsDisallowedScheme(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>javascript:alert(1)</baseURL>
  <media url="x.mp4" bitrate="800"/>
</manifest>`
	if _, err := Parse("http://cdn.example/manifest.f4m", []byte(doc)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err = %v, want ErrInvalidManifest", err)
	}
}

func TestParseRejectsUserinfoInBaseURL(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>http://user:pass@cdn.example/</baseURL>
  <media url="x.mp4" bitrate="800"/>
</manifest>`
	if _, err := Parse("http://cdn.example/manifest.f4m", []byte(doc)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err = %v, want ErrInvalidManifest", err)
	}
}

func TestParseDecodesBootstrapInBounds(t *testing.T) {
	payload := strings.Repeat("A", 100)
	encoded := base64Encode(payload)
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>http://cdn.example/</baseURL>
  <media url="m.mp4" bitrate="800"/>
  <bootstrapInfo>` + encoded + `</bootstrapInfo>
</manifest>`
	manifest, err := Parse("http://cdn.example/manifest.f4m", []byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(manifest.Bootstrap.Inline) != 100 {
		t.Fatalf("bootstrap bytes = %d, want 100", len(manifest.Bootstrap.Inline))
	}
}

func TestParseRejectsOversizedBootstrap(t *testing.T) {
	payload := strings.Repeat("A", maxDecodedBootstrapBytes+10)
	encoded := base64Encode(payload)
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>http://cdn.example/</baseURL>
  <media url="m.mp4" bitrate="800"/>
  <bootstrapInfo>` + encoded + `</bootstrapInfo>
</manifest>`
	if _, err := Parse("http://cdn.example/manifest.f4m", []byte(doc)); !errors.Is(err, ErrInvalidBootstrap) {
		t.Fatalf("err = %v, want ErrInvalidBootstrap", err)
	}
}

func fmtxml(template, ns string) string {
	return strings.Replace(template, "%s", ns, 1)
}

func base64Encode(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	out := make([]byte, 0, ((len(s)+2)/3)*4)
	for i := 0; i < len(s); i += 3 {
		var n uint32
		switch len(s) - i {
		case 1:
			n = uint32(s[i]) << 16
			out = append(out, alphabet[(n>>18)&0x3f], alphabet[(n>>12)&0x3f], '=', '=')
		case 2:
			n = uint32(s[i])<<16 | uint32(s[i+1])<<8
			out = append(out, alphabet[(n>>18)&0x3f], alphabet[(n>>12)&0x3f], alphabet[(n>>6)&0x3f], '=')
		default:
			n = uint32(s[i])<<16 | uint32(s[i+1])<<8 | uint32(s[i+2])
			out = append(out, alphabet[(n>>18)&0x3f], alphabet[(n>>12)&0x3f], alphabet[(n>>6)&0x3f], alphabet[n&0x3f])
		}
	}
	return string(out)
}
