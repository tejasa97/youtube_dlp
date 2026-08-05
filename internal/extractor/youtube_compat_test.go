package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
)

const (
	youtubeFixtureURL = "https://www.youtube.com/watch?v=fixture0001"
	youtubePlayerURL  = "https://www.youtube.com/s/player/fixture/base.js"
)

type memoryTransport struct {
	pages map[string][]byte
	reads []string
}

func (*memoryTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected Do call")
}

func (transport *memoryTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	transport.reads = append(transport.reads, rawURL)
	page, ok := transport.pages[rawURL]
	if !ok {
		return nil, nil, fmt.Errorf("unexpected URL %q", rawURL)
	}
	return append([]byte(nil), page...), make(http.Header), nil
}

type youtubeTestHelper interface {
	Helper()
	Fatal(...any)
}

func readYouTubeFixture(t youtubeTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var _ youtubeTestHelper = (*testing.T)(nil)
