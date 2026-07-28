package format

import (
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestFilterNumericSuffixesAndIrregularCase(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{"1KB", 1000},
		{"1kB", 1024},
		{"1Kb", 1000},
		{"1KiB", 1024},
		{"1MB", 1000 * 1000},
		{"1MiB", 1024 * 1024},
		{"1.5GiB", int64(math.RoundToEven(1.5 * 1024 * 1024 * 1024))},
		{"1M", 1000 * 1000}, // parse_filesize("1M"+"B")
		{"1Mi", 1024 * 1024},
		{"1.5KB", 1500},
		{"2.5KB", 2500},
	}
	for _, test := range cases {
		got, ok := parseFilterNumber(test.raw)
		if !ok || !got.isInt || got.integer.Cmp(big.NewInt(test.want)) != 0 {
			t.Fatalf("parseFilterNumber(%q) = %#v, %v want %d", test.raw, got, ok, test.want)
		}
	}
}

func TestFilterNumericVsStringGrammar(t *testing.T) {
	for _, raw := range []string{"height=-1", "height=1e3", "format_id=123"} {
		predicate, err := compileFilterSpec(raw, 0, len(raw))
		if err != nil {
			t.Fatalf("compileFilterSpec(%q) = %v", raw, err)
		}
		switch raw {
		case "format_id=123":
			if predicate.kind != filterKindNumeric {
				t.Fatalf("%q kind = %v, want numeric", raw, predicate.kind)
			}
		default:
			if predicate.kind != filterKindString {
				t.Fatalf("%q kind = %v, want string", raw, predicate.kind)
			}
		}
	}
}

func TestFilterPlainNumericOverflowMatchesPythonInfinity(t *testing.T) {
	raw := strings.Repeat("9", 400)
	got, ok := parseFilterNumber(raw)
	if !ok || got.isInt || !math.IsInf(got.floating, 1) {
		t.Fatalf("parseFilterNumber(overflow) = %#v, %v", got, ok)
	}
	matched, err := compareFilterNumbers(int64Numeric(math.MaxInt64), got, filterOpLT)
	if err != nil || !matched {
		t.Fatalf("MaxInt64 < +inf = %v, %v", matched, err)
	}
}

func TestFilterNoneInclusiveAndMissing(t *testing.T) {
	object := value.NewObject(value.Field{Key: "format_id", Value: value.String("x")})
	for _, raw := range []string{"missing!=x", "missing=x", "height>1"} {
		predicate, err := compileFilterSpec(raw, 0, len(raw))
		if err != nil {
			t.Fatal(err)
		}
		matched, err := predicate.match(object, nil)
		if err != nil || matched {
			t.Fatalf("%q on missing = %v, %v want false", raw, matched, err)
		}
	}
	for _, raw := range []string{"missing!=?x", "missing=?x", "height>?1"} {
		predicate, err := compileFilterSpec(raw, 0, len(raw))
		if err != nil {
			t.Fatal(err)
		}
		matched, err := predicate.match(object, nil)
		if err != nil || !matched {
			t.Fatalf("%q on missing = %v, %v want true", raw, matched, err)
		}
	}
	nullObject := value.NewObject(value.Field{Key: "missing", Value: value.Null()})
	predicate, err := compileFilterSpec("missing=?x", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := predicate.match(nullObject, nil)
	if err != nil || !matched {
		t.Fatalf("explicit null none-inclusive = %v, %v", matched, err)
	}
}

func TestFilterQuotedEscapesAndEmptyReject(t *testing.T) {
	predicate, err := compileFilterSpec(`format_id="a\"b"`, 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	if predicate.text != `a"b` {
		t.Fatalf("text = %q", predicate.text)
	}
	object := value.NewObject(value.Field{Key: "format_id", Value: value.String(`a"b`)})
	matched, err := predicate.match(object, nil)
	if err != nil || !matched {
		t.Fatalf("quoted escape match = %v, %v", matched, err)
	}
	if _, err := compileFilterSpec(`format_id=""`, 0, 12); err == nil {
		t.Fatal("empty quoted value accepted")
	}
}

func TestFilterNegatedStringOperators(t *testing.T) {
	object := value.NewObject(value.Field{Key: "format_id", Value: value.String("abc-cba")})
	cases := []struct {
		raw  string
		want bool
	}{
		{"format_id!^=abc", false},
		{"format_id!^=zxc", true},
		{"format_id!$=cba", false},
		{"format_id!*=bc-cb", false},
		{"format_id!=abc-cba", false},
		{"format_id!=other", true},
	}
	for _, test := range cases {
		predicate, err := compileFilterSpec(test.raw, 0, len(test.raw))
		if err != nil {
			t.Fatalf("%q: %v", test.raw, err)
		}
		matched, err := predicate.match(object, nil)
		if err != nil || matched != test.want {
			t.Fatalf("%q = %v, %v want %v", test.raw, matched, err, test.want)
		}
	}
}

func TestFilterPreservesRawAndSpan(t *testing.T) {
	selector, err := ParseSelector(`best[filesize <= ? 3000]`)
	if err != nil {
		t.Fatal(err)
	}
	filter := selector.root.filters[0]
	if filter.raw != `filesize <= ? 3000` {
		t.Fatalf("raw = %q", filter.raw)
	}
	if filter.span.start != 5 || filter.span.end != 23 {
		t.Fatalf("span = %d:%d", filter.span.start, filter.span.end)
	}
	if filter.predicate == nil || !filter.predicate.noneInclusive || filter.predicate.kind != filterKindNumeric {
		t.Fatalf("predicate = %#v", filter.predicate)
	}
}

func TestFilterLegacyConstructorCompiles(t *testing.T) {
	selector := Selector{Alternatives: []Choice{{Terms: []Term{{
		Name:    "bestvideo",
		Filters: []Filter{{Field: "height", Operator: "=", Value: "1080"}},
	}}}}}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("1080")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/1080")},
			value.Field{Key: "height", Value: value.Int(1080)},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
	)}))
	selected, err := Select(info, selector)
	if err != nil || len(selected) != 1 || selected[0].ID != "1080" {
		t.Fatalf("legacy filter select = %#v, %v", selected, err)
	}
}

func TestFilterTypeMismatchPropagates(t *testing.T) {
	predicate, err := compileFilterSpec("height>700", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	object := value.NewObject(value.Field{Key: "height", Value: value.String("720")})
	_, err = predicate.match(object, nil)
	if !errors.Is(err, ErrFilterEvaluation) {
		t.Fatalf("type mismatch err = %v", err)
	}
}

func TestFilterRunsOnlyOnAtomCandidates(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("other")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/other")},
			value.Field{Key: "ext", Value: value.String("webm")},
			value.Field{Key: "height", Value: value.String("bad")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
		value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("target")},
			value.Field{Key: "url", Value: value.String("https://example.invalid/target")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "height", Value: value.Int(720)},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
		)),
	)}))
	for _, source := range []string{"target[height>1]", "mp4[height>1]"} {
		selector, err := ParseSelector(source)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := Select(info, selector)
		if err != nil || len(selected) != 1 || selected[0].ID != "target" {
			t.Fatalf("Select(%q) = %#v, %v", source, selected, err)
		}
	}
}
