package engine_test

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/engine/value"
)

type typedOptions struct{ Enabled bool }

type typedRequest struct {
	engine.ProviderRequest
	Options typedOptions
}

type publicProvider struct{}

func (publicProvider) Name() string { return "public-fixture" }
func (publicProvider) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Hostname() == "public.example"
}
func (publicProvider) Extract(_ context.Context, request typedRequest) (engine.Extraction, error) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Public fixture")},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))
	return engine.Media(info), nil
}

type publicTransport struct{}

func (publicTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (publicTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, nil
}

type publicCredentials struct{}

func (publicCredentials) Lookup(context.Context, string) (engine.Credential, bool, error) {
	return engine.Credential{}, false, nil
}

type publicChallengeSolver struct{}

func (publicChallengeSolver) SolvePlayer(context.Context, string, string, []engine.ChallengeRequest, bool) (engine.ChallengeResult, error) {
	return engine.ChallengeResult{}, nil
}

type publicPOTResolver struct{}

func (publicPOTResolver) ResolvePolicy(context.Context, engine.POTRequest, bool, bool) (string, bool, error) {
	return "", false, nil
}
func (publicPOTResolver) NewEpisodeResolver() engine.POTEpisodeResolver { return nil }

func TestExternalPackageCanComposeTypedProviderRegistry(t *testing.T) {
	request := typedRequest{
		ProviderRequest: engine.ProviderRequest{
			URL:         "https://public.example/video",
			Transport:   publicTransport{},
			Credentials: publicCredentials{},
		},
		Options: typedOptions{Enabled: true},
	}
	registry := engine.NewRegistry[typedRequest](publicProvider{})
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"public-fixture"}) {
		t.Fatalf("provider names = %v", got)
	}
	result, name, err := registry.Extract(context.Background(), request)
	if err != nil || name != "public-fixture" || result.IsPlaylist() || result.IsURL() {
		t.Fatalf("result=%#v name=%q error=%v", result, name, err)
	}
	if title, ok := result.Info.Title(); !ok || title != "Public fixture" {
		t.Fatalf("title=%q ok=%v", title, ok)
	}
}

func TestExternalPackageCanComposeAndRunClient(t *testing.T) {
	composition := engine.NewComposition[typedRequest](
		func(engine.ClientProviderConfig) []engine.Provider[typedRequest] {
			return []engine.Provider[typedRequest]{publicProvider{}}
		},
		func(operation engine.Operation, _ engine.Request) typedRequest {
			return typedRequest{ProviderRequest: operation.Request, Options: typedOptions{Enabled: true}}
		},
		engine.ProviderHooks{},
	)
	client := engine.NewClient(composition)
	result, err := client.Run(context.Background(), engine.Request{
		URL: "https://public.example/video", SkipDownload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "public-fixture" || result.Downloaded {
		t.Fatalf("result = %#v", result)
	}
}

var (
	_ engine.Provider[typedRequest] = publicProvider{}
	_ engine.Transport              = publicTransport{}
	_ engine.CredentialProvider     = publicCredentials{}
	_ engine.ChallengeSolver        = publicChallengeSolver{}
	_ engine.POTResolver            = publicPOTResolver{}
)
