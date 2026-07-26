package extractor

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestJWWave1JSToJSONSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{
			name:  "unquoted keys",
			input: `{state: {articles: [{items: {canonical_title: 'Fixture'}}]}}`,
			want:  `{"state":{"articles":[{"items":{"canonical_title":"Fixture"}}]}}`,
		},
		{
			name:  "single quotes and trailing commas",
			input: `{items: [{id: 'AbCd1234',},], extra: undefined, void: void 0}`,
			want:  `{"items":[{"id":"AbCd1234"}],"extra":null,"void":null}`,
		},
		{
			name:  "escaped apostrophe",
			input: `{title: 'It\'s fine'}`,
			want:  `{"title":"It's fine"}`,
		},
		{
			name:  "escaped backslash",
			input: `{title: 'back\\slash'}`,
			want:  `{"title":"back\\slash"}`,
		},
		{
			name:  "hex escape",
			input: `{title: '\x41'}`,
			want:  `{"title":"\u0041"}`,
		},
		{
			name:  "unicode escape passthrough",
			input: `{title: '\u0041'}`,
			want:  `{"title":"\u0041"}`,
		},
		{
			name:  "control escapes",
			input: `{title: 'tab\there\nline'}`,
			want:  `{"title":"tab\there\nline"}`,
		},
		{
			name:  "line continuation",
			input: "{title: 'line\\\ncont'}",
			want:  `{"title":"linecont"}`,
		},
		{
			name:  "double quote inside single quoted string",
			input: `{title: 'say "hello"'}`,
			want:  `{"title":"say \"hello\""}`,
		},
		{
			name:  "block comment",
			input: `{/* comment */title: 'Fixture'}`,
			want:  `{"title":"Fixture"}`,
		},
		{
			name:    "malformed string",
			input:   `{title: 'unterminated}`,
			wantErr: ErrInvalidMetadata,
		},
		{
			name:    "malformed escape at end",
			input:   "{title: 'broken\\",
			wantErr: ErrInvalidMetadata,
		},
		{
			name:    "unterminated block comment",
			input:   `{/* never closed title: 'x'}`,
			wantErr: ErrInvalidMetadata,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := jwWave1JSToJSON([]byte(test.input))
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("err=%v want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("got=%s want=%s", got, test.want)
			}
			var decoded any
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatalf("output is not valid JSON: %v (%s)", err, got)
			}
		})
	}
}

func TestJWWave1JSToJSONBounds(t *testing.T) {
	t.Parallel()
	oversized := "{" + strings.Repeat("a", int(maxExtractorJSONBytes)+1) + ":1}"
	if _, err := jwWave1JSToJSON([]byte(oversized)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized=%v", err)
	}
}

func TestIltalehtiRelaxedAppState(t *testing.T) {
	t.Parallel()
	page := jwWave1Fixture(t, "iltalehti_relaxed_page.html")
	title, mediaIDs, err := iltalehtiArticle(page)
	if err != nil || title != "Fixture Article" || len(mediaIDs) != 2 || mediaIDs[0] != "AbCd1234" || mediaIDs[1] != "EfGh5678" {
		t.Fatalf("title=%q ids=%v err=%v", title, mediaIDs, err)
	}
}
