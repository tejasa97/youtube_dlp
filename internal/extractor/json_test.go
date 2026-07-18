package extractor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type jsonTransport struct {
	request  *http.Request
	status   int
	response string
}

func (transport *jsonTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.request = request
	return &http.Response{
		StatusCode: transport.status,
		Body:       io.NopCloser(strings.NewReader(transport.response)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (*jsonTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage")
}

func TestRequestJSONBoundsStatusAndSyntax(t *testing.T) {
	transport := &jsonTransport{status: http.StatusOK, response: `{"ok":true}`}
	var result struct {
		OK bool `json:"ok"`
	}
	err := RequestJSON(context.Background(), transport, http.MethodPost, "https://example.test/api", []byte(`{"input":1}`), http.Header{"X-Test": {"yes"}}, &result)
	if err != nil || !result.OK || transport.request.Method != http.MethodPost || transport.request.Header.Get("X-Test") != "yes" {
		t.Fatalf("result=%#v request=%#v error=%v", result, transport.request, err)
	}
	transport.status = http.StatusForbidden
	if err := RequestJSON(context.Background(), transport, http.MethodGet, "https://example.test/api", nil, nil, &result); !errors.As(err, new(*HTTPStatusError)) {
		t.Fatalf("status error = %v", err)
	}
	transport.status, transport.response = http.StatusOK, `{} {}`
	if err := RequestJSON(context.Background(), transport, http.MethodGet, "https://example.test/api", nil, nil, &result); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("syntax error = %v", err)
	}
	transport.response = strings.Repeat(" ", int(maxExtractorJSONBytes)+1)
	if err := RequestJSON(context.Background(), transport, http.MethodGet, "https://example.test/api", nil, nil, &result); !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("size error = %v", err)
	}
}
