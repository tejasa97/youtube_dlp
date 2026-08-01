package extractor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type jsonTransport struct {
	request  *http.Request
	status   int
	response string
}

type jsonBodyErrorTransport struct{ err error }

func (transport *jsonBodyErrorTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       jsonReadErrorBody{err: transport.err},
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (*jsonBodyErrorTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage")
}

type jsonReadErrorBody struct{ err error }

func (body jsonReadErrorBody) Read([]byte) (int, error) { return 0, body.err }

func (jsonReadErrorBody) Close() error { return nil }

func (transport *jsonTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.request = request
	request.Header.Set("X-Transport-Test", "yes")
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

func TestRequestJSONPreservesBodyCancellation(t *testing.T) {
	tests := []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		wantErr    error
	}{
		{
			name: "canceled",
			newContext: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Unix(0, 0))
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.newContext()
			defer cancel()
			transport := &jsonBodyErrorTransport{err: test.wantErr}
			var result struct{}
			err := RequestJSON(ctx, transport, http.MethodGet, "https://example.test/api", nil, nil, &result)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("RequestJSON() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
