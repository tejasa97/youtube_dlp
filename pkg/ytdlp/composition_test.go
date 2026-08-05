package ytdlp

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type compositionTestProvider struct {
	name  string
	calls *atomic.Int32
}

func (provider compositionTestProvider) Name() string { return provider.name }
func (compositionTestProvider) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Hostname() == "composition.invalid"
}
func (provider compositionTestProvider) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	provider.calls.Add(1)
	return extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(provider.name)},
		value.Field{Key: "title", Value: value.String(provider.name)},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))), nil
}

func TestExplicitCompositionRunsWithoutBroadCatalogFallback(t *testing.T) {
	var catalogCalls, providerCalls atomic.Int32
	composition := composeProviders(func() []extractor.Extractor {
		catalogCalls.Add(1)
		return []extractor.Extractor{compositionTestProvider{name: "focused", calls: &providerCalls}}
	})
	client := newClientWithComposition(composition)

	result, err := client.Run(context.Background(), Request{
		URL: "https://composition.invalid/video", SkipDownload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "focused" {
		t.Fatalf("result = %#v", result)
	}
	if catalogCalls.Load() != 1 || providerCalls.Load() != 1 {
		t.Fatalf("catalog calls=%d provider calls=%d", catalogCalls.Load(), providerCalls.Load())
	}

	_, err = client.Run(context.Background(), Request{
		URL: "https://example.com/media.mp4", SkipDownload: true,
	})
	if !IsCategory(err, ErrorUnsupported) || providerCalls.Load() != 1 {
		t.Fatalf("broad catalog fallback error=%v provider calls=%d", err, providerCalls.Load())
	}
}

func TestCompositionBuildsOperationLocalSelectionRegistries(t *testing.T) {
	var catalogCalls, firstCalls, secondCalls atomic.Int32
	composition := composeProviders(func() []extractor.Extractor {
		catalogCalls.Add(1)
		return []extractor.Extractor{
			compositionTestProvider{name: "first", calls: &firstCalls},
			compositionTestProvider{name: "second", calls: &secondCalls},
		}
	})
	client := newClientWithComposition(composition)

	tests := []struct {
		name string
	}{
		{name: "first"},
		{name: "second"},
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, len(tests))
	for _, test := range tests {
		test := test
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := client.Run(context.Background(), Request{
				URL:                "https://composition.invalid/" + test.name,
				SkipDownload:       true,
				ExtractorSelection: ExtractorSelectionOptions{Rules: []string{test.name}},
			})
			if err != nil {
				errorsSeen <- err
				return
			}
			if result.Extractor != test.name {
				errorsSeen <- errors.New("unexpected composed extractor: " + result.Extractor)
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if catalogCalls.Load() != int32(len(tests)) || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("catalog=%d first=%d second=%d", catalogCalls.Load(), firstCalls.Load(), secondCalls.Load())
	}
}

func TestNewClientExplicitlySelectsBroadCompatibilityComposition(t *testing.T) {
	registry, err := NewClient().composition.newRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := productRegistry().Names()
	if got := registry.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("NewClient catalog differs from broad compatibility catalog")
	}
	registryAgain, err := NewClient().composition.newRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if registry == registryAgain {
		t.Fatal("composition shared a mutable registry across clients")
	}
}

func TestRunRejectsMissingCompositionWithoutFallback(t *testing.T) {
	client := newClientWithComposition(engineComposition{})
	_, err := client.Run(context.Background(), Request{
		URL: "https://example.com/media.mp4", SkipDownload: true,
	})
	if !IsCategory(err, ErrorInternal) || !errors.Is(err, errInvalidProviderComposition) {
		t.Fatalf("Run() error = %v", err)
	}
}

var _ extractor.Extractor = compositionTestProvider{}
