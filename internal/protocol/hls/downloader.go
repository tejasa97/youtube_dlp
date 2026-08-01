package hls

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

type Config struct {
	Headers http.Header
	// AllowedHosts is an optional extractor-owned HTTPS host policy. When set,
	// every manifest, variant, segment, key, map, preload hint, and rendition
	// report URI must remain on one of these exact DNS zones or subdomains. The
	// suffix form is intentional: Twitch uses distinct edge hostnames within a
	// small CDN zone, and the dot boundary prevents look-alike hosts.
	AllowedHosts        []string
	PollInterval        time.Duration
	MaxPolls            int
	FragmentConcurrency int
	PerHostConcurrency  int
	MaxSegments         int
	MaxSegmentSize      int64
	Attempts            int
	RetryBaseDelay      time.Duration
	RetryMaxDelay       time.Duration
	URLValidator        func(string) error
	// SelectedDiscontinuityGroup restricts this downloader to one absolute
	// EXT-X-DISCONTINUITY-SEQUENCE group. Nil preserves the existing behavior
	// of accumulating every group in the selected representation. A selected
	// group that is absent from a live snapshot contributes no segments; the
	// normal poll/no-segments bounds remain in force.
	SelectedDiscontinuityGroup *DiscontinuityGroupID
}

type Downloader struct {
	transport Transport
	config    Config
}

// segmentKey is a physical HLS identity. Sequence numbers are only unique
// within a discontinuity epoch; live origins are permitted to restart them.
// Part completion intentionally shares the same epoch/sequence identity so a
// complete segment atomically replaces its previously advertised parts.
type segmentKey struct {
	epoch, discontinuity, sequence int64
	part                           int
	partial                        bool
}

type playlistContext struct {
	mapValue *Map
	keyValue *Key
}

type keyIdentity struct {
	url         string
	declaration int64
	snapshot    uint64
}

func NewDownloader(transport Transport, config Config) *Downloader {
	config.Headers = config.Headers.Clone()
	if config.SelectedDiscontinuityGroup != nil {
		selected := *config.SelectedDiscontinuityGroup
		config.SelectedDiscontinuityGroup = &selected
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.MaxPolls <= 0 {
		config.MaxPolls = 120
	}
	return &Downloader{transport: transport, config: config}
}

func (downloader *Downloader) Download(ctx context.Context, manifestURL, outputRoot, destination string, overwrite bool, sink events.Sink) (fragment.Result, error) {
	mediaURL, media, err := downloader.loadMedia(ctx, manifestURL)
	if err != nil {
		return fragment.Result{}, err
	}
	media, err = selectMediaPlaylistGroup(media, downloader.config.SelectedDiscontinuityGroup)
	if err != nil {
		return fragment.Result{}, err
	}
	segments := make(map[segmentKey]Segment)
	complete := make(map[segmentKey]bool)
	var contextState playlistContext
	var previous *MediaPlaylist
	epoch := int64(0)
	snapshot := uint64(0)
	polls := 0
	for {
		polls++
		if snapshot == ^uint64(0) {
			return fragment.Result{}, fmt.Errorf("%w: live snapshot overflow", ErrInvalidPlaylist)
		}
		snapshot++
		if previous != nil && playlistSequenceReset(previous, media) {
			if epoch == int64(^uint64(0)>>1) {
				return fragment.Result{}, fmt.Errorf("%w: live epoch overflow", ErrInvalidPlaylist)
			}
			epoch++
			// A new live epoch cannot inherit a previous epoch's crypto or map
			// state. This avoids publishing plausibly decrypted but corrupt bytes.
			contextState = playlistContext{}
		}
		inheritPlaylistContext(media, &contextState)
		if err := downloader.validateMediaPlaylist(media); err != nil {
			return fragment.Result{}, err
		}
		stampKeySnapshot(media, snapshot)
		if err := downloader.captureSnapshotKeyMaterial(ctx, media); err != nil {
			return fragment.Result{}, err
		}
		rememberPlaylistContext(media, &contextState)
		for _, segment := range media.Segments {
			base := segmentKey{epoch: epoch, discontinuity: segment.DiscontinuitySequence, sequence: segment.Sequence}
			if segment.Partial {
				completeKey := base
				if !complete[completeKey] {
					partKey := base
					partKey.part, partKey.partial = segment.PartIndex, true
					if existing, exists := segments[partKey]; !exists || (existing.Advertisement && !segment.Advertisement) {
						segments[partKey] = segment
					}
				}
				continue
			}
			complete[base] = true
			for key := range segments {
				if key.epoch == base.epoch && key.discontinuity == base.discontinuity && key.sequence == base.sequence && key.partial {
					delete(segments, key)
				}
			}
			if existing, exists := segments[base]; !exists || (existing.Advertisement && !segment.Advertisement) {
				segments[base] = segment
			}
		}
		if media.EndList {
			break
		}
		if polls >= downloader.config.MaxPolls {
			return fragment.Result{}, ErrLivePollLimit
		}
		timer := time.NewTimer(downloader.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fragment.Result{}, ctx.Err()
		case <-timer.C:
		}
		previous = media
		reloadURL := liveReloadURL(mediaURL, media)
		body, _, err := downloader.readPage(ctx, reloadURL)
		if err != nil && reloadURL != mediaURL && isUnsupportedBlockingReload(err) {
			// Servers that advertise blocking reload but reject delivery
			// directives remain readable through the ordinary playlist URL.
			body, _, err = downloader.readPage(ctx, mediaURL)
		}
		if err != nil {
			return fragment.Result{}, err
		}
		parsed, err := Parse(mediaURL, body)
		if err != nil || parsed.Media == nil {
			return fragment.Result{}, errors.Join(err, ErrInvalidPlaylist)
		}
		media, err = selectMediaPlaylistGroup(parsed.Media, downloader.config.SelectedDiscontinuityGroup)
		if err != nil {
			return fragment.Result{}, err
		}
		if err := downloader.validatePlaylistURLs(parsed); err != nil {
			return fragment.Result{}, err
		}
	}

	keys := make([]segmentKey, 0, len(segments))
	for key := range segments {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].epoch != keys[right].epoch {
			return keys[left].epoch < keys[right].epoch
		}
		if keys[left].discontinuity != keys[right].discontinuity {
			return keys[left].discontinuity < keys[right].discontinuity
		}
		if keys[left].sequence != keys[right].sequence {
			return keys[left].sequence < keys[right].sequence
		}
		if keys[left].partial != keys[right].partial {
			return !keys[left].partial
		}
		return keys[left].part < keys[right].part
	})
	type mapIdentity struct {
		url, keyURL, iv                      string
		epoch, discontinuity, keyDeclaration int64
		keySnapshot                          uint64
		rangeStart, rangeLength              int64
	}
	var lastMap *mapIdentity
	loadEncryption := func(key *Key, sequence int64) (*fragment.AES128, error) {
		if key == nil {
			return nil, nil
		}
		if len(key.material) != 16 {
			return nil, fmt.Errorf("%w: AES-128 key material was not captured", ErrInvalidPlaylist)
		}
		iv := append([]byte(nil), key.IV...)
		if len(iv) == 0 {
			iv = make([]byte, 16)
			binary.BigEndian.PutUint64(iv[8:], uint64(sequence))
		}
		return &fragment.AES128{Key: append([]byte(nil), key.material...), IV: iv}, nil
	}
	var plan []fragment.Segment
	for _, key := range keys {
		segment := segments[key]
		if segment.Advertisement {
			continue
		}
		if segment.Map == nil {
			lastMap = nil
		} else {
			var mapIV []byte
			if segment.Map.Key != nil {
				mapIV = segment.Map.Key.IV
			}
			identity := mapIdentity{
				url: segment.Map.URL, rangeStart: segment.Map.RangeStart, rangeLength: segment.Map.RangeLength,
				iv: hex.EncodeToString(mapIV), epoch: key.epoch, discontinuity: segment.DiscontinuitySequence,
			}
			if segment.Map.Key != nil {
				identity.keyURL = segment.Map.Key.URL
				identity.keyDeclaration = segment.Map.Key.Declaration
				identity.keySnapshot = segment.Map.Key.snapshot
			}
			if segment.Discontinuity || segment.MapDeclared || lastMap == nil || *lastMap != identity {
				encryption, err := loadEncryption(segment.Map.Key, segment.Sequence)
				if err != nil {
					return fragment.Result{}, err
				}
				plan = append(plan, fragment.Segment{
					URL: segment.Map.URL, RangeStart: segment.Map.RangeStart,
					RangeLength: segment.Map.RangeLength, AES128: encryption,
				})
				identityCopy := identity
				lastMap = &identityCopy
			}
		}
		planned := fragment.Segment{URL: segment.URL, RangeStart: segment.RangeStart, RangeLength: segment.RangeLength}
		planned.AES128, err = loadEncryption(segment.Key, segment.Sequence)
		if err != nil {
			return fragment.Result{}, err
		}
		plan = append(plan, planned)
	}
	return fragment.New(downloader.transport).Download(ctx, fragment.Job{
		Segments: plan, Headers: downloader.config.Headers, OutputRoot: outputRoot, Destination: destination,
		Concurrency: downloader.config.FragmentConcurrency, PerHostConcurrency: downloader.config.PerHostConcurrency,
		MaxSegments: downloader.config.MaxSegments, MaxSegmentSize: downloader.config.MaxSegmentSize,
		Attempts: downloader.config.Attempts, RetryBaseDelay: downloader.config.RetryBaseDelay,
		RetryMaxDelay: downloader.config.RetryMaxDelay, Overwrite: overwrite,
	}, sink)
}

func inheritPlaylistContext(media *MediaPlaylist, state *playlistContext) {
	if media == nil || state == nil {
		return
	}
	for index := range media.Segments {
		segment := &media.Segments[index]
		if segment.Map == nil {
			segment.Map = cloneMap(state.mapValue)
			segment.MapInherited = segment.Map != nil
		} else {
			state.mapValue = cloneMap(segment.Map)
		}
		if !segment.KeyDeclared {
			segment.Key = cloneKey(state.keyValue)
		} else {
			state.keyValue = cloneKey(segment.Key)
		}
	}
}

func rememberPlaylistContext(media *MediaPlaylist, state *playlistContext) {
	if media == nil || state == nil {
		return
	}
	for index := range media.Segments {
		segment := &media.Segments[index]
		if segment.Map != nil && !segment.MapInherited {
			state.mapValue = cloneMap(segment.Map)
		}
		if segment.KeyDeclared {
			state.keyValue = cloneKey(segment.Key)
		}
	}
}

// captureSnapshotKeyMaterial reads each retained AES-128 key while its
// manifest snapshot is current. Final assembly must never re-fetch key bytes:
// a live key URI may legitimately serve a later rotation by then.
func (downloader *Downloader) captureSnapshotKeyMaterial(ctx context.Context, media *MediaPlaylist) error {
	if media == nil {
		return nil
	}
	type capturedKey struct {
		key     *Key
		aliases []*Key
	}
	index := make(map[keyIdentity]int)
	keys := make([]capturedKey, 0, 4)
	add := func(key *Key) {
		if key == nil || key.Method != "AES-128" {
			return
		}
		identity := keyIdentity{url: key.URL, declaration: key.Declaration, snapshot: key.snapshot}
		if existing, ok := index[identity]; ok {
			keys[existing].aliases = append(keys[existing].aliases, key)
			return
		}
		index[identity] = len(keys)
		keys = append(keys, capturedKey{key: key, aliases: []*Key{key}})
	}
	for index := range media.Segments {
		segment := &media.Segments[index]
		if segment.Advertisement {
			continue
		}
		if segment.Map != nil {
			add(segment.Map.Key)
		}
		add(segment.Key)
	}
	for _, declaration := range keys {
		key := declaration.key
		if len(key.material) == 0 {
			body, _, err := downloader.readPage(ctx, key.URL)
			if err != nil {
				return err
			}
			if len(body) != 16 {
				return fmt.Errorf("AES-128 key length = %d, want 16", len(body))
			}
			key.material = append([]byte(nil), body...)
		}
		for _, alias := range declaration.aliases {
			alias.material = append([]byte(nil), key.material...)
		}
	}
	return nil
}

// stampKeySnapshot makes a parsed playlist's key declaration local to that
// snapshot. The parser's declaration ordinal starts at one for each Parse, so
// it is not itself a cache identity across live reloads. Reusing a key URI is
// legal; reusing old key bytes for a later declaration is not.
func stampKeySnapshot(media *MediaPlaylist, snapshot uint64) {
	if media == nil {
		return
	}
	for index := range media.Segments {
		segment := &media.Segments[index]
		if segment.Key != nil && segment.KeyDeclared {
			segment.Key.snapshot = snapshot
		}
		if segment.Map != nil && segment.Map.Key != nil && !segment.MapInherited {
			segment.Map.Key.snapshot = snapshot
		}
	}
}

func playlistSequenceReset(previous, current *MediaPlaylist) bool {
	if previous == nil || current == nil || len(previous.Segments) == 0 || len(current.Segments) == 0 {
		return false
	}
	if current.DiscontinuitySequence < previous.DiscontinuitySequence {
		return true
	}
	// An origin can restart at the same (or an overlapping) media sequence.
	// Sequence alone is not a physical identity: accepting a different object
	// under the old key would silently publish stale media. Complete segments
	// and LL-HLS parts intentionally have separate logical identities, so a
	// normal part-to-complete replacement remains within the same epoch.
	type logicalIdentity struct {
		discontinuity, sequence int64
		part                    int
		partial                 bool
	}
	previousSegments := make(map[logicalIdentity]Segment, len(previous.Segments))
	for _, segment := range previous.Segments {
		previousSegments[logicalIdentity{
			discontinuity: segment.DiscontinuitySequence,
			sequence:      segment.Sequence,
			part:          segment.PartIndex,
			partial:       segment.Partial,
		}] = segment
	}
	for _, segment := range current.Segments {
		identity := logicalIdentity{
			discontinuity: segment.DiscontinuitySequence,
			sequence:      segment.Sequence,
			part:          segment.PartIndex,
			partial:       segment.Partial,
		}
		if old, overlaps := previousSegments[identity]; overlaps &&
			(old.URL != segment.URL || old.RangeStart != segment.RangeStart || old.RangeLength != segment.RangeLength) {
			return true
		}
	}
	previousMin, previousMax := previous.Segments[0].Sequence, previous.Segments[0].Sequence
	currentMin, currentMax := current.Segments[0].Sequence, current.Segments[0].Sequence
	for _, segment := range previous.Segments[1:] {
		if segment.Sequence < previousMin {
			previousMin = segment.Sequence
		}
		if segment.Sequence > previousMax {
			previousMax = segment.Sequence
		}
	}
	for _, segment := range current.Segments[1:] {
		if segment.Sequence < currentMin {
			currentMin = segment.Sequence
		}
		if segment.Sequence > currentMax {
			currentMax = segment.Sequence
		}
	}
	// A sliding window only moves forward. A wholly older window is therefore a
	// source restart, not an ordinary delta or a late playlist response.
	return currentMin < previousMin && currentMax < previousMin
}

func liveReloadURL(rawURL string, media *MediaPlaylist) string {
	if media == nil || !media.CanBlockReload || len(media.Segments) == 0 {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return rawURL
	}
	last := media.Segments[len(media.Segments)-1]
	msn := last.Sequence
	part := -1
	if last.Partial {
		if last.PartIndex == int(^uint(0)>>1) {
			return rawURL
		}
		part = last.PartIndex + 1
	} else {
		if msn == int64(^uint64(0)>>1) {
			return rawURL
		}
		msn++
		if media.PreloadHint != nil {
			part = 0
		}
	}
	query := parsed.Query()
	query.Del("_HLS_msn")
	query.Del("_HLS_part")
	query.Set("_HLS_msn", strconv.FormatInt(msn, 10))
	if part >= 0 {
		query.Set("_HLS_part", strconv.Itoa(part))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func isUnsupportedBlockingReload(err error) bool {
	var status *network.StatusError
	return errors.As(err, &status) && (status.Code == http.StatusBadRequest || status.Code == http.StatusNotFound || status.Code == http.StatusNotImplemented)
}

func (downloader *Downloader) loadMedia(ctx context.Context, manifestURL string) (string, *MediaPlaylist, error) {
	if err := downloader.validateURL(manifestURL); err != nil {
		return "", nil, err
	}
	body, _, err := downloader.readPage(ctx, manifestURL)
	if err != nil {
		return "", nil, err
	}
	playlist, err := Parse(manifestURL, body)
	if err != nil {
		annotateEncryptionMediaURL(err, manifestURL)
		return "", nil, err
	}
	if playlist.Media != nil {
		if err := downloader.validatePlaylistURLs(playlist); err != nil {
			return "", nil, err
		}
		return manifestURL, playlist.Media, nil
	}
	if len(playlist.Variants) == 0 {
		return "", nil, ErrInvalidPlaylist
	}
	if err := downloader.validatePlaylistURLs(playlist); err != nil {
		return "", nil, err
	}
	selected := playlist.Variants[0]
	for _, variant := range playlist.Variants[1:] {
		if variant.Bandwidth > selected.Bandwidth {
			selected = variant
		}
	}
	body, _, err = downloader.readPage(ctx, selected.URL)
	if err != nil {
		return "", nil, err
	}
	playlist, err = Parse(selected.URL, body)
	if err != nil || playlist.Media == nil {
		annotateEncryptionMediaURL(err, selected.URL)
		return "", nil, errors.Join(err, ErrInvalidPlaylist)
	}
	if err := downloader.validatePlaylistURLs(playlist); err != nil {
		return "", nil, err
	}
	return selected.URL, playlist.Media, nil
}

func (downloader *Downloader) validateURL(rawURL string) error {
	if len(downloader.config.AllowedHosts) > 0 {
		allowedHosts := make([]string, 0, len(downloader.config.AllowedHosts))
		seen := make(map[string]struct{}, len(downloader.config.AllowedHosts))
		for _, rawHost := range downloader.config.AllowedHosts {
			host, ok := normalizeAllowedHost(rawHost)
			if !ok {
				return ErrInvalidPlaylist
			}
			if _, duplicate := seen[host]; duplicate {
				return ErrInvalidPlaylist
			}
			seen[host] = struct{}{}
			allowedHosts = append(allowedHosts, host)
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
			return ErrInvalidPlaylist
		}
		host, ok := normalizeAllowedHost(parsed.Hostname())
		if !ok {
			return ErrInvalidPlaylist
		}
		allowed := false
		for _, zone := range allowedHosts {
			if host == zone || strings.HasSuffix(host, "."+zone) {
				allowed = true
				break
			}
		}
		if !allowed {
			return ErrInvalidPlaylist
		}
	}
	if downloader.config.URLValidator != nil {
		if err := downloader.config.URLValidator(rawURL); err != nil {
			return fmt.Errorf("%w: URL policy rejected: %w", ErrInvalidPlaylist, err)
		}
	}
	return nil
}

func normalizeAllowedHost(host string) (string, bool) {
	if host == "" || len(host) > 253 || host != strings.TrimSpace(host) || net.ParseIP(host) != nil {
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

func (downloader *Downloader) validatePlaylistURLs(playlist Playlist) error {
	for _, variant := range playlist.Variants {
		if err := downloader.validateURL(variant.URL); err != nil {
			return err
		}
	}
	return downloader.validateMediaPlaylist(playlist.Media)
}

func (downloader *Downloader) validateMediaPlaylist(media *MediaPlaylist) error {
	if media == nil {
		return nil
	}
	if media.PreloadHint != nil {
		if err := downloader.validateURL(media.PreloadHint.URL); err != nil {
			return err
		}
	}
	for _, report := range media.RenditionReports {
		if err := downloader.validateURL(report.URL); err != nil {
			return err
		}
	}
	for _, segment := range media.Segments {
		if err := downloader.validateURL(segment.URL); err != nil {
			return err
		}
		if segment.Key != nil {
			if err := downloader.validateURL(segment.Key.URL); err != nil {
				return err
			}
		}
		if segment.Map != nil {
			if err := downloader.validateURL(segment.Map.URL); err != nil {
				return err
			}
			if segment.Map.Key != nil {
				if err := downloader.validateURL(segment.Map.Key.URL); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func annotateEncryptionMediaURL(err error, mediaURL string) {
	var encryption *EncryptionError
	if errors.As(err, &encryption) {
		encryption.MediaURL = mediaURL
	}
}

func (downloader *Downloader) readPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := downloader.validateURL(rawURL); err != nil {
		return nil, nil, err
	}
	if len(downloader.config.Headers) == 0 {
		return downloader.transport.ReadPage(ctx, rawURL)
	}
	return network.ReadPageWithHeaders(ctx, downloader.transport, rawURL, downloader.config.Headers, maxPlaylistBytes)
}
