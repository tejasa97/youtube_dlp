// Package format implements media-format selection.
package format

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"unicode"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrNoFormats      = errors.New("no downloadable formats")
	ErrInvalidHeaders = errors.New("invalid format HTTP headers")
	// ErrInvalidFormats indicates a formats list whose members violate the
	// structural contracts of the selector pipeline: a non-list formats field,
	// a non-object member, or a non-string/non-null format_id.
	ErrInvalidFormats = errors.New("invalid format list")
	// ErrFormatLimit indicates the format pipeline exceeded one of its bounded
	// inputs (entry count, per-ID bytes, or total normalized bytes).
	ErrFormatLimit = errors.New("format preparation exceeds limit")
)

type Selection struct {
	ID       string
	URL      string
	Ext      string
	Filesize int64
	Protocol string
	VCodec   string
	ACodec   string
	Height   int64
	TBR      float64
	Headers  http.Header
	// CredentialIsolated requires isolated no-redirect transport for media
	// fetches so ambient cookies, authorization, and referer cannot leak.
	CredentialIsolated bool
	// CredentialIsolatedReferer is an extractor-validated referer that may be
	// preserved only by the credential-isolated media transport. It is never
	// taken from ambient request headers.
	CredentialIsolatedReferer string
	// HostPolicy names an extractor-owned attributable-origin policy used by
	// credential-isolated native downloaders. Empty keeps the existing generic
	// credential boundary.
	HostPolicy string
	// NiconicoScoped applies the Niconico attributable host policy to every
	// manifest and fragment hop after the generic HLS dispatcher re-enters.
	NiconicoScoped bool
	// AllowedHosts is an extractor-owned HLS trust boundary. When present, the
	// native HLS downloader applies it to the manifest, variants, segments,
	// encryption keys, and initialization maps.
	AllowedHosts []string

	// YouTubePostLive selects the finite post-live DVR sequence downloader.
	// The discriminator is extractor-produced and never inferred from a URL.
	YouTubePostLive      bool
	YouTubeLiveFromStart bool
	YouTubeItag          int64
	YouTubeClient        string
	YouTubeSourceURL     string
	TargetDuration       float64
	// YouTubeSABR selects the finite-VOD SABR/UMP downloader.
	YouTubeSABR                bool
	YouTubeSABRTrack           string
	YouTubeSABRItag            int64
	YouTubeSABRLastModified    int64
	YouTubeSABRXTags           string
	YouTubeSABRServerURL       string
	YouTubeSABRUstreamerConfig string
	YouTubeSABRClientID        int64
	YouTubeSABRClientVersion   string
	YouTubeSABRUserAgent       string
	YouTubeSABRVisitorData     string
	YouTubeSABRDurationSec     int64
	YouTubeSABRVideoID         string
	YouTubeSABRClientName      string
	YouTubeSABRDrc             bool
	YouTubeSABRAudioTrackID    string
	LiveStartTimestamp         int64

	// sourceIndex records the original list index of the extractor-owned format
	// this selection was prepared from. It is unexported because it is
	// meaningful only in conjunction with the prepared clone's identity, not
	// the original extractor-owned format list. Use SourceFormatIndex to query
	// it.
	sourceIndex     int
	sourceKnown     bool
	normalizedIndex int
	normalizedKnown bool
}

// SourceFormatIndex returns the original list index of the extractor-owned
// format this selection was prepared from. The second return value reports
// whether the index is known; selection paths that bypass prepareFormats (for
// example the legacy helper code) report false.
func (selection Selection) SourceFormatIndex() (int, bool) {
	return selection.sourceIndex, selection.sourceKnown
}

// setSourceFormatIndex records the original list index of the extractor-owned
// format this selection was prepared from. Internal to the format package.
func (selection *Selection) setSourceFormatIndex(index int) {
	if selection == nil {
		return
	}
	selection.sourceIndex = index
	selection.sourceKnown = true
}

// NormalizedFormatIndex returns the selection's position in the canonical
// filtered and sorted format list.
func (selection Selection) NormalizedFormatIndex() (int, bool) {
	return selection.normalizedIndex, selection.normalizedKnown
}

func (selection *Selection) setNormalizedFormatIndex(index int) {
	if selection == nil {
		return
	}
	selection.normalizedIndex = index
	selection.normalizedKnown = true
}

// Default applies yt-dlp-style best-quality selection: prefer a video-only and
// audio-only pair, then a single combined format. Explicit user selectors
// remain authoritative.
func Default(info value.Info, options Options) ([]Selection, error) {
	prepared, err := Prepare(info, options)
	if err != nil {
		return nil, err
	}
	return prepared.Default()
}

// Default applies the default selector to the canonical prepared formats.
// It preserves the historical contract: one output plan, fail with
// ErrMultiOutput if multiple plans would be returned.
func (prepared Prepared) Default() ([]Selection, error) {
	// Legacy compatibility: a planner capable of merging formats and not
	// emitting to stdout picks the VOD default selector. The product
	// layer can override via DefaultWithContext.
	plans, err := prepared.DefaultWithContext(
		PlannerCapabilities{CanMergeFormats: true, OutputToStdout: false},
		DefaultSelectorContext{IsLive: false, LiveFromStart: false, LegacyFormatSpec: false},
		EvaluationOptions{},
	)
	if err != nil {
		return nil, err
	}
	if len(plans) != 1 {
		return nil, ErrMultiOutput
	}
	return plans[0].Tracks, nil
}

// DefaultWithContext computes the default selector via DefaultSelectorSpec
// and evaluates it. The selector depends on the injected runtime
// capabilities (merge availability, stdout destination) and live context;
// the function is pure with respect to those inputs.
func (prepared Prepared) DefaultWithContext(
	capabilities PlannerCapabilities,
	context DefaultSelectorContext,
	evaluationOptions EvaluationOptions,
) ([]OutputPlan, error) {
	selectorSpec := DefaultSelectorSpec(capabilities, context, prepared.options)
	selector, err := ParseSelector(selectorSpec)
	if err != nil {
		return nil, err
	}
	return prepared.PlanWithOptions(selector, evaluationOptions)
}

// Best selects the first canonical format.
func Best(info value.Info) (Selection, error) {
	prepared, err := Prepare(info, Options{})
	if err != nil {
		return Selection{}, err
	}
	return prepared.Best()
}

// Best selects the first playable canonical format.
func (prepared Prepared) Best() (Selection, error) {
	for _, candidate := range prepared.formats {
		object := candidate.Object
		rawURL, ok := object.Lookup("url").StringValue()
		sabr, _ := object.Lookup("_youtube_sabr").Bool()
		if !sabr && (!ok || rawURL == "") {
			continue
		}
		headers, err := mergeHeaders(prepared.info.Lookup("http_headers"), object.Lookup("http_headers"))
		if err != nil {
			return Selection{}, err
		}
		selection := Selection{URL: rawURL, Headers: headers}
		selection.ID, _ = object.Lookup("format_id").StringValue()
		selection.Ext, _ = object.Lookup("ext").StringValue()
		selection.Filesize, _ = object.Lookup("filesize").Int()
		selection.Protocol, _ = object.Lookup("protocol").StringValue()
		selection.VCodec, _ = object.Lookup("vcodec").StringValue()
		selection.ACodec, _ = object.Lookup("acodec").StringValue()
		selection.Height, _ = object.Lookup("height").Int()
		selection.TBR, _ = numeric(object.Lookup("tbr"))
		selection.YouTubePostLive, _ = object.Lookup("_youtube_post_live").Bool()
		selection.YouTubeLiveFromStart, _ = object.Lookup("_youtube_live_from_start").Bool()
		selection.YouTubeItag, _ = object.Lookup("_youtube_itag").Int()
		selection.YouTubeClient, _ = object.Lookup("_youtube_client").StringValue()
		selection.YouTubeSourceURL, _ = object.Lookup("_youtube_source_url").StringValue()
		selection.TargetDuration, _ = numeric(object.Lookup("target_duration"))
		selection.LiveStartTimestamp, _ = object.Lookup("live_start_timestamp").Int()
		selection.YouTubeSABR, _ = object.Lookup("_youtube_sabr").Bool()
		selection.YouTubeSABRTrack, _ = object.Lookup("_youtube_sabr_track").StringValue()
		selection.YouTubeSABRItag, _ = object.Lookup("_youtube_sabr_itag").Int()
		selection.YouTubeSABRLastModified, _ = object.Lookup("_youtube_sabr_last_modified").Int()
		selection.YouTubeSABRXTags, _ = object.Lookup("_youtube_sabr_xtags").StringValue()
		selection.YouTubeSABRServerURL, _ = object.Lookup("_youtube_sabr_server_url").StringValue()
		selection.YouTubeSABRUstreamerConfig, _ = object.Lookup("_youtube_sabr_ustreamer_config").StringValue()
		selection.YouTubeSABRClientID, _ = object.Lookup("_youtube_sabr_client_id").Int()
		selection.YouTubeSABRClientVersion, _ = object.Lookup("_youtube_sabr_client_version").StringValue()
		selection.YouTubeSABRUserAgent, _ = object.Lookup("_youtube_sabr_user_agent").StringValue()
		selection.YouTubeSABRVisitorData, _ = object.Lookup("_youtube_sabr_visitor_data").StringValue()
		selection.YouTubeSABRDurationSec, _ = object.Lookup("_youtube_sabr_duration_sec").Int()
		selection.YouTubeSABRVideoID, _ = object.Lookup("_youtube_sabr_video_id").StringValue()
		selection.YouTubeSABRClientName, _ = object.Lookup("_youtube_client").StringValue()
		selection.YouTubeSABRDrc, _ = object.Lookup("_youtube_sabr_drc").Bool()
		selection.YouTubeSABRAudioTrackID, _ = object.Lookup("_youtube_sabr_audio_track_id").StringValue()
		selection.CredentialIsolated, _ = object.Lookup("_credential_isolated").Bool()
		selection.CredentialIsolatedReferer, _ = object.Lookup("_credential_isolated_referer").StringValue()
		selection.HostPolicy, _ = object.Lookup("_host_policy").StringValue()
		if selection.HostPolicy == "" {
			selection.HostPolicy, _ = object.Lookup("_ted_host_policy").StringValue()
		}
		selection.NiconicoScoped, _ = object.Lookup("_niconico_scoped").Bool()
		var allowedHostsErr error
		selection.AllowedHosts, allowedHostsErr = readAllowedHosts(object)
		if allowedHostsErr != nil {
			return Selection{}, allowedHostsErr
		}
		if selection.YouTubeSABR {
			selection.Protocol = "youtube_sabr_ump"
		}
		selection.setSourceFormatIndex(candidate.Source)
		selection.setNormalizedFormatIndex(candidate.Index)
		return selection, nil
	}
	return Selection{}, fmt.Errorf("%w: formats contain no URL", ErrNoFormats)
}

func readAllowedHosts(object *value.Object) ([]string, error) {
	raw := object.Lookup("_allowed_hosts")
	if raw.IsMissing() {
		return nil, nil
	}
	values, ok := raw.ListValue()
	if !ok || len(values) == 0 || len(values) > 32 {
		return nil, fmt.Errorf("%w: invalid allowed HLS hosts", ErrInvalidFormats)
	}
	hosts := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, rawHost := range values {
		host, ok := rawHost.StringValue()
		host, ok = normalizeAllowedHost(host, ok)
		if !ok {
			return nil, fmt.Errorf("%w: invalid allowed HLS host", ErrInvalidFormats)
		}
		if _, duplicate := seen[host]; duplicate {
			return nil, fmt.Errorf("%w: duplicate allowed HLS host", ErrInvalidFormats)
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

func normalizeAllowedHost(host string, present bool) (string, bool) {
	if !present || host == "" || len(host) > 253 || host != strings.TrimSpace(host) || net.ParseIP(host) != nil {
		return "", false
	}
	if strings.IndexFunc(host, unicode.IsSpace) >= 0 || strings.ContainsAny(host, "/\\:\x00\r\n") {
		return "", false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return "", false
			}
		}
	}
	return strings.ToLower(host), true
}

func mergeHeaders(values ...value.Value) (http.Header, error) {
	headers := make(http.Header)
	for _, candidate := range values {
		if candidate.IsMissing() || candidate.IsNull() {
			continue
		}
		object, ok := candidate.Object()
		if !ok {
			return nil, fmt.Errorf("%w: header collection is not an object", ErrInvalidHeaders)
		}
		for _, field := range object.Fields() {
			text, ok := field.Value.StringValue()
			name := http.CanonicalHeaderKey(field.Key)
			if !ok || name == "" || strings.ContainsAny(field.Key+text, "\r\n") {
				return nil, fmt.Errorf("%w: malformed field", ErrInvalidHeaders)
			}
			headers.Set(name, text)
		}
	}
	return headers, nil
}

// MergeHeaders validates and combines ordered metadata header collections.
// Later collections override earlier values.
func MergeHeaders(values ...value.Value) (http.Header, error) {
	return mergeHeaders(values...)
}

func numeric(input value.Value) (float64, bool) {
	if integer, ok := input.Int(); ok {
		return float64(integer), true
	}
	return input.Float()
}
