package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	hlsDiscontinuityIDMarker    = "__hls_discontinuity_"
	maxHLSDiscontinuityManifest = 16 << 20
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
		return hls.DiscontinuityGroupID{}, false, fmt.Errorf("invalid HLS discontinuity group selection")
	}
	return hls.DiscontinuityGroupID{DiscontinuitySequence: sequence}, true, nil
}

func clonePlanForHLSDiscontinuityGroup(plan mediaformat.OutputPlan, group hls.DiscontinuityGroupID) mediaformat.OutputPlan {
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
	cloned.Metadata = plan.Metadata
	cloned.Metadata.Set("_hls_discontinuity_group", value.Int(group.DiscontinuitySequence))
	if len(cloned.Tracks) == 1 {
		cloned.Metadata.Set("format_id", value.String(cloned.Tracks[0].ID))
	}
	return cloned
}

func (operation *operation) hlsDiscontinuityGroupsForSelection(
	ctx context.Context,
	selection mediaformat.Selection,
) ([]hls.DiscontinuityGroup, error) {
	transport, err := operation.mediaTransport(
		selection.CredentialIsolated,
		selection.CredentialIsolatedReferer,
		selection.HostPolicy,
		selection.Protocol,
	)
	if err != nil {
		return nil, err
	}
	hlsTransport, ok := transport.(hls.Transport)
	if !ok || hlsTransport == nil {
		return nil, errors.New("HLS transport unavailable")
	}
	return operation.readHLSDiscontinuityGroups(ctx, selection.URL, selection.Headers, hlsTransport, selection.AllowedHosts)
}

func (operation *operation) readHLSDiscontinuityGroups(
	ctx context.Context,
	rawURL string,
	headers http.Header,
	transport hls.Transport,
	allowedHosts []string,
) ([]hls.DiscontinuityGroup, error) {
	if !hlsDiscontinuityAllowedURL(rawURL, allowedHosts) {
		return nil, hls.ErrInvalidPlaylist
	}
	if headers == nil {
		headers = make(http.Header)
	}
	body, err := readHLSPageWithHeaders(ctx, transport, rawURL, headers, maxHLSDiscontinuityManifest)
	if err != nil {
		return nil, err
	}
	playlist, err := hls.Parse(rawURL, body)
	if err != nil {
		return nil, err
	}
	if playlist.Media == nil {
		if len(playlist.Variants) == 0 {
			return nil, hls.ErrInvalidPlaylist
		}
		variant := playlist.Variants[0]
		for _, candidate := range playlist.Variants[1:] {
			if candidate.Bandwidth > variant.Bandwidth {
				variant = candidate
			}
		}
		if !hlsDiscontinuityAllowedURL(variant.URL, allowedHosts) {
			return nil, hls.ErrInvalidPlaylist
		}
		body, err = readHLSPageWithHeaders(ctx, transport, variant.URL, headers, maxHLSDiscontinuityManifest)
		if err != nil {
			return nil, err
		}
		playlist, err = hls.Parse(variant.URL, body)
		if err != nil {
			return nil, err
		}
	}
	if playlist.Media == nil {
		return nil, hls.ErrInvalidPlaylist
	}
	return hls.BuildDiscontinuityGroups(playlist.Media)
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

// selectDefaultHLSDiscontinuityPlans discovers groups only after ordinary
// format selection has produced the output plans. Each selected HLS track is
// therefore the sole representation this integration probes; no extractor
// format list is expanded and no implicit multi-output selection is created.
func (operation *operation) selectDefaultHLSDiscontinuityPlans(
	ctx context.Context,
	plans []mediaformat.OutputPlan,
) ([]mediaformat.OutputPlan, error) {
	if len(plans) == 0 {
		return plans, nil
	}
	result := make([]mediaformat.OutputPlan, 0, len(plans))
	for _, plan := range plans {
		hlsTrackCount := 0
		for _, track := range plan.Tracks {
			if track.Protocol == "m3u8_native" {
				hlsTrackCount++
			}
		}
		if hlsTrackCount > 1 {
			return nil, fmt.Errorf("%w: --hls-split-discontinuity does not support output plans with multiple HLS representations", extractor.ErrUnsupported)
		}
		selectedHLS := false
		for _, track := range plan.Tracks {
			if track.Protocol != "m3u8_native" {
				continue
			}
			groups, err := operation.hlsDiscontinuityGroupsForSelection(ctx, track)
			if err != nil {
				return nil, err
			}
			for _, group := range groups {
				if !group.Selectable {
					continue
				}
				result = append(result, clonePlanForHLSDiscontinuityGroup(plan, group.ID))
				selectedHLS = true
				break
			}
			if !selectedHLS {
				return nil, hls.ErrNoSelectableGroup
			}
			break
		}
		if !selectedHLS {
			result = append(result, plan)
		}
	}
	return result, nil
}
