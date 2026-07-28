package format

import (
	"errors"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestParserParityOfficialExamples(t *testing.T) {
	examples := []string{
		"22,17,18",
		"136/137/mp4/bestvideo,140/m4a/bestaudio",
		"bestvideo+bestaudio",
		"bv*+ba/b",
		"bv*+mergeall[vcodec=none]",
		"bv*+ba+ba.2",
		"bv*[ext=mp4]+ba[ext=m4a]/b[ext=mp4] / bv*+ba/b",
		"bv*[height<=480]+ba/b[height<=480] / wv*+ba/w",
		"(bv*+ba/b)[protocol^=http][protocol!*=dash] / (bv*+ba/b)",
		"((bv*[fps>30]/bv*)[height<=720]/(wv*[fps>30]/wv*)) + ba / (b[fps>30]/b)[height<=720]/(w[fps>30]/w)",
	}
	for _, example := range examples {
		t.Run(example, func(t *testing.T) {
			if _, err := ParseSelector(example); err != nil {
				t.Fatalf("ParseSelector(%q) = %v", example, err)
			}
		})
	}
}

func TestParserParityQuotedAndEscapedFilterBrackets(t *testing.T) {
	for _, input := range []string{
		`best[format_id="a]b"]`,
		`best[format_id='a]b']`,
		`best[format_id="a\"b]c"]`,
		`best[format_id=a\]b]`,
	} {
		if _, err := ParseSelector(input); err != nil {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
	}
}

func TestParserParityDirectIDPunctuation(t *testing.T) {
	for _, input := range []string{
		"dash-video-low",
		"codec:v1@cdn",
		"id%2A;variant",
		"stream{main}",
		"track=primary",
	} {
		selector, err := ParseSelector(input)
		if err != nil {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
		if selector.root == nil || selector.root.atom.kind != atomDirectID || selector.root.atom.text != input {
			t.Fatalf("ParseSelector(%q) atom = %#v", input, selector.root)
		}
	}
}

func TestParserParityRejectsDiscardedPythonTokenPunctuation(t *testing.T) {
	for _, input := range []string{
		"id!variant",
		"id$variant",
		"id?variant",
		"id#variant",
		`id\variant`,
		`id"variant`,
		"id'variant",
		"id`variant",
	} {
		if _, err := ParseSelector(input); !errors.Is(err, ErrInvalidSelector) {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
	}
}

func TestParserParityNegatedFilterSyntax(t *testing.T) {
	for _, input := range []string{
		"best[protocol!^=http]",
		"best[protocol!$=dash]",
		"best[protocol!*=dash]",
		"best[protocol!~=^http]",
	} {
		selector, err := ParseSelector(input)
		if err != nil {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
		if len(selector.root.filters) != 1 || selector.root.filters[0].Operator != input[13:16] {
			t.Fatalf("ParseSelector(%q) filters = %#v", input, selector.root.filters)
		}
	}
}

func TestParserParityExactExtensionRecognition(t *testing.T) {
	for _, input := range []string{"mp4", "m4a", "3gp", "mhtml"} {
		selector, err := ParseSelector(input)
		if err != nil || selector.root.atom.kind != atomExtension {
			t.Fatalf("ParseSelector(%q) = %#v, %v", input, selector.root, err)
		}
	}
	for _, input := range []string{"3g2", "f4v", "mk3d", "divx", "mpg", "ogv", "m4v", "wmv"} {
		selector, err := ParseSelector(input)
		if err != nil || selector.root.atom.kind != atomDirectID {
			t.Fatalf("ParseSelector(%q) = %#v, %v", input, selector.root, err)
		}
	}
}

func TestParserParityOperatorPrecedence(t *testing.T) {
	selector, err := ParseSelector("a+b/c,d+e/f")
	if err != nil {
		t.Fatal(err)
	}
	root := selector.root
	if root.kind != astComma || len(root.children) != 2 {
		t.Fatalf("root = %#v", root)
	}
	for index := range root.children {
		choice := &root.children[index]
		if choice.kind != astPickFirst || choice.children[0].kind != astMerge {
			t.Fatalf("output[%d] = %#v", index, choice)
		}
	}
}

func TestParserParityUnboundedPositiveAtomIndex(t *testing.T) {
	for _, input := range []string{"best.1001", "bv*.4294967296", "worstaudio." + strings.Repeat("9", 128)} {
		selector, err := ParseSelector(input)
		if err != nil {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
		if !selector.root.atom.quality.OK {
			t.Fatalf("ParseSelector(%q) atom = %#v", input, selector.root.atom)
		}
	}

	selector, err := ParseSelector("best." + strings.Repeat("9", 128))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Select(advancedSelectorInfo(), selector); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("Select(huge index) = %v", err)
	}
}

func TestParserParitySyntaxSpansUseOriginalInput(t *testing.T) {
	tests := []struct {
		input      string
		start, end int
	}{
		{"  bestvideo+?unknown  ", 12, 20},
		{"best[format_id='unterminated]", 15, 29},
		{"(bestvideo+bestaudio", 0, 20},
		{"bestvideo,,", 10, 11},
		{"bestvideo)", 9, 10},
		{",best", 0, 1},
	}
	for _, test := range tests {
		_, err := ParseSelector(test.input)
		var syntaxError *SyntaxError
		if !errors.As(err, &syntaxError) {
			t.Fatalf("ParseSelector(%q) = %v", test.input, err)
		}
		if syntaxError.Start != test.start || syntaxError.End != test.end {
			t.Fatalf("ParseSelector(%q) span = %d:%d, want %d:%d", test.input, syntaxError.Start, syntaxError.End, test.start, test.end)
		}
	}
}

func TestParserParityTokenJoinAndDanglingBranches(t *testing.T) {
	for _, input := range []string{
		"best video",
		"best . 2",
		"()",
		"best,",
		"best/",
		"best//",
		"(best/)",
		"best/,",
		"best*foo",
		"best.01",
		"best.0",
		"best.",
		"best.1001",
	} {
		if _, err := ParseSelector(input); err != nil {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
	}
	for _, input := range []string{",best", "best,,", "(,best)", "(best,,)", "best+"} {
		if _, err := ParseSelector(input); !errors.Is(err, ErrInvalidSelector) {
			t.Fatalf("ParseSelector(%q) = %v", input, err)
		}
	}

	selector, err := ParseSelector("best//")
	if err != nil {
		t.Fatal(err)
	}
	if selector.root.atom.kind != atomDirectID || selector.root.atom.text != "best//" {
		t.Fatalf("best// atom = %#v", selector.root.atom)
	}

	format := func(id string) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/" + id)},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		))
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		format("best*foo"), format("best.01"), format("best.0"), format("best."),
	)}))
	for _, id := range []string{"best*foo", "best.01", "best.0", "best."} {
		selector, err := ParseSelector(id)
		if err != nil {
			t.Fatalf("ParseSelector(%q): %v", id, err)
		}
		selected, err := Select(info, selector)
		if err != nil || len(selected) != 1 || selected[0].ID != id {
			t.Fatalf("Select(%q) = %#v, %v", id, selected, err)
		}
	}
}

func FuzzParserParitySpans(f *testing.F) {
	for _, seed := range []string{
		"bestvideo+bestaudio/best",
		`best[format_id="a]b"]`,
		"((bv*/b)+ba),best.1001",
		"  bestvideo+?unknown",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_, first := ParseSelector(input)
		_, second := ParseSelector(input)
		if (first == nil) != (second == nil) {
			t.Fatalf("nondeterministic parse for %q: %v, %v", input, first, second)
		}
		var syntaxError *SyntaxError
		if errors.As(first, &syntaxError) {
			if syntaxError.Start < 0 || syntaxError.Start > syntaxError.End || syntaxError.End > len(input) {
				t.Fatalf("ParseSelector(%q) invalid span %d:%d", input, syntaxError.Start, syntaxError.End)
			}
		}
	})
}
