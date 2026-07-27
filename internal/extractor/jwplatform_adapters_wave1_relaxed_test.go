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

func TestJWWave1JSToJSONCombinesCommentsWithTrailingCommas(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "object trailing comma after line comment",
			input: "{a:1, // trailing\n}",
			want:  `{"a":1}`,
		},
		{
			name:  "array trailing comma after block comment",
			input: `[1, /* tail */]`,
			want:  `[1]`,
		},
		{
			name:  "nested trailing comma inside object and array",
			input: `{outer: {a:1,}, arr: [1,2,],}`,
			want:  `{"outer":{"a":1},"arr":[1,2]}`,
		},
		{
			name:  "comma only consumed past block comment",
			input: `{a:/* keep this id */ 'fixture0001', /* drop */}`,
			want:  `{"a":"fixture0001"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := jwWave1JSToJSON([]byte(test.input))
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if string(got) != test.want {
				t.Fatalf("got=%s want=%s", got, test.want)
			}
		})
	}
}

func TestJWWave1JSToJSONOutputBound(t *testing.T) {
	t.Parallel()
	// `\xNN` consumes 4 input bytes and emits `\u00NN` (6 output bytes), a
	// 1.5x expansion. Build an input just under the source cap whose decoded
	// output therefore exceeds the output cap, ensuring the bounded writer
	// rejects without producing an oversized buffer.
	repeats := int(maxExtractorJSONBytes/10) + 1
	var builder strings.Builder
	builder.WriteByte('{')
	for i := 0; i < repeats; i++ {
		builder.WriteString(`a:`)
	}
	builder.WriteByte('\'')
	// Append escape sequences. The `\x` itself writes `\u00`; the two
	// following hex digits become literal output characters.
	for i := 0; i < repeats; i++ {
		builder.WriteString(`\x41`)
	}
	builder.WriteByte('\'')
	builder.WriteByte('}')
	src := []byte(builder.String())
	if int64(len(src)) >= maxExtractorJSONBytes {
		t.Fatalf("test source breached source cap; recalibrate constants (len=%d)", len(src))
	}
	out, err := jwWave1JSToJSON(src)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("output-bound err=%v out=%d bytes", err, len(out))
	}
	if int64(len(out)) > maxExtractorJSONBytes {
		t.Fatalf("oversized output returned (len=%d, cap=%d)", len(out), maxExtractorJSONBytes)
	}
}

// TestJWWave1JSToJSONEscapesNearCap asserts that an escape-driven expansion
// bringing the buffer close to the cap causes a later primitive token or
// closing delimiter to fail without overshooting. Each `\x41` produces 6
// output bytes from 4 input bytes (1.5x); the trailing comma+key+value
// expansion must trip the bound.
func TestJWWave1JSToJSONEscapesNearCap(t *testing.T) {
	t.Parallel()
	// Choose repeats so that escape-driven output is close to but below
	// the cap, and the trailing primitive + delimiter pushes it over.
	// 6*repeats + structural padding + trailing write ≤ cap < 6*repeats + padding + trailing write.
	repeats := int(maxExtractorJSONBytes/6) - 2
	var builder strings.Builder
	builder.WriteByte('{')
	builder.WriteString(`a:'`)
	for i := 0; i < repeats; i++ {
		builder.WriteString(`\x41`)
	}
	builder.WriteString(`',b:12345678901234567890}`)
	src := []byte(builder.String())
	if int64(len(src)) >= maxExtractorJSONBytes {
		t.Fatalf("source exceeds source cap: len=%d cap=%d", len(src), maxExtractorJSONBytes)
	}
	out, err := jwWave1JSToJSON(src)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v out=%d bytes", err, len(out))
	}
	if int64(len(out)) > maxExtractorJSONBytes {
		t.Fatalf("oversized output (len=%d, cap=%d)", len(out), maxExtractorJSONBytes)
	}
}

// TestJWWave1BoundedWriterCapsInitialCapacity asserts the bounded writer
// constructor never asks bytes.Buffer.Grow for an initial capacity above the
// output cap. Even when the source is at the source cap, the constructor
// must clamp the requested growth before delegating to bytes.Buffer.Grow.
func TestJWWave1BoundedWriterCapsInitialCapacity(t *testing.T) {
	t.Parallel()
	// Direct check: an over-cap hint must not over-allocate. bytes.Buffer.Grow
	// rounds up to powers of two internally, but the constructor's own
	// clamp must keep the request at or below the output cap so the
	// allocation itself does not exceed the promised bound.
	bounded := newJWWave1BoundedWriter(int(maxExtractorJSONBytes) + 16)
	if bounded.buf.Cap() > int(maxExtractorJSONBytes) {
		t.Fatalf("constructor over-allocated: cap=%d, allowed=%d",
			bounded.buf.Cap(), maxExtractorJSONBytes)
	}
	// An exact-cap hint must produce at most the cap.
	atCap := newJWWave1BoundedWriter(int(maxExtractorJSONBytes))
	if atCap.buf.Cap() > int(maxExtractorJSONBytes) {
		t.Fatalf("at-cap constructor over-allocated: cap=%d, allowed=%d",
			atCap.buf.Cap(), maxExtractorJSONBytes)
	}

	// Drive the converter with an at-cap source via the public API. The
	// converter passes len(src)+16 = cap+16 to the constructor; the
	// constructor must clamp that to the cap. jwWave1JSToJSON itself
	// rejects the at-cap source via its own size check, but the
	// constructor (invoked first) is the unit under test.
	atCapSource := make([]byte, maxExtractorJSONBytes)
	atCapSource[0] = '{'
	for i := 1; i < int(maxExtractorJSONBytes); i++ {
		atCapSource[i] = byte('a' + (i % 200))
	}
	if int64(len(atCapSource)) != maxExtractorJSONBytes {
		t.Fatalf("source size=%d want %d", len(atCapSource), maxExtractorJSONBytes)
	}
	if _, err := jwWave1JSToJSON(atCapSource); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("at-cap source err=%v", err)
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
