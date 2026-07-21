package ytdlp

import "github.com/ytdlp-go/ytdlp/internal/youtubepot"

type YouTubePOTContext = youtubepot.Context
type YouTubePOTRequest = youtubepot.Request
type YouTubePOTResponse = youtubepot.Response
type YouTubePOTProvider = youtubepot.Provider
type YouTubePOTProviderFunc = youtubepot.ProviderFunc
type YouTubePOTFetchPolicy = youtubepot.FetchPolicy

const (
	YouTubePOTContextGVS    = youtubepot.ContextGVS
	YouTubePOTContextPlayer = youtubepot.ContextPlayer
	YouTubePOTContextSubs   = youtubepot.ContextSubs

	YouTubePOTFetchNever  = youtubepot.FetchNever
	YouTubePOTFetchAuto   = youtubepot.FetchAuto
	YouTubePOTFetchAlways = youtubepot.FetchAlways
)

// YouTubePOTConfig installs an explicit provider chain. Providers receive only
// bounded binding metadata and return base64url tokens. Token values remain in
// the process-local bounded cache and are excluded from errors and events.
type YouTubePOTConfig struct {
	Policy    YouTubePOTFetchPolicy
	CacheSize int
	Providers []YouTubePOTProvider
}

func WithYouTubePOTProviders(config YouTubePOTConfig) Option {
	return func(client *Client) {
		providers := append([]youtubepot.Provider(nil), config.Providers...)
		client.youtubePOT, client.youtubePOTErr = youtubepot.New(youtubepot.Config{
			Policy: config.Policy, CacheSize: config.CacheSize, Providers: providers,
		})
	}
}
