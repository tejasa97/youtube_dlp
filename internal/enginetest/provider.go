// Package enginetest supplies cycle-free fake providers for root engine tests.
// It intentionally depends only on public provider contracts and metadata.
package enginetest

import (
	"context"
	"net/url"

	"github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/engine/value"
)

type Request struct {
	provider.Request
	Label string
}

type Provider struct {
	ProviderName string
	Host         string
	ExtractFunc  func(context.Context, Request) (provider.Extraction, error)
}

func (candidate Provider) Name() string { return candidate.ProviderName }

func (candidate Provider) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Hostname() == candidate.Host
}

func (candidate Provider) Extract(ctx context.Context, request Request) (provider.Extraction, error) {
	if candidate.ExtractFunc != nil {
		return candidate.ExtractFunc(ctx, request)
	}
	return provider.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(candidate.ProviderName)},
		value.Field{Key: "title", Value: value.String(request.Label)},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))), nil
}

var _ provider.Provider[Request] = Provider{}
