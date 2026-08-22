package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/network"
)

const youtubeFormatFidelityProductURL = "https://www.youtube.com/watch?v=fixture0003"

type youtubeFormatFidelityRoundTripper struct {
	page []byte
}

func (transport *youtubeFormatFidelityRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.URL.String() != youtubeFormatFidelityProductURL {
		return nil, errors.New("unexpected product request: " + request.URL.String())
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/html"}},
		Body:       io.NopCloser(bytes.NewReader(transport.page)),
		Request:    request,
	}, nil
}

func TestProductYouTubeFormatFidelityListFormats(t *testing.T) {
	page, err := os.ReadFile("../conformance/extractors/youtube_format_fidelity/watch.html")
	if err != nil {
		t.Fatal(err)
	}
	client := newBroadTestClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = &youtubeFormatFidelityRoundTripper{page: page}
		return network.New(config)
	}))
	result, err := client.Run(context.Background(), Request{
		URL:        youtubeFormatFidelityProductURL,
		Simulate:   true,
		PrintRules: []PrintRule{{Stage: PrintPreProcess, Template: "%(formats_table)s"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Prints) == 0 || result.Prints[0].Text == "" {
		t.Fatalf("prints = %#v", result.Prints)
	}
	table := result.Prints[0].Text
	for _, want := range []string{
		"ID", "RESOLUTION", "HDR", "CH", "MORE INFO",
		"640x360", "1920x1080", "HDR", "601-drc", "272-sr",
		"English, (default), medium", "1080p Premium",
	} {
		if !strings.Contains(table, want) {
			t.Fatalf("list-formats output missing %q:\n%s", want, table)
		}
	}
	if strings.Contains(table, "audio only") == false || !strings.Contains(table, "m4a") {
		t.Fatalf("list-formats output missing audio-only rows:\n%s", table)
	}
}
