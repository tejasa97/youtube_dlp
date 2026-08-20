package extractor

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

func nhkValidPublicURL(raw string) bool {
	return strictValidHostedHTTPURL(raw)
}

func nhkFetchIsolatedBytes(ctx context.Context, transport Transport, rawURL string, maxBytes int64) ([]byte, error) {
	if !nhkValidPublicURL(rawURL) {
		return nil, fmt.Errorf("%w: unsafe NHK media URL", ErrInvalidMetadata)
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NHK media request", ErrInvalidMetadata)
	}
	resp, err := isolated.DoWithoutCredentialsNoRedirect(ctx, req)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("%w: empty NHK media response", ErrInvalidMetadata)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nhkCategorizeStatus(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: NHK media response too large", ErrInvalidMetadata)
	}
	return data, nil
}

func nhkCredentialIsolateFormats(formats []value.Value) []value.Value {
	for i, format := range formats {
		object, ok := format.Object()
		if !ok || object == nil {
			continue
		}
		object.Set("_credential_isolated", value.Bool(true))
		formats[i] = value.ObjectValue(object)
	}
	return formats
}

func nhkCredentialIsolateSubtitleObject(object *value.Object) *value.Object {
	if object == nil {
		return object
	}
	for _, field := range object.Fields() {
		entries, ok := field.Value.ListValue()
		if !ok {
			continue
		}
		for i, entry := range entries {
			entryObject, ok := entry.Object()
			if !ok || entryObject == nil {
				continue
			}
			entryObject.Set("_credential_isolated", value.Bool(true))
			entries[i] = value.ObjectValue(entryObject)
		}
		object.Set(field.Key, value.List(entries...))
	}
	return object
}
