package extraction

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type requestTestTransport struct{}

func (*requestTestTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (*requestTestTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, nil
}

type requestTestCredentials struct{}

func (*requestTestCredentials) Lookup(context.Context, string) (Credential, bool, error) {
	return Credential{}, false, nil
}

func TestRequestOwnsNeutralStateAndRedactsDiagnostics(t *testing.T) {
	transport := &requestTestTransport{}
	credentials := &requestTestCredentials{}
	request := Request{
		URL:                 "https://user:secret@example.test/video",
		SearchQueryOverride: "private query",
		Referer:             "https://referrer.test/embed",
		Transport:           transport,
		Credentials:         credentials,
		VideoPassword:       "video-secret",
		NoPlaylist:          true,
	}
	if request.ExtractionURL() != request.URL || request.Transport != transport || request.Credentials != credentials || !request.NoPlaylist {
		t.Fatalf("neutral request state was not retained: %#v", request)
	}
	for _, rendered := range []string{fmt.Sprint(request), fmt.Sprintf("%+v", request), fmt.Sprintf("%#v", request)} {
		for _, secret := range []string{"user", "secret", "private query", "referrer", "video-secret", "requestTestTransport", "requestTestCredentials"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("formatted request %q contains %q", rendered, secret)
			}
		}
	}
}
