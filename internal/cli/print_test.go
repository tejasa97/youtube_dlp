package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestParsePrintRulesStagesShorthandAndSimulation(t *testing.T) {
	rules, err := parsePrintRules([]string{
		"title,id", "after_filter:%(title)s", "https://example.invalid:literal",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ytdlp.PrintRule{
		{Stage: ytdlp.PrintVideo, Template: "%(title)s\n%(id)s"},
		{Stage: ytdlp.PrintAfterFilter, Template: "%(title)s"},
		{Stage: ytdlp.PrintVideo, Template: "https://example.invalid:literal"},
	}
	if !reflect.DeepEqual(rules, want) || !printRulesImplySimulation(rules) {
		t.Fatalf("rules=%#v", rules)
	}
	later, err := parsePrintRules([]string{"before_dl:title"})
	if err != nil || printRulesImplySimulation(later) {
		t.Fatalf("later=%#v error=%v", later, err)
	}
	if _, err := parsePrintRules([]string{"video:"}); err == nil {
		t.Fatal("empty template accepted")
	}
}

func TestAppendLegacyPrintRulesOrderAndOptionalFields(t *testing.T) {
	rules := appendLegacyPrintRules(nil, true, true, true, true, true, true, true, true)
	fields := []string{"title", "id", "urls", "thumbnail", "description", "filename", "duration_string", "format"}
	if len(rules) != len(fields) {
		t.Fatalf("rules=%#v", rules)
	}
	for index, field := range fields {
		if rules[index].Stage != ytdlp.PrintVideo || !bytes.Contains([]byte(rules[index].Template), []byte(field)) {
			t.Fatalf("rule %d=%#v", index, rules[index])
		}
	}
	if rules[3].OmitIfMissing != "thumbnail" || rules[4].OmitIfMissing != "description" ||
		rules[6].OmitIfMissing != "duration_string" {
		t.Fatalf("optional rules=%#v", rules)
	}
}

func TestWritePrintOutputsOrdersChildrenBeforePlaylistAndHandlesFailures(t *testing.T) {
	result := ytdlp.Result{
		Entries: []ytdlp.Result{
			{Prints: []ytdlp.PrintOutput{{Stage: ytdlp.PrintVideo, Text: "one"}}},
			{Prints: []ytdlp.PrintOutput{{Stage: ytdlp.PrintVideo, Text: "two"}}},
		},
		Prints: []ytdlp.PrintOutput{{Stage: ytdlp.PrintPlaylist, Text: "playlist"}},
	}
	var output bytes.Buffer
	if err := writePrintOutputs(context.Background(), result, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "one\ntwo\nplaylist\n" {
		t.Fatalf("output=%q", output.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writePrintOutputs(ctx, result, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	if err := writePrintOutputs(context.Background(), result, shortPrintWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short-write error=%v", err)
	}
}

type shortPrintWriter struct{}

func (shortPrintWriter) Write(input []byte) (int, error) {
	if len(input) == 0 {
		return 0, nil
	}
	return len(input) - 1, nil
}

func FuzzParsePrintRules(f *testing.F) {
	for _, seed := range []string{"title", "video:%(title)s", "before_dl:id", "https://example.invalid:x", "video:"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		rules, err := parsePrintRules([]string{input})
		if err != nil {
			return
		}
		if len(rules) != 1 || rules[0].Template == "" {
			t.Fatalf("invalid successful parse: %#v", rules)
		}
	})
}
