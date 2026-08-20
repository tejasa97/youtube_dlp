package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/tejasa97/ytdlp-go/internal/extractor"
)

var (
	errNiconicoMediaHost     = errors.New("niconico media host rejected")
	errNiconicoMediaResponse = errors.New("niconico media response rejected")
	errNiconicoMediaStatus   = errors.New("niconico media status rejected")
	errNiconicoMediaRedirect = errors.New("niconico media redirect rejected")
)

type niconicoMediaStatusError struct {
	status int
}

func (err *niconicoMediaStatusError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.status >= 300 && err.status < 400 {
		return "niconico media redirect rejected"
	}
	return "niconico media response rejected"
}

func (err *niconicoMediaStatusError) Unwrap() error {
	if err == nil {
		return nil
	}
	if err.status >= 300 && err.status < 400 {
		return errNiconicoMediaRedirect
	}
	return errNiconicoMediaStatus
}

func (err *niconicoMediaStatusError) StatusCode() int {
	if err == nil {
		return 0
	}
	return err.status
}

// niconicoMediaTransport keeps generic HLS parsing/download useful while
// preserving the extractor's role-specific media host boundary.  The wrapped
// transport remains the credential-isolated no-redirect implementation.
type niconicoMediaTransport struct {
	base Transport
}

func newNiconicoMediaTransport(base Transport) *niconicoMediaTransport {
	return &niconicoMediaTransport{base: base}
}

func (transport *niconicoMediaTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || !extractor.NiconicoMediaURLAllowed(request.URL.String()) {
		return nil, errNiconicoMediaHost
	}
	response, err := transport.base.Do(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, errNiconicoMediaResponse
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		return nil, &niconicoMediaStatusError{status: response.StatusCode}
	}
	return response, nil
}

func (transport *niconicoMediaTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if !extractor.NiconicoMediaURLAllowed(rawURL) {
		return nil, nil, errNiconicoMediaHost
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: create media request", errNiconicoMediaResponse)
	}
	response, err := transport.Do(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	if err != nil {
		return nil, response.Header.Clone(), fmt.Errorf("%w: read media response", errNiconicoMediaResponse)
	}
	if len(body) > 16<<20 {
		return nil, response.Header.Clone(), fmt.Errorf("%w: media manifest exceeds size limit", errNiconicoMediaResponse)
	}
	return body, response.Header.Clone(), nil
}
