package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hls"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	hlsDiscontinuityIDMarker             = "__hls_discontinuity_"
	hlsDiscontinuityDestinationSuffixKey = "__hls_discontinuity_destination_suffix"
	maxHLSDiscontinuityManifest          = 16 << 20
)

var (
	ErrHLSDiscontinuitySelection         = errors.New("invalid HLS discontinuity selection")
	ErrHLSDiscontinuityGroupMissing      = errors.New("requested HLS discontinuity group is missing")
	ErrHLSDiscontinuityPlaylistEmpty     = errors.New("HLS discontinuity playlist is empty")
	ErrHLSDiscontinuityGroupAdOnly       = errors.New("requested HLS discontinuity group is advertisement-only")
	ErrHLSDiscontinuityPlaylistMalformed = errors.New("malformed HLS discontinuity playlist")
	ErrHLSDiscontinuityHostPolicy        = errors.New("HLS discontinuity URL violates AllowedHosts")
)

// hlsDiscontinuitySelectionID keeps the protocol identity in a format ID so
// the existing selector and output-plan machinery can carry a selected group
// without changing the internal format package's public shape.
func hlsDiscontinuitySelectionID(formatID string, sequence int64) string {
	return hlsDiscontinuityBaseID(formatID) + hlsDiscontinuityIDMarker + strconv.FormatInt(sequence, 10)
}

func hlsDiscontinuityBaseID(formatID string) string {
	if marker := strings.Index(formatID, hlsDiscontinuityIDMarker); marker >= 0 {
		return formatID[:marker]
	}
	return formatID
}

func hlsDiscontinuityGroupFromSelection(selection mediaformat.Selection) (hls.DiscontinuityGroupID, bool, error) {
	marker := strings.LastIndex(selection.ID, hlsDiscontinuityIDMarker)
	if marker < 0 {
		return hls.DiscontinuityGroupID{}, false, nil
	}
	value := selection.ID[marker+len(hlsDiscontinuityIDMarker):]
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence < 0 {
		return hls.DiscontinuityGroupID{}, false, fmt.Errorf("%w: invalid HLS discontinuity group selection", ErrHLSDiscontinuitySelection)
	}
	return hls.DiscontinuityGroupID{DiscontinuitySequence: sequence}, true, nil
}

func clonePlanForHLSDiscontinuityGroup(
	plan mediaformat.OutputPlan,
	group hls.DiscontinuityGroupID,
	explicitMulti bool,
) mediaformat.OutputPlan {
	cloned := plan
	cloned.Tracks = make([]mediaformat.Selection, len(plan.Tracks))
	copy(cloned.Tracks, plan.Tracks)
	for index := range cloned.Tracks {
		if cloned.Tracks[index].Protocol == "m3u8_native" {
			cloned.Tracks[index].ID = hlsDiscontinuitySelectionID(
				cloned.Tracks[index].ID, group.DiscontinuitySequence,
			)
		}
	}
	cloned.Metadata = value.NewInfo(plan.Metadata.Fields().Clone())
	cloned.Metadata.Set("_hls_discontinuity_group", value.Int(group.DiscontinuitySequence))
	if explicitMulti {
		cloned.Metadata.Set(
			hlsDiscontinuityDestinationSuffixKey,
			value.String(".d"+strconv.FormatInt(group.DiscontinuitySequence, 10)),
		)
	}
	if len(cloned.Tracks) == 1 {
		cloned.Metadata.Set("format_id", value.String(cloned.Tracks[0].ID))
	}
	return cloned
}

func hlsDiscontinuityDestinationSuffix(info value.Info) string {
	suffix, ok := info.Lookup(hlsDiscontinuityDestinationSuffixKey).StringValue()
	if !ok || suffix == "" {
		return ""
	}
	return suffix
}

func applyHLSDiscontinuityDestinationSuffix(path string, info value.Info) string {
	suffix := hlsDiscontinuityDestinationSuffix(info)
	if suffix == "" || path == "" || path == "-" {
		return path
	}
	extension := filepath.Ext(path)
	return strings.TrimSuffix(path, extension) + suffix + extension
}

type hlsDiscontinuityDiscovery struct {
	Groups          []hls.DiscontinuityGroup
	InitialPlaylist hls.InitialPlaylist
}

// hlsInitialPlaylistCacheKey scopes the selected-representation snapshot to
// one processMedia entry. The operation itself may process entries
// concurrently, so this state must travel with the entry context rather than
// live on operation.
type hlsInitialPlaylistCacheKey struct{}

type hlsInitialPlaylistCache map[string]hls.InitialPlaylist

func withHLSInitialPlaylistCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, hlsInitialPlaylistCacheKey{}, make(hlsInitialPlaylistCache, 1))
}

func hlsInitialPlaylistsFromContext(ctx context.Context) hlsInitialPlaylistCache {
	cache, _ := ctx.Value(hlsInitialPlaylistCacheKey{}).(hlsInitialPlaylistCache)
	return cache
}

func (operation *operation) hlsDiscontinuityGroupsForSelection(
	ctx context.Context,
	selection mediaformat.Selection,
) (hlsDiscontinuityDiscovery, error) {
	transport, err := operation.mediaTransport(
		selection.CredentialIsolated,
		selection.CredentialIsolatedReferer,
		selection.HostPolicy,
		selection.Protocol,
	)
	if err != nil {
		return hlsDiscontinuityDiscovery{}, err
	}
	hlsTransport, ok := transport.(hls.Transport)
	if !ok || hlsTransport == nil {
		return hlsDiscontinuityDiscovery{}, errors.New("HLS transport unavailable")
	}
	return operation.readHLSDiscontinuityGroups(ctx, selection.URL, selection.Headers, hlsTransport, selection.AllowedHosts)
}

func (operation *operation) readHLSDiscontinuityGroups(
	ctx context.Context,
	rawURL string,
	headers http.Header,
	transport hls.Transport,
	allowedHosts []string,
) (hlsDiscontinuityDiscovery, error) {
	if !hlsDiscontinuityAllowedURL(rawURL, allowedHosts) {
		return hlsDiscontinuityDiscovery{}, ErrHLSDiscontinuityHostPolicy
	}
	if headers == nil {
		headers = make(http.Header)
	}
	initialURL := rawURL
	body, err := readHLSPageWithHeaders(ctx, transport, rawURL, headers, maxHLSDiscontinuityManifest)
	if err != nil {
		return hlsDiscontinuityDiscovery{}, err
	}
	playlist, err := hls.Parse(rawURL, body)
	if err != nil {
		return hlsDiscontinuityDiscovery{}, fmt.Errorf("%w: %v", ErrHLSDiscontinuityPlaylistMalformed, err)
	}
	if playlist.Media == nil {
		if len(playlist.Variants) == 0 {
			return hlsDiscontinuityDiscovery{}, hls.ErrInvalidPlaylist
		}
		variant := playlist.Variants[0]
		for _, candidate := range playlist.Variants[1:] {
			if candidate.Bandwidth > variant.Bandwidth {
				variant = candidate
			}
		}
		if !hlsDiscontinuityAllowedURL(variant.URL, allowedHosts) {
			return hlsDiscontinuityDiscovery{}, ErrHLSDiscontinuityHostPolicy
		}
		initialURL = variant.URL
		body, err = readHLSPageWithHeaders(ctx, transport, variant.URL, headers, maxHLSDiscontinuityManifest)
		if err != nil {
			return hlsDiscontinuityDiscovery{}, err
		}
		playlist, err = hls.Parse(variant.URL, body)
		if err != nil {
			return hlsDiscontinuityDiscovery{}, fmt.Errorf("%w: %v", ErrHLSDiscontinuityPlaylistMalformed, err)
		}
	}
	if playlist.Media == nil {
		return hlsDiscontinuityDiscovery{}, fmt.Errorf("%w: media playlist missing", ErrHLSDiscontinuityPlaylistMalformed)
	}
	groups, err := hls.BuildDiscontinuityGroups(playlist.Media)
	if err != nil {
		return hlsDiscontinuityDiscovery{}, fmt.Errorf("%w: %v", ErrHLSDiscontinuityPlaylistMalformed, err)
	}
	return hlsDiscontinuityDiscovery{
		Groups:          groups,
		InitialPlaylist: hls.InitialPlaylist{URL: initialURL, Body: append([]byte(nil), body...)},
	}, nil
}

func hlsDiscontinuityAllowedHosts(raw value.Value) ([]string, error) {
	if raw.IsMissing() {
		return nil, nil
	}
	items, ok := raw.ListValue()
	if !ok {
		return nil, hls.ErrInvalidPlaylist
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		host, ok := item.StringValue()
		if !ok || host == "" {
			return nil, hls.ErrInvalidPlaylist
		}
		result = append(result, host)
	}
	return result, nil
}

func hlsDiscontinuityAllowedURL(rawURL string, allowedHosts []string) bool {
	if len(allowedHosts) == 0 {
		return true
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	for _, rawHost := range allowedHosts {
		zone := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(rawHost), "."))
		if host == zone || strings.HasSuffix(host, "."+zone) {
			return true
		}
	}
	return false
}

func readHLSPageWithHeaders(
	ctx context.Context,
	transport hls.Transport,
	rawURL string,
	headers http.Header,
	limit int64,
) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	response, err := transport.Do(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("empty HLS manifest response")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &network.StatusError{Code: response.StatusCode, URL: network.RedactRawURL(rawURL)}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("manifest exceeds %d bytes", limit)
	}
	return body, nil
}

type hlsDiscontinuityProgressSink struct {
	sink     events.Sink
	sequence int64
}

func (sink hlsDiscontinuityProgressSink) Emit(ctx context.Context, event events.Event) error {
	if sink.sink == nil {
		return nil
	}
	if event.Message == "" {
		event.Message = "HLS discontinuity group " + strconv.FormatInt(sink.sequence, 10)
	} else {
		event.Message = "HLS discontinuity group " + strconv.FormatInt(sink.sequence, 10) + ": " + event.Message
	}
	return sink.sink.Emit(ctx, event)
}

func deduplicateHLSDiscontinuitySequences(input []int64) []int64 {
	if len(input) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(input))
	result := make([]int64, 0, len(input))
	for _, sequence := range input {
		if _, exists := seen[sequence]; exists {
			continue
		}
		seen[sequence] = struct{}{}
		result = append(result, sequence)
	}
	return result
}

func classifyHLSDiscontinuityDiscoveryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, hls.ErrInvalidPlaylist) || errors.Is(err, hls.ErrInvalidDiscontinuityGroups) {
		return fmt.Errorf("%w: %v", ErrHLSDiscontinuityPlaylistMalformed, err)
	}
	return err
}

// selectHLSDiscontinuityPlans discovers groups only after ordinary format
// selection has produced the output plans. Each selected HLS track is
// therefore the sole representation this integration probes; no extractor
// format list is expanded and no implicit multi-output selection is created.
func (operation *operation) selectHLSDiscontinuityPlans(
	ctx context.Context,
	plans []mediaformat.OutputPlan,
) ([]mediaformat.OutputPlan, error) {
	if len(plans) == 0 {
		return plans, nil
	}
	explicit := deduplicateHLSDiscontinuitySequences(operation.request.HLSDiscontinuitySequences)
	initialPlaylists := hlsInitialPlaylistsFromContext(ctx)
	result := make([]mediaformat.OutputPlan, 0, len(plans))
	for _, plan := range plans {
		hlsTrackCount := 0
		for _, track := range plan.Tracks {
			if track.Protocol == "m3u8_native" {
				hlsTrackCount++
			}
		}
		if hlsTrackCount > 1 {
			return nil, fmt.Errorf("%w: --hls-split-discontinuity does not support output plans with multiple HLS representations", ErrUnsupported)
		}
		selectedHLS := false
		for _, track := range plan.Tracks {
			if track.Protocol != "m3u8_native" {
				continue
			}
			discovery, err := operation.hlsDiscontinuityGroupsForSelection(ctx, track)
			if err != nil {
				return nil, classifyHLSDiscontinuityDiscoveryError(err)
			}
			if initialPlaylists != nil {
				initialPlaylists[track.URL] = hls.InitialPlaylist{
					URL:  discovery.InitialPlaylist.URL,
					Body: append([]byte(nil), discovery.InitialPlaylist.Body...),
				}
			}
			groups := discovery.Groups
			if len(groups) == 0 {
				if len(explicit) > 0 {
					return nil, fmt.Errorf("%w: %w", ErrHLSDiscontinuityPlaylistEmpty, hls.ErrNoSelectableGroup)
				}
				return nil, hls.ErrNoSelectableGroup
			}

			selectedGroups := make([]hls.DiscontinuityGroup, 0, len(explicit))
			if len(explicit) == 0 {
				for _, group := range groups {
					if group.Selectable {
						selectedGroups = append(selectedGroups, group)
						break
					}
				}
				if len(selectedGroups) == 0 {
					return nil, hls.ErrNoSelectableGroup
				}
			} else {
				requested := make(map[int64]struct{}, len(explicit))
				for _, sequence := range explicit {
					requested[sequence] = struct{}{}
				}
				found := make(map[int64]struct{}, len(explicit))
				for _, group := range groups {
					if _, wanted := requested[group.ID.DiscontinuitySequence]; !wanted {
						continue
					}
					found[group.ID.DiscontinuitySequence] = struct{}{}
					if !group.Selectable {
						return nil, fmt.Errorf("%w: sequence %d", ErrHLSDiscontinuityGroupAdOnly, group.ID.DiscontinuitySequence)
					}
					selectedGroups = append(selectedGroups, group)
				}
				for _, sequence := range explicit {
					if _, exists := found[sequence]; !exists {
						return nil, fmt.Errorf("%w: sequence %d", ErrHLSDiscontinuityGroupMissing, sequence)
					}
				}
			}
			for _, group := range selectedGroups {
				result = append(result, clonePlanForHLSDiscontinuityGroup(plan, group.ID, len(selectedGroups) > 1))
			}
			selectedHLS = true
			break
		}
		if !selectedHLS {
			if len(explicit) > 0 {
				return nil, fmt.Errorf("%w: selected format is not native HLS", ErrHLSDiscontinuitySelection)
			}
			result = append(result, plan)
		}
	}
	return result, nil
}
