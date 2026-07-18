package matchfilter

import (
	"errors"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func info() value.Info {
	return value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String("x")}, value.Field{Key: "title", Value: value.String("Example")}, value.Field{Key: "duration", Value: value.Int(120)}, value.Field{Key: "uploader", Value: value.String("alice")}))
}
func TestProgramORAndAnd(t *testing.T) {
	p, err := Parse([]string{"duration >= 90 & uploader = alice", "id=other"})
	if err != nil {
		t.Fatal(err)
	}
	if decision := p.Evaluate(info(), false); !decision.Pass {
		t.Fatalf("decision = %#v", decision)
	}
	p, _ = Parse([]string{"duration<90"})
	if decision := p.Evaluate(info(), false); decision.Pass || decision.Reason == "" {
		t.Fatalf("rejection = %#v", decision)
	}
}
func TestIncompleteAndRegex(t *testing.T) {
	p, err := Parse([]string{"missing > 0", "title ~= ^Ex"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Evaluate(info(), false).Pass {
		t.Fatal("second OR expression should pass")
	}
	p, _ = Parse([]string{"missing > 0"})
	if !p.Evaluate(info(), true).Pass {
		t.Fatal("incomplete missing value should pass")
	}
}
func TestErrorsHaveSpan(t *testing.T) {
	_, err := Parse([]string{"duration &"})
	var syntax *SyntaxError
	if !errors.As(err, &syntax) || syntax.Start != 9 {
		t.Fatalf("error = %#v, %v", syntax, err)
	}
	for _, input := range []string{"x ~= (", "bad-field=1", ""} {
		if _, err := Parse([]string{input}); !errors.Is(err, ErrInvalidFilter) {
			t.Fatalf("Parse(%q) = %v", input, err)
		}
	}
}
func FuzzParse(f *testing.F) {
	f.Add("duration>=3&title~=x")
	f.Add("!")
	f.Fuzz(func(t *testing.T, input string) {
		p, err := Parse([]string{input})
		if err == nil {
			_ = p.Evaluate(info(), false)
		}
	})
}
