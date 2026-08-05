package engine

// Request is the provider-neutral state supplied by engine orchestration to a
// composed provider. Provider-specific packages layer typed options on this
// request instead of extending the engine contract.
type Request struct {
	URL string
	// SearchQueryOverride carries an original bounded query after routing chose
	// an opaque search provider token. It is never rendered by Request.String.
	SearchQueryOverride string
	// Referer is an optional validated HTTPS embedding page URL. It must not
	// carry arbitrary caller headers or credentials.
	Referer       string
	Transport     Transport
	Credentials   CredentialProvider
	VideoPassword string
	// NoPlaylist asks providers that can interpret one URL as either a media
	// item or playlist to prefer the media item. Pure playlist URLs are not
	// affected.
	NoPlaylist bool
}

// String and GoString deliberately hide URLs, credentials, transports, and
// passwords from diagnostic formatting.
func (Request) String() string                { return "[redacted extraction request]" }
func (Request) GoString() string              { return "extraction.Request{[redacted]}" }
func (request Request) ExtractionURL() string { return request.URL }
