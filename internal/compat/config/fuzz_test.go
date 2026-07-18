package config

import "testing"

func FuzzTokenize(f *testing.F) {
	for _, seed := range []string{"--output 'x y' # comment", `--header "X: a\\\"b"`, "line\\\ncontinued", "'unterminated"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		limits := DefaultLimits()
		limits.MaxTokens = 1_000
		limits.MaxTokenBytes = 64 << 10
		_, _ = Tokenize(input, "fuzz.conf", limits)
	})
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte("# coding: utf-8\n--output ok"))
	f.Add([]byte{0xff, 0xfe, '-', 0, '-', 0, 'x', 0})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		_, _ = Decode(input, "fuzz.conf")
	})
}

func FuzzExpandAliases(f *testing.F) {
	f.Add("name", "--output {0}", "value")
	f.Add("loop", "--loop", "")
	f.Fuzz(func(t *testing.T, name, template, argument string) {
		if len(name)+len(template)+len(argument) > 64<<10 {
			t.Skip()
		}
		origin := Token{Source: "fuzz.conf", Line: 1, Column: 1}
		limits := DefaultLimits()
		limits.MaxAliasTriggers = 8
		limits.MaxTokens = 1_000
		limits.MaxTokenBytes = 64 << 10
		_, _ = ExpandAliases(stringTokens([]string{"--alias", name, template, "--" + name, argument}, origin), limits)
	})
}
