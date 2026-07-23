package matchfilter

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
	"gopkg.in/yaml.v3"
)

func info() value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("x")},
		value.Field{Key: "title", Value: value.String("Example")},
		value.Field{Key: "duration", Value: value.Int(120)},
		value.Field{Key: "filesize", Value: value.Int(10 * 1024)},
		value.Field{Key: "aspect_ratio", Value: value.Float(1.5)},
		value.Field{Key: "uploader", Value: value.String("變態妍字幕版 太妍 тест")},
		value.Field{Key: "creator", Value: value.String("тест ' 123 ' тест--")},
		value.Field{Key: "description", Value: value.String("cats & dogs")},
		value.Field{Key: "playlist_id", Value: value.String("42")},
		value.Field{Key: "enabled", Value: value.Bool(true)},
		value.Field{Key: "disabled", Value: value.Bool(false)},
		value.Field{Key: "null_value", Value: value.Null()},
	))
}

func TestProgramORAndAnd(t *testing.T) {
	program, err := Parse([]string{"duration >= 90 & uploader = alice", "id=x"})
	if err != nil {
		t.Fatal(err)
	}
	if decision := program.Evaluate(info(), false); !decision.Pass {
		t.Fatalf("decision = %#v", decision)
	}
	program, _ = Parse([]string{"duration<90"})
	if decision := program.Evaluate(info(), false); decision.Pass || decision.Reason == "" {
		t.Fatalf("rejection = %#v", decision)
	}
}

func TestStringOperatorsNegationAndEscapes(t *testing.T) {
	tests := []struct {
		expression string
		want       bool
	}{
		{`description *= "cats \& dogs"`, true},
		{`description !*= cats`, false},
		{`description ! ^= dogs`, true},
		{`description $= dogs`, true},
		{`description !$= cats`, true},
		{`description != other`, true},
		{`description ! = "cats \& dogs"`, false},
		{`description ~= (?i)^CATS`, true},
		{`description !~= ^dogs`, true},
		{`creator = 'тест \' 123 \' тест--'`, true},
		{`uploader ^= 變態`, true},
		{`playlist_id = 42`, true},
		{`playlist_id > 41`, true},
		{`playlist_id < 43`, true},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			program, err := Parse([]string{test.expression})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := program.EvaluateContext(context.Background(), info(), EvaluationOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Pass != test.want {
				t.Fatalf("pass = %v, want %v", decision.Pass, test.want)
			}
		})
	}
}

func TestNoneInclusiveAndIncompleteFields(t *testing.T) {
	tests := []struct {
		name       string
		expression string
		options    EvaluationOptions
		want       bool
	}{
		{"missing fails", "missing > 0", EvaluationOptions{}, false},
		{"none inclusive compact", "missing >? 0", EvaluationOptions{}, true},
		{"none inclusive spaced", "null_value = ? value", EvaluationOptions{}, true},
		{"incomplete all", "missing > 0", EvaluationOptions{IncompleteAll: true}, true},
		{"incomplete named", "missing > 0", EvaluationOptions{IncompleteFields: fields("missing")}, true},
		{"other incomplete field", "missing > 0", EvaluationOptions{IncompleteFields: fields("other")}, false},
		{"missing unary", "!missing", EvaluationOptions{}, true},
		{"incomplete unary", "missing", EvaluationOptions{IncompleteFields: fields("missing")}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			program, err := Parse([]string{test.expression})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := program.EvaluateContext(context.Background(), info(), test.options)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Pass != test.want {
				t.Fatalf("pass = %v, want %v", decision.Pass, test.want)
			}
		})
	}
}

func TestUnaryPresentMissingAndIncomplete(t *testing.T) {
	for _, test := range []struct {
		expression string
		want       bool
	}{
		{"enabled", true}, {"!enabled", false},
		{"disabled", false}, {"!disabled", true},
		{"title", true}, {"!title", false},
		{"missing", false}, {"!missing", true},
		{"null_value", false}, {"!null_value", true},
	} {
		program, err := Parse([]string{test.expression})
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.expression, err)
		}
		if got := program.Evaluate(info(), false).Pass; got != test.want {
			t.Fatalf("%q = %v, want %v", test.expression, got, test.want)
		}
	}
}

func TestNumericFilesizeDurationAndDecimalCoercion(t *testing.T) {
	tests := []struct {
		expression string
		want       bool
	}{
		{"filesize > 5KiB", true},
		{"filesize = 10KiB", true},
		{"filesize >= 10K", true},
		{"filesize < 11KB", true},
		{"duration > 01:30", true},
		{"duration = 2min", true},
		{"duration <= 2 minutes", true},
		{"duration != 30", true},
		{"duration !< 30", true},
		{"duration !>= 120", false},
		{"aspect_ratio = 1.5", false},
		{"enabled = 1", true},
		{"disabled = 0", true},
	}
	for _, test := range tests {
		program, err := Parse([]string{test.expression})
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.expression, err)
		}
		decision, err := program.EvaluateContext(context.Background(), info(), EvaluationOptions{})
		if err != nil {
			t.Fatalf("Evaluate(%q): %v", test.expression, err)
		}
		if decision.Pass != test.want {
			t.Fatalf("%q = %v, want %v", test.expression, decision.Pass, test.want)
		}
	}
	for _, raw := range []string{"12junk", "NaN", "+Inf", "1e999", "-1KiB"} {
		if _, ok := parseNumericComparison(raw); ok {
			t.Fatalf("parseNumericComparison(%q) accepted invalid input", raw)
		}
	}
}

func TestNumericComparisonPreservesInt64Precision(t *testing.T) {
	metadata := value.NewInfo(value.NewObject(
		value.Field{Key: "filesize", Value: value.Int(9007199254740993)},
	))
	program, err := Parse([]string{"filesize = 9007199254740992"})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := program.EvaluateContext(context.Background(), metadata, EvaluationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Pass {
		t.Fatal("adjacent integers above 2^53 compared equal")
	}
}

func TestStringOperatorOnNumberIsEvaluationError(t *testing.T) {
	program, err := Parse([]string{"duration *= 2"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = program.EvaluateContext(context.Background(), info(), EvaluationOptions{})
	if !errors.Is(err, ErrEvaluation) {
		t.Fatalf("error = %v", err)
	}
	var evaluation *EvaluationError
	if !errors.As(err, &evaluation) || evaluation.Field != "duration" {
		t.Fatalf("evaluation error = %#v", evaluation)
	}
}

func TestEvaluationCancellationAndBounds(t *testing.T) {
	program, err := Parse([]string{"title ~= Example"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := program.EvaluateContext(ctx, info(), EvaluationOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	large := info()
	large.Set("title", value.String(strings.Repeat("x", maxEvaluatedStringBytes+1)))
	if _, err := program.EvaluateContext(context.Background(), large, EvaluationOptions{}); !errors.Is(err, ErrEvaluationLimit) {
		t.Fatalf("large scalar error = %v", err)
	}

	incomplete := make(map[string]struct{}, maxIncompleteFields+1)
	for index := 0; index <= maxIncompleteFields; index++ {
		incomplete[strings.Repeat("x", index+1)] = struct{}{}
	}
	if _, err := program.EvaluateContext(context.Background(), info(), EvaluationOptions{IncompleteFields: incomplete}); !errors.Is(err, ErrEvaluationLimit) {
		t.Fatalf("incomplete field error = %v", err)
	}
}

func TestErrorsHaveSpan(t *testing.T) {
	_, err := Parse([]string{"duration &"})
	var syntaxError *SyntaxError
	if !errors.As(err, &syntaxError) || syntaxError.Start != 9 {
		t.Fatalf("error = %#v, %v", syntaxError, err)
	}
	for _, input := range []string{
		"x ~= (", "bad-field=1", "", "-", "title ~= (?=Example)",
		"title = 'unterminated", "title !? Example", "ID = x", "id2 = x",
	} {
		if _, err := Parse([]string{input}); !errors.Is(err, ErrInvalidFilter) {
			t.Fatalf("Parse(%q) = %v", input, err)
		}
	}
}

type conformanceCorpus struct {
	Version int `yaml:"version"`
	Cases   []struct {
		Name             string   `yaml:"name"`
		Filters          []string `yaml:"filters"`
		IncompleteAll    bool     `yaml:"incomplete_all"`
		IncompleteFields []string `yaml:"incomplete_fields"`
		Pass             bool     `yaml:"pass"`
	} `yaml:"cases"`
}

func TestPinnedMatchFilterConformance(t *testing.T) {
	data, err := os.ReadFile("../../../conformance/compatibility/phase2/matchfilter.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var corpus conformanceCorpus
	if err := yaml.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.Version != 2 || len(corpus.Cases) == 0 {
		t.Fatalf("invalid conformance corpus: version=%d cases=%d", corpus.Version, len(corpus.Cases))
	}
	for _, test := range corpus.Cases {
		t.Run(test.Name, func(t *testing.T) {
			program, err := Parse(test.Filters)
			if err != nil {
				t.Fatal(err)
			}
			decision, err := program.EvaluateContext(context.Background(), info(), EvaluationOptions{
				IncompleteAll: test.IncompleteAll, IncompleteFields: fields(test.IncompleteFields...),
			})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Pass != test.Pass {
				t.Fatalf("filters %q pass = %v, want %v", test.Filters, decision.Pass, test.Pass)
			}
		})
	}
}

func fields(names ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}

func FuzzParse(f *testing.F) {
	f.Add("duration>=3&title~=x")
	f.Add(`description *= "cats \& dogs"`)
	f.Add("missing >? 1")
	f.Add("!")
	f.Fuzz(func(t *testing.T, input string) {
		program, err := Parse([]string{input})
		if err != nil {
			return
		}
		first, firstErr := program.EvaluateContext(context.Background(), info(), EvaluationOptions{})
		second, secondErr := program.EvaluateContext(context.Background(), info(), EvaluationOptions{})
		if (firstErr == nil) != (secondErr == nil) || first != second {
			t.Fatalf("non-deterministic evaluation: (%#v, %v), (%#v, %v)", first, firstErr, second, secondErr)
		}
	})
}
