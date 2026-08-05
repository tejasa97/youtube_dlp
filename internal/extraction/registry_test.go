package extraction

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type registryRequest struct{ rawURL string }

func (request registryRequest) ExtractionURL() string { return request.rawURL }

type registryProvider struct {
	name        string
	suitable    bool
	aliases     []string
	description string
}

func (provider registryProvider) Name() string { return provider.name }
func (provider registryProvider) Suitable(*url.URL) bool {
	return provider.suitable
}
func (provider registryProvider) Extract(_ context.Context, request registryRequest) (Extraction, error) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(provider.name)},
		value.Field{Key: "title", Value: value.String(request.rawURL)},
	))
	return Media(info), nil
}
func (provider registryProvider) ExtractorMetadata() Metadata {
	return Metadata{Aliases: provider.aliases, Description: provider.description}
}

type explicitRegistryProvider struct{ registryProvider }

func (explicitRegistryProvider) ExplicitOnly() {}

type searchRegistryProvider struct{ registryProvider }

func (searchRegistryProvider) SupportsSearchPrefix(prefix string) bool { return prefix == "fixture" }
func (searchRegistryProvider) SearchQueryAllowed(query string) bool    { return query != "" }

func TestRegistryRoutesAndExtractsExplicitProviders(t *testing.T) {
	providers := []Provider[registryRequest]{
		registryProvider{name: "first", suitable: false},
		registryProvider{name: "second", suitable: true},
	}
	registry := NewRegistry[registryRequest](providers...)
	providers[1] = nil

	if got := registry.Names(); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("Names() = %v", got)
	}
	selected, err := registry.Select("https://example.test/video")
	if err != nil || selected.Name() != "second" {
		t.Fatalf("Select() = %v, %v", selected, err)
	}
	result, name, err := registry.Extract(context.Background(), registryRequest{rawURL: "https://example.test/video"})
	if err != nil || name != "second" {
		t.Fatalf("Extract() name=%q err=%v", name, err)
	}
	if title, _ := result.Info.Title(); title != "https://example.test/video" {
		t.Fatalf("Extract() title = %q", title)
	}
}

func TestRegistrySelectionIsBoundedAndOperationLocal(t *testing.T) {
	first := registryProvider{name: "first", suitable: true}
	second := registryProvider{name: "Second", suitable: true}
	explicit := explicitRegistryProvider{registryProvider{name: "plugin.example", suitable: false}}
	registry := NewRegistry[registryRequest](first, second, explicit)

	if err := registry.ConfigureSelection([]string{"second", "FIRST", "-SECOND"}); err != nil {
		t.Fatal(err)
	}
	selected, err := registry.Select("https://example.test/video")
	if err != nil || selected.Name() != "first" {
		t.Fatalf("selected = %v, %v", selected, err)
	}
	if _, err := registry.SelectFor("https://example.test/video", "Second"); !errors.Is(err, ErrUnsupported) || !errors.Is(err, ErrSelectionDisabled) {
		t.Fatalf("disabled SelectFor() error = %v", err)
	}
	selected, err = registry.SelectFor("https://example.test/video", "PLUGIN.EXAMPLE")
	if err != nil || selected.Name() != "plugin.example" {
		t.Fatalf("explicit-only SelectFor() = %v, %v", selected, err)
	}

	other := NewRegistry[registryRequest](first, second)
	selected, err = other.Select("https://example.test/video")
	if err != nil || selected.Name() != "first" {
		t.Fatalf("selection leaked to another registry: %v, %v", selected, err)
	}
	if err := registry.ConfigureSelection([]string{"end", "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Select("https://example.test/video"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("end sentinel error = %v", err)
	}
}

func TestRegistryMetadataAndSearchCapabilitiesAreOptional(t *testing.T) {
	search := searchRegistryProvider{registryProvider{
		name: "Zulu", suitable: true, aliases: []string{"z", "Z", "Zulu"},
		description: strings.Repeat("é", 200),
	}}
	registry := NewRegistry[registryRequest](
		search,
		registryProvider{name: "alpha", suitable: false},
		registryProvider{name: "generic", suitable: true},
	)
	metadata := registry.MetadataForURLs([]string{"https://example.test/video", "https://example.test/video"})
	if got := []string{metadata[0].Name, metadata[1].Name, metadata[2].Name}; !reflect.DeepEqual(got, []string{"alpha", "Zulu", "generic"}) {
		t.Fatalf("metadata order = %v", got)
	}
	if len(metadata[1].Description) > maxExtractorDescriptionBytes ||
		!reflect.DeepEqual(metadata[1].Aliases, []string{"z"}) ||
		!reflect.DeepEqual(metadata[1].URLs, []string{"https://example.test/video"}) {
		t.Fatalf("metadata = %#v", metadata[1])
	}
	provider, ok := registry.SearchPrefix("fixture")
	if !ok || provider.Name() != "Zulu" || !provider.SearchQueryAllowed("query") {
		t.Fatalf("SearchPrefix() = %v, %t", provider, ok)
	}
}

func TestRegistryRejectsMalformedInputsAndNilReceiver(t *testing.T) {
	var registry *Registry[registryRequest]
	if registry.Names() != nil {
		t.Fatal("nil registry returned names")
	}
	if err := registry.ConfigureSelection(nil); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("nil ConfigureSelection() error = %v", err)
	}
	registry = NewRegistry[registryRequest](registryProvider{name: "fixture", suitable: true})
	for _, rawURL := range []string{"", "not a URL", "://bad"} {
		if _, err := registry.Select(rawURL); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Select(%q) error = %v", rawURL, err)
		}
	}
	if err := ValidateSelectionRules([]string{"[malformed"}); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("ValidateSelectionRules() error = %v", err)
	}
}

var _ Provider[registryRequest] = registryProvider{}
var _ MetadataProvider = registryProvider{}
var _ SearchPrefixProvider[registryRequest] = searchRegistryProvider{}
var _ ExplicitOnlyProvider = explicitRegistryProvider{}
