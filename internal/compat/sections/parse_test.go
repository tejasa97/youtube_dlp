package sections

import (
	"math"
	"testing"
)

func TestParseBasicRanges(t *testing.T) {
	program, err := Parse([]string{"*10:15-15:00", "*0-inf", "*from-url"})
	if err != nil {
		t.Fatal(err)
	}
	if !program.FromURL {
		t.Fatal("FromURL not set")
	}
	if len(program.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(program.Sections))
	}
	if program.Sections[0].Start != 615 || program.Sections[0].End == nil || *program.Sections[0].End != 900 {
		t.Fatalf("section 0 = %#v", program.Sections[0])
	}
	if program.Sections[1].Start != 0 || program.Sections[1].End != nil {
		t.Fatalf("section 1 = %#v", program.Sections[1])
	}
}

func TestParseRejectsUnsupported(t *testing.T) {
	for _, spec := range []string{"10:15-15:00", "10:15", "*10:15+15:00", "*", "* -", "*10:15-5:00"} {
		if _, err := Parse([]string{spec}); err == nil {
			t.Fatalf("Parse(%q) succeeded, want error", spec)
		}
	}
}

func TestParseRejectsBounds(t *testing.T) {
	// end <= start is rejected.
	if _, err := Parse([]string{"*10:15-10:15"}); err == nil {
		t.Fatal("equal bounds accepted")
	}
	// negative start rejected.
	if _, err := Parse([]string{"*-5-10"}); err == nil {
		t.Fatal("negative start accepted")
	}
	// NaN rejected.
	if _, err := Parse([]string{"*NaN-10"}); err == nil {
		t.Fatal("NaN start accepted")
	}
}

func TestParseOpenEnded(t *testing.T) {
	program, err := Parse([]string{"*1:00-inf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(program.Sections) != 1 || program.Sections[0].End != nil {
		t.Fatalf("open-ended section = %#v", program.Sections)
	}
	if program.Sections[0].Start != 60 {
		t.Fatalf("start = %v, want 60", program.Sections[0].Start)
	}
}

func TestParseLimits(t *testing.T) {
	// Too many specs.
	if _, err := Parse(make([]string, MaxSpecifications+1)); err == nil {
		t.Fatal("over-max specifications accepted")
	}
	// Oversized payload.
	if _, err := Parse([]string{"*" + string(make([]byte, MaxSpecificationBytes+1)) + "-1"}); err == nil {
		t.Fatal("oversized payload accepted")
	}
}

func TestParseUnitAndColon(t *testing.T) {
	program, err := Parse([]string{"*1h30m-2h", "*90s-2m"})
	if err != nil {
		t.Fatal(err)
	}
	if program.Sections[0].Start != 5400 || *program.Sections[0].End != 7200 {
		t.Fatalf("section[0] = %#v", program.Sections[0])
	}
	if program.Sections[1].Start != 90 || *program.Sections[1].End != 120 {
		t.Fatalf("section[1] = %#v", program.Sections[1])
	}
	_ = math.Inf
}

// FuzzParse ensures the section planner never returns unsafe bounds (negative
// starts, non-positive ranges, or unvalidated open-ended sections) for any
// input, and that successful parses always satisfy the section contract.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{"*10:15-15:00", "*0-inf", "*from-url", "*1h30m-2h", "*bad", "10:15", "*10:15+15:00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, specification string) {
		program, err := Parse([]string{specification})
		if err != nil {
			return
		}
		for _, section := range program.Sections {
			if math.IsNaN(section.Start) || math.IsInf(section.Start, 0) || section.Start < 0 {
				t.Fatalf("unsafe section start %#v", section)
			}
			if section.End != nil && (math.IsNaN(*section.End) || math.IsInf(*section.End, 0) || *section.End <= section.Start) {
				t.Fatalf("unsafe section end %#v", section)
			}
		}
	})
}

// TestParseRejectsExcessPrecision verifies the documented precision contract:
// values with more than MaxFractionalDigits fractional digits, scientific
// notation, and the spelled-out "infinite" form are rejected rather than
// silently rounded into ffmpeg arguments.
func TestParseRejectsExcessPrecisionAndSyntax(t *testing.T) {
	for _, spec := range []string{
		"*0-0.0004",   // 4 fractional digits -> rounds "-t 0"
		"*0-1e3",      // scientific notation
		"*1e1-2e1",    // scientific notation
		"*0-infinite", // spelled-out infinity outside the grammar
		"*.5-1.5",     // leading dot
	} {
		if _, err := Parse([]string{spec}); err == nil {
			t.Fatalf("Parse(%q) succeeded, want error", spec)
		}
	}
}

// TestParseAcceptsThreeFractionalDigits verifies the accepted grammar keeps
// the 3-fractional-digit precision that ffmpeg argument generation preserves.
func TestParseAcceptsThreeFractionalDigits(t *testing.T) {
	program, err := Parse([]string{"*0-0.125"})
	if err != nil {
		t.Fatal(err)
	}
	if len(program.Sections) != 1 || program.Sections[0].End == nil || *program.Sections[0].End != 0.125 {
		t.Fatalf("sections = %#v", program.Sections)
	}
}

// TestParseRejectsInfinityOutsideOpenEnd verifies only the literal "inf"
// end marker is accepted; any other infinity/scientific form is rejected.
func TestParseRejectsOtherInfinityForms(t *testing.T) {
	if _, err := Parse([]string{"*0-inf"}); err != nil {
		t.Fatalf("*0-inf should parse: %v", err)
	}
	for _, spec := range []string{"*0-infinite", "*0-Infinity", "*0-1e999"} {
		if _, err := Parse([]string{spec}); err == nil {
			t.Fatalf("Parse(%q) succeeded, want error", spec)
		}
	}
}
