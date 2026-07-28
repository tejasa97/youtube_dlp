package format

import (
	"errors"
	"math/big"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/dlclark/regexp2"
)

func TestMain(m *testing.M) {
	initRegexTimeoutClock()
	code := m.Run()
	regexp2.StopTimeoutClock()
	os.Exit(code)
}

func TestPythonRegexNamedGroupsAndBackrefs(t *testing.T) {
	expression, err := compilePythonRegex(`^(?P<word>\w+)-(?P=word)$`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := expression.search("hé-hé", nil)
	if err != nil || !matched {
		t.Fatalf("named backref = %v, %v", matched, err)
	}
	matched, err = expression.search("hé-ho", nil)
	if err != nil || matched {
		t.Fatalf("named backref mismatch = %v, %v", matched, err)
	}
}

func TestPythonRegexZAndFinalNewline(t *testing.T) {
	expression, err := compilePythonRegex(`foo\Z`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := expression.search("foo\n", nil)
	if err != nil || matched {
		t.Fatalf(`\Z matched final newline = %v, %v`, matched, err)
	}
	matched, err = expression.search("foo", nil)
	if err != nil || !matched {
		t.Fatalf(`\Z exact = %v, %v`, matched, err)
	}
}

func TestPythonRegexLookaroundAndFixedWidth(t *testing.T) {
	expression, err := compilePythonRegex(`(?<=dash)-low$`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := expression.search("dash-low", nil)
	if err != nil || !matched {
		t.Fatalf("lookbehind = %v, %v", matched, err)
	}
	if _, err := compilePythonRegex(`(?<=a+)b`, 0, 0); err == nil {
		t.Fatal("variable-width lookbehind accepted")
	}
	if _, err := compilePythonRegex(`(?<=a|bc)c`, 0, 0); err == nil {
		t.Fatal("unequal lookbehind alternatives accepted")
	}
	expression, err = compilePythonRegex(`(?<=a(?:b|c))d`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched, err = expression.search("abd", nil)
	if err != nil || !matched {
		t.Fatalf("nested fixed lookbehind = %v, %v", matched, err)
	}
}

func TestPythonRegexRejectsDotNetOnly(t *testing.T) {
	for _, pattern := range []string{`(?<x>a)`, `(?'x'a)`, `\k<x>`, `\p{L}`, `(?(?=a)b|c)`, `(?n)a`} {
		if _, err := compilePythonRegex(pattern, 0, 0); err == nil {
			t.Fatalf("accepted %q", pattern)
		}
	}
}

func TestPythonRegexUnicodeClasses(t *testing.T) {
	expression, err := compilePythonRegex(`^\w+$`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := expression.search("héllo", nil)
	if err != nil || !matched {
		t.Fatalf("unicode word = %v, %v", matched, err)
	}
	matched, err = expression.search("a\u0301", nil)
	if err != nil || matched {
		t.Fatalf("combining mark word = %v, %v", matched, err)
	}
	space, err := compilePythonRegex(`^\s$`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched, err = space.search("\x1c", nil)
	if err != nil || !matched {
		t.Fatalf("python space U+001C = %v, %v", matched, err)
	}
}

func TestPythonRegexMixedClassShorthands(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{`[a\w]`, "é", true},
		{`(?a)[a\w]`, "é", false},
		{`[a\W]`, "!", true},
		{`[a\W]`, "b", false},
		{`[^a\W]`, "b", true},
		{`[^a\W]`, "a", false},
		{`[\S\W]`, " ", true},
	}
	for _, test := range cases {
		expression, err := compilePythonRegex(test.pattern, 0, 0)
		if err != nil {
			t.Fatalf("%q: %v", test.pattern, err)
		}
		matched, err := expression.search(test.input, nil)
		if err != nil || matched != test.want {
			t.Fatalf("%q against %q = %v, %v want %v", test.pattern, test.input, matched, err, test.want)
		}
	}
}

func TestPythonRegexIgnoreCaseEdges(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{`(?i)i`, "ı", true},
		{`(?i)i`, "İ", true},
		{`(?i)s`, "ſ", true},
		{`(?i)k`, "K", true},
		{`(?i)[i]`, "ı", true},
		{`(?ai)i`, "ı", false},
		{`(?i)[^i]`, "ı", false},
	}
	for _, test := range cases {
		expression, err := compilePythonRegex(test.pattern, 0, 0)
		if err != nil {
			t.Fatalf("%q: %v", test.pattern, err)
		}
		matched, err := expression.search(test.input, nil)
		if err != nil || matched != test.want {
			t.Fatalf("%q against %q = %v, %v want %v", test.pattern, test.input, matched, err, test.want)
		}
	}
}

func TestPythonRegexUnicodeNames(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
	}{
		{`\N{GREEK SMALL LETTER ALPHA}`, "α"},
		{`\N{GRINNING FACE}`, "😀"},
		{`\N{CJK UNIFIED IDEOGRAPH-4E2D}`, "中"},
		{`\N{BOM}`, "\ufeff"},
		{`\N{ZWJ}`, "\u200d"},
		{`\N{BYTE ORDER MARK}`, "\ufeff"},
		{`\N{ZERO WIDTH JOINER}`, "\u200d"},
	}
	for _, test := range cases {
		expression, err := compilePythonRegex(test.pattern, 0, 0)
		if err != nil {
			t.Fatalf("%q: %v", test.pattern, err)
		}
		matched, err := expression.search(test.input, nil)
		if err != nil || !matched {
			t.Fatalf("%q = %v, %v", test.pattern, matched, err)
		}
	}
	for _, pattern := range []string{`\N{LATIN  SMALL LETTER A}`, `\N{  LATIN SMALL LETTER A  }`} {
		if _, err := compilePythonRegex(pattern, 0, 0); err == nil {
			t.Fatalf("accepted invalid name spacing %q", pattern)
		}
	}
}

func TestPythonRegexConditionalsAndBackrefs(t *testing.T) {
	expression, err := compilePythonRegex(`(a)?(?(1)b|c)`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := expression.search("ab", nil)
	if err != nil || !matched {
		t.Fatalf("conditional yes = %v, %v", matched, err)
	}
	if _, err := compilePythonRegex(`(?(?=a)b|c)`, 0, 0); err == nil {
		t.Fatal("assertion conditional accepted")
	}
	if _, err := compilePythonRegex(`\1(a)`, 0, 0); err == nil {
		t.Fatal("forward numeric backref accepted")
	}
	if _, err := compilePythonRegex(`(?P=a)(?P<a>.)`, 0, 0); err == nil {
		t.Fatal("forward named backref accepted")
	}
}

func TestPythonRegexTimeoutSanitized(t *testing.T) {
	initRegexTimeoutClock()
	expression, err := compilePythonRegex(`^(a+)+$`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Repeat("a", 40) + "!"
	matched, err := expression.search(input, newRegexEvalBudget())
	if matched {
		t.Fatal("pathological match returned true")
	}
	if !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("err = %v, want ErrSelectorLimit", err)
	}
	if strings.Contains(err.Error(), input) {
		t.Fatalf("timeout error leaked input: %v", err)
	}
}

func TestPythonRegexConcurrentSearch(t *testing.T) {
	initRegexTimeoutClock()
	expression, err := compilePythonRegex(`(?P<x>a+)b(?P=x)`, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			matched, err := expression.search("aaabaaa", nil)
			if err != nil || !matched {
				t.Errorf("concurrent search = %v, %v", matched, err)
			}
		}()
	}
	wait.Wait()
}

func TestPythonRegexPredicateLimit(t *testing.T) {
	var builder strings.Builder
	for index := 0; index < maxRegexPredicatesPerAST+1; index++ {
		if index > 0 {
			builder.WriteByte('/')
		}
		builder.WriteString(`best[format_id~="a"]`)
	}
	_, err := ParseSelector(builder.String())
	if !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("err = %v, want ErrSelectorLimit", err)
	}
}

func TestPythonRegexResourceLimits(t *testing.T) {
	if _, err := compilePythonRegex(strings.Repeat("a", maxRegexBytes+1), 2, 3); !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("source limit err = %v", err)
	}
	selectorSource := `best[format_id~="` + strings.Repeat("a", maxRegexBytes+1) + `"]`
	if _, err := ParseSelector(selectorSource); !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("selector regex source limit err = %v", err)
	}
	if _, err := compilePythonRegex(strings.Repeat(`\b`, 256), 2, 3); !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("translated limit err = %v", err)
	}
	expression, err := compilePythonRegex("a", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expression.search(strings.Repeat("a", maxRegexInputBytes+1), nil); !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("input limit err = %v", err)
	}
	budget := newRegexEvalBudget()
	budget.attempts = maxRegexAttemptsPerPlan
	if _, err := expression.search("a", budget); !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("attempt limit err = %v", err)
	}
	budget = newRegexEvalBudget()
	budget.inspectedBytes = maxRegexInspectedBytesPerPlan
	if _, err := expression.search("a", budget); !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("inspected-byte limit err = %v", err)
	}
}

func TestFilterPlainIntegerLiteralUsesPythonFloat(t *testing.T) {
	got, ok := parseFilterNumber("9007199254740993")
	if !ok || got.isInt {
		t.Fatalf("plain literal = %#v, %v; want float", got, ok)
	}
	if got.floating != 9007199254740992 {
		t.Fatalf("plain literal = %.0f, want Python float rounding", got.floating)
	}
}

func TestFilterNumberPinnedYB(t *testing.T) {
	got, ok := parseFilterNumber("1YB")
	if !ok || !got.isInt {
		t.Fatalf("parseFilterNumber(1YB) = %#v, %v", got, ok)
	}
	want, _ := new(big.Int).SetString("999999999999999983222784", 10)
	if got.integer.Cmp(want) != 0 {
		t.Fatalf("1YB = %s, want %s", got.integer, want)
	}
	got, ok = parseFilterNumber("1YiB")
	if !ok || !got.isInt {
		t.Fatalf("parseFilterNumber(1YiB) = %#v, %v", got, ok)
	}
	want, _ = new(big.Int).SetString("1208925819614629174706176", 10)
	if got.integer.Cmp(want) != 0 {
		t.Fatalf("1YiB = %s, want %s", got.integer, want)
	}
}
