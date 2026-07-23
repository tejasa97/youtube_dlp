package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestRenderSubtitleListingOrderAndNoTracks(t *testing.T) {
	raw := json.RawMessage(`{"id":"video-1","automatic_captions":{"es":[{"ext":"vtt","name":"Spanish"}],"pt":[{"ext":"srv3"}]},"subtitles":{"en":[{"ext":"srt"},{"ext":"vtt"}],"fr":[{"ext":"vtt","name":"French"}]}}`)
	stdout, stderr, err := renderSubtitleListing(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	wantTable := "Language  Name     Formats\n" +
		"es        Spanish  vtt    \n" +
		"pt                 srv3   \n" +
		"Language  Name    Formats \n" +
		"en                vtt, srt\n" +
		"fr        French  vtt     \n"
	if stdout != wantTable {
		t.Fatalf("table = %q\nwant  = %q", stdout, wantTable)
	}
	if stderr != "[info] Available automatic captions for video-1:\n[info] Available subtitles for video-1:\n" {
		t.Fatalf("status = %q", stderr)
	}

	stdout, stderr, err = renderSubtitleListing(context.Background(), json.RawMessage(`{"id":"empty","automatic_captions":{},"subtitles":null}`))
	if err != nil || stdout != "" || stderr != "empty has no automatic captions\nempty has no subtitles\n" {
		t.Fatalf("empty stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

func TestRenderSubtitleListingRejectsMalformedAndBounds(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"id":"bad","subtitles":{"en":{}}}`),
		json.RawMessage(`{"id":"bad","subtitles":{"en":[{"ext":1}]}}`),
		json.RawMessage(`{"id":"bad","subtitles":{"en":[{"ext":"vtt","name":false}]}}`),
		json.RawMessage(`[]`),
		json.RawMessage(`{"id":"bad","subtitles":{}} {}`),
		json.RawMessage(`{"subtitles":{"en":[` + strings.Repeat(`{"ext":"vtt"},`, maxSubtitleListTracks) + `{"ext":"vtt"}]}}`),
	} {
		if _, _, err := renderSubtitleListing(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "invalid subtitle metadata") {
			t.Fatalf("raw %q error=%v", raw, err)
		}
	}
	tooLarge := json.RawMessage(`{"id":"` + strings.Repeat("x", maxSubtitleListJSON) + `"}`)
	if _, _, err := renderSubtitleListing(context.Background(), tooLarge); err == nil {
		t.Fatal("oversized InfoJSON succeeded")
	}
}

func TestRenderSubtitleListingUsesRuneWidths(t *testing.T) {
	stdout, _, err := renderSubtitleListing(context.Background(), json.RawMessage(`{"id":"unicode","subtitles":{"语言":[{"ext":"vtt"}],"en":[{"ext":"vtt"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines=%q", stdout)
	}
	left := func(line string) int {
		prefix, _, found := strings.Cut(line, "vtt")
		if !found {
			t.Fatalf("format column missing: %q", line)
		}
		return utf8.RuneCountInString(prefix)
	}
	if left(lines[1]) != left(lines[2]) {
		t.Fatalf("format columns do not align by runes: %q", stdout)
	}
}

func TestRenderSubtitleListingCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := renderSubtitleListing(ctx, json.RawMessage(`{"id":"x","subtitles":{}}`)); err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
}

type cancelAfterContext struct{ calls, cancelAt int }

func (ctx *cancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelAfterContext) Done() <-chan struct{}       { return nil }
func (ctx *cancelAfterContext) Value(any) any               { return nil }
func (ctx *cancelAfterContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestRenderSubtitleListingChecksCancellationWithinTracks(t *testing.T) {
	ctx := &cancelAfterContext{cancelAt: 4}
	_, _, err := renderSubtitleListing(ctx, json.RawMessage(`{"id":"x","subtitles":{"en":[{"ext":"vtt"},{"ext":"srt"}]}}`))
	if err != context.Canceled {
		t.Fatalf("error=%v calls=%d", err, ctx.calls)
	}
}

func TestWriteSubtitleListingsUsesPlaylistEntries(t *testing.T) {
	result := ytdlp.Result{
		InfoJSON: json.RawMessage(`{"id":"parent","subtitles":{"parent":[{"ext":"vtt"}]}}`),
		Entries: []ytdlp.Result{
			{InfoJSON: json.RawMessage(`{"id":"one","subtitles":{"en":[{"ext":"vtt"}]}}`)},
			{InfoJSON: json.RawMessage(`{"id":"two","subtitles":{}}`)},
		},
	}
	var stdout, stderr bytes.Buffer
	if err := writeSubtitleListings(context.Background(), result, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "parent") || !strings.Contains(stdout.String(), "en") ||
		!strings.Contains(stderr.String(), "two has no subtitles") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func FuzzRenderSubtitleListing(f *testing.F) {
	f.Add([]byte(`{"id":"x","subtitles":{"en":[{"ext":"vtt"}]}}`))
	f.Add([]byte(`{"subtitles":null}`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxSubtitleListJSON+1 {
			t.Skip()
		}
		_, _, _ = renderSubtitleListing(context.Background(), json.RawMessage(raw))
	})
}
