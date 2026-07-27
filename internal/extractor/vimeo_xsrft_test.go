package extractor

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseVimeoViewerXSRFT(t *testing.T) {
	maxToken := strings.Repeat("a", maxVimeoViewerXSRFTBytes)
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{name: "token", payload: []byte(`{"xsrft":"abc123"}`), want: "abc123"},
		{name: "extra fields", payload: []byte(`{"jwt":"ignored","xsrft":"token-1","extra":[1,2,3]}`), want: "token-1"},
		{name: "punctuation", payload: []byte(`{"xsrft":"!#$%&'()*+,-./:;<=>?@[\\]^_` + "`" + `{|}~"}`), want: "!#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"},
		{name: "unicode", payload: []byte(`{"xsrft":"résumé-密碼"}`), want: "résumé-密碼"},
		{name: "interior whitespace", payload: []byte(`{"xsrft":"a b\tc"}`), want: "a b\tc"},
		{name: "maximum token", payload: []byte(fmt.Sprintf(`{"xsrft":%q}`, maxToken)), want: maxToken},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseVimeoViewerXSRFT(test.payload)
			if err != nil {
				t.Fatalf("parseVimeoViewerXSRFT error = %v", err)
			}
			if got != test.want {
				t.Fatalf("parseVimeoViewerXSRFT = %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseVimeoViewerXSRFTRejectsInvalidInput(t *testing.T) {
	const secretFixture = "VimeoViewerSecret-AAAAA-12345"
	invalidUTF8Payload := append([]byte(`{"xsrft":"`), 0xff)
	invalidUTF8Payload = append(invalidUTF8Payload, []byte(`"}`)...)
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty payload"},
		{name: "whitespace-only payload", payload: []byte(" \n\t")},
		{name: "malformed", payload: []byte(`{"xsrft":"` + secretFixture)},
		{name: "top-level null", payload: []byte(`null`)},
		{name: "top-level array", payload: []byte(`[{"xsrft":"` + secretFixture + `"}]`)},
		{name: "trailing object", payload: []byte(`{"xsrft":"a"}{"secret":"` + secretFixture + `"}`)},
		{name: "trailing scalar", payload: []byte(`{"xsrft":"a"} "` + secretFixture + `"`)},
		{name: "missing", payload: []byte(`{"secret":"` + secretFixture + `"}`)},
		{name: "empty token", payload: []byte(`{"xsrft":""}`)},
		{name: "null token", payload: []byte(`{"xsrft":null,"secret":"` + secretFixture + `"}`)},
		{name: "non-string token", payload: []byte(`{"xsrft":123,"secret":"` + secretFixture + `"}`)},
		{name: "invalid UTF-8", payload: invalidUTF8Payload},
		{name: "overlong token", payload: []byte(`{"xsrft":"` + secretFixture + strings.Repeat("a", maxVimeoViewerXSRFTBytes) + `"}`)},
		{name: "leading ASCII whitespace", payload: []byte(`{"xsrft":" ` + secretFixture + `"}`)},
		{name: "trailing ASCII whitespace", payload: []byte(`{"xsrft":"` + secretFixture + `\t"}`)},
		{name: "leading Unicode whitespace", payload: []byte(`{"xsrft":" ` + secretFixture + `"}`)},
		{name: "trailing Unicode whitespace", payload: []byte(`{"xsrft":"` + secretFixture + ` "}`)},
		{name: "NUL", payload: []byte(`{"xsrft":"` + secretFixture + `\u0000x"}`)},
		{name: "CR", payload: []byte(`{"xsrft":"` + secretFixture + `\r"}`)},
		{name: "LF", payload: []byte(`{"xsrft":"` + secretFixture + `\n"}`)},
		{name: "oversized payload", payload: bytes.Repeat([]byte("x"), int(maxExtractorJSONBytes)+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseVimeoViewerXSRFT(test.payload)
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("parseVimeoViewerXSRFT error = %v, want ErrInvalidMetadata", err)
			}
			if got != "" {
				t.Fatalf("parseVimeoViewerXSRFT = %q, want empty result on failure", got)
			}
			if strings.Contains(err.Error(), secretFixture) {
				t.Fatalf("error exposed payload/token fixture: %q", err.Error())
			}
		})
	}
}

func FuzzParseVimeoViewerXSRFT(f *testing.F) {
	const secretFixture = "VimeoViewerFuzzSecret-AAAAA-12345"
	f.Add([]byte(`{"xsrft":"abc"}`))
	f.Add([]byte(`{"xsrft":"` + secretFixture + `\u0000"}`))
	f.Add([]byte(`{"xsrft":" ` + secretFixture + `"}`))
	f.Add([]byte(`{"xsrft":"abc"}{"secret":"` + secretFixture + `"}`))
	f.Add([]byte(`not-json-` + secretFixture))
	f.Fuzz(func(t *testing.T, data []byte) {
		token, err := parseVimeoViewerXSRFT(data)
		if err == nil {
			if token == "" || len(token) > maxVimeoViewerXSRFTBytes || !utf8.ValidString(token) {
				t.Fatalf("invalid successful token (bytes=%d)", len(token))
			}
			if strings.ContainsAny(token, "\x00\r\n") || strings.TrimSpace(token) != token {
				t.Fatal("successful token violates control/whitespace contract")
			}
			return
		}
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("error = %v, want ErrInvalidMetadata", err)
		}
		if strings.Contains(err.Error(), secretFixture) {
			t.Fatalf("error exposed raw secret fixture: %q", err.Error())
		}
	})
}
