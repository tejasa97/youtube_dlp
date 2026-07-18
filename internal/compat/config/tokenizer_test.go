package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestTokenizeQuotesCommentsEscapesAndLocations(t *testing.T) {
	tokens, err := Tokenize("# heading\n--one 'a b' \"c # d\" escaped\\ value empty=\"\" foo#ignored\n--two line\\\ncontinued", "fixture.conf", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := values(tokens), []string{"--one", "a b", "c # d", "escaped value", "empty=", "foo", "--two", "linecontinued"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens = %#v, want %#v", got, want)
	}
	if tokens[0].Line != 2 || tokens[0].Column != 1 || tokens[6].Line != 3 {
		t.Fatalf("unexpected locations: %#v", tokens)
	}
}

func TestTokenizeEmptyQuotedWord(t *testing.T) {
	tokens, err := Tokenize(`'' ""`, "empty.conf", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got := values(tokens); !reflect.DeepEqual(got, []string{"", ""}) {
		t.Fatalf("got %#v", got)
	}
}

func TestTokenizeExactSyntaxLocationAndNoSecret(t *testing.T) {
	_, err := Tokenize("--password super-secret\n--output 'unterminated", "broken.conf", DefaultLimits())
	var failure *Error
	if !errors.As(err, &failure) || failure.Category != ErrorSyntax || failure.Line != 2 || failure.Column != 10 {
		t.Fatalf("unexpected error: %#v", err)
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("diagnostic exposed secret: %v", err)
	}
}

func TestDiagnosticSourceCannotInjectLines(t *testing.T) {
	_, err := Tokenize("'", "bad\nsource.conf", DefaultLimits())
	if err == nil || strings.Contains(err.Error(), "bad\nsource") {
		t.Fatalf("unsafe diagnostic: %v", err)
	}
}

func TestDecodeBOMsAndCodingDeclarations(t *testing.T) {
	utf8Text, err := Decode(append([]byte{0xef, 0xbb, 0xbf}, []byte("--title café")...), "utf8.conf")
	if err != nil || utf8Text != "--title café" {
		t.Fatalf("UTF-8 BOM: %q, %v", utf8Text, err)
	}

	units := utf16.Encode([]rune("--title snowman"))
	utf16Data := []byte{0xff, 0xfe}
	for _, unit := range units {
		utf16Data = append(utf16Data, byte(unit), byte(unit>>8))
	}
	decoded, err := Decode(utf16Data, "utf16.conf")
	if err != nil || decoded != "--title snowman" {
		t.Fatalf("UTF-16: %q, %v", decoded, err)
	}

	cp1252 := append([]byte("# coding: windows-1252\n--title caf"), 0xe9)
	decoded, err = Decode(cp1252, "cp.conf")
	if err != nil || !strings.Contains(decoded, "café") {
		t.Fatalf("cp1252: %q, %v", decoded, err)
	}
}

func TestDecodeRejectsMalformedAndUnsupportedEncoding(t *testing.T) {
	if _, err := Decode([]byte{0xff}, "bad.conf"); !IsCategory(err, ErrorEncoding) {
		t.Fatalf("expected encoding error, got %v", err)
	}
	if _, err := Decode([]byte("# coding: shift-jis\n--x y"), "bad.conf"); !IsCategory(err, ErrorEncoding) {
		t.Fatalf("expected unsupported error, got %v", err)
	}
}

func TestTokenizeShellQuoteRoundTripProperty(t *testing.T) {
	valuesToTry := []string{"", "plain", "two words", "a'b", "#comment", "\\", "line\nbreak", "日本語"}
	for _, value := range valuesToTry {
		tokens, err := Tokenize(shellQuote(value), "property", DefaultLimits())
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if len(tokens) != 1 || tokens[0].Value != value {
			t.Fatalf("roundtrip %q => %#v", value, tokens)
		}
	}
}

func TestTokenizeResourceLimits(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxTokenBytes = 3
	if _, err := Tokenize("four", "large.conf", limits); !IsCategory(err, ErrorResource) {
		t.Fatalf("got %v", err)
	}
	limits = DefaultLimits()
	limits.MaxTokens = 1
	if _, err := Tokenize("one two", "many.conf", limits); !IsCategory(err, ErrorResource) {
		t.Fatalf("got %v", err)
	}
}

func values(tokens []Token) []string {
	result := make([]string, len(tokens))
	for index := range tokens {
		result[index] = tokens[index].Value
	}
	return result
}
