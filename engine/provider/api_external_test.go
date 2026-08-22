package provider_test

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"testing"

	"github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/engine/value"
)

type fixtureConfig struct{ Label string }

type fixtureRequest struct {
	provider.Request
	Label string
}

type fixtureProvider struct{ name string }

func (candidate fixtureProvider) Name() string { return candidate.name }
func (fixtureProvider) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Hostname() == "provider.example"
}
func (candidate fixtureProvider) Extract(_ context.Context, request fixtureRequest) (provider.Extraction, error) {
	return provider.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(candidate.name)},
		value.Field{Key: "title", Value: value.String(request.Label)},
	))), nil
}

func TestExternalPackageComposesTypedBundle(t *testing.T) {
	classified := errors.New("classified")
	hooks := provider.Hooks[fixtureConfig]{
		ClassifyError: func(err error) (provider.ErrorClass, bool) {
			return provider.ErrorNetwork, errors.Is(err, classified)
		},
		ValidateURL:   func(provider.URLPolicyRequest) error { return nil },
		NetworkError:  func(string) error { return classified },
		StatusError:   func(provider.StatusErrorRequest) error { return classified },
		ValidateAsset: func(provider.URLPolicyRequest) error { return nil },
		Reload: func(context.Context, provider.Operation, fixtureConfig, provider.ReloadRequest) (provider.Extraction, error) {
			return provider.Extraction{}, nil
		},
	}
	bundle := provider.Compose(
		func(fixtureConfig) []provider.Provider[fixtureRequest] {
			return []provider.Provider[fixtureRequest]{fixtureProvider{name: "focused"}}
		},
		func(operation provider.Operation, configuration fixtureConfig) fixtureRequest {
			return fixtureRequest{Request: operation.Request, Label: configuration.Label}
		},
		hooks,
	)

	runtime, err := bundle.NewRuntime(fixtureConfig{Label: "typed configuration"})
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.Names(); !reflect.DeepEqual(got, []string{"focused"}) {
		t.Fatalf("names = %v", got)
	}
	selected, err := runtime.Select("https://provider.example/watch")
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := selected.Extract(context.Background(), provider.Operation{
		Request: provider.Request{URL: "https://provider.example/watch"},
	}, fixtureConfig{Label: "typed configuration"})
	if err != nil {
		t.Fatal(err)
	}
	if title, ok := extracted.Info.Title(); !ok || title != "typed configuration" {
		t.Fatalf("title=%q ok=%v", title, ok)
	}
	if class, ok := runtime.Hooks().ClassifyError(classified); !ok || class != provider.ErrorNetwork {
		t.Fatalf("classification=%q ok=%v", class, ok)
	}
	if runtime.Hooks().ValidateURL == nil || runtime.Hooks().ValidateAsset == nil || runtime.Hooks().Reload == nil {
		t.Fatal("composition support hooks were not retained")
	}
}

func TestZeroBundleFailsClosed(t *testing.T) {
	var bundle provider.Bundle[fixtureConfig]
	if _, err := bundle.NewRuntime(fixtureConfig{}); !errors.Is(err, provider.ErrInvalidBundle) {
		t.Fatalf("error = %v", err)
	}
}

var _ provider.Provider[fixtureRequest] = fixtureProvider{}
