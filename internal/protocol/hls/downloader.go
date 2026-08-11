package hls

import (
	"context"
	"crypto/sha256"
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
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/fragment"
	"github.com/tejasa97/youtube_dlp/internal/network"
)

type Transport interface {
	Do(context.Context, *http.Request) (*http.Response, error)
	ReadPage(context.Context, string) ([]byte, http.Header, error)
}

type Config struct {
	Headers http.Header
	// InitialPlaylist supplies one already-fetched bounded media-playlist
	// snapshot. It is used only for the initial load; live polling and every
	// subsequent reload remain network-backed. The body is copied by
	// NewDownloader so callers may release or reuse their buffer.
	InitialPlaylist *InitialPlaylist
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
	// Checkpoint opts a finite VOD playlist into caller-owned fragment resume.
	// It is deliberately ignored for a playlist that was not finite at the
	// initial snapshot: live HLS remains within-run only.
	Checkpoint *fragment.Checkpoint
	// RequireVODCheckpoint rejects a non-ENDLIST initial snapshot when a
	// checkpoint was supplied. Session callers set it so live HLS cannot leave
	// legacy destination-derived artifacts in a durable workspace.
	RequireVODCheckpoint bool
	// EquivalenceProof supplies an explicit URL-free proof for a VOD fragment.
	// A nil callback (or no proof) conservatively disables cross-refresh reuse.
	// The returned Scale's Key is ignored; the downloader owns the structural
	// key and accepts only its Kind, Value, and Scope.
	EquivalenceProof func(FragmentIdentity) (fragment.Scale, bool)
	// StableKeyIdentity is the extractor-owned, non-secret identity of an
	// encrypted HLS key epoch. Without it encrypted fragments cannot safely
	// cross a refreshed key URL and always restart.
	StableKeyIdentity func(FragmentIdentity) (string, bool)
	// RepresentationIdentity is a bounded provider/selection identity supplied
	// by the coordinator. It binds rendition/track selection into structural
	// keys; it is never inferred from a URL.
	RepresentationIdentity string
	// SelectedDiscontinuityGroup restricts this downloader to one absolute
	// EXT-X-DISCONTINUITY-SEQUENCE group. Nil preserves the existing behavior
	// of accumulating every group in the selected representation. A selected
	// group that is absent from a live snapshot contributes no segments; the
	// normal poll/no-segments bounds remain in force.
	SelectedDiscontinuityGroup *DiscontinuityGroupID
}

// FragmentIdentity is the bounded structural identity supplied to a VOD
// remote-equivalence proof callback. It intentionally omits every URI,
// request header, cookie, key URI, and AES key byte.
type FragmentIdentity struct {
	Map                    bool
	DiscontinuitySequence  int64
	MediaSequence          int64
	PartIndex              int
	Partial                bool
	RangeStart             int64
	RangeLength            int64
	DurationNanos          int64
	MapOrdinal             int
	KeyDeclaration         int64
	Encrypted              bool
	RepresentationIdentity string
	// Playlist and selected-rendition facts are the bounded URL-free HLS
	// representation model. They are hashed into the stable fragment key, so
	// a selected stream's codec/audio relationship or playlist epoch cannot be
	// mistaken for the old representation after signed URL rotation.
	PlaylistVersion               int
	PlaylistMediaSequence         int64
	PlaylistDiscontinuitySequence int64
	SelectedBandwidth             int64
	SelectedCodecs                string
	SelectedResolution            string
	SelectedAudioGroup            string
	SelectedAudioLanguage         string
	SelectedDiscontinuityGroup    int64
	StableKeyIdentity             string
	CanonicalComplete             bool
	// Encryption is the method/IV declaration only. It never contains a key
	// URI or AES key material and is included to make an encryption-declaration
	// change invalidate the structural key.
	Encryption string
}

type selectedRendition struct {
	Bandwidth  int64
	Codecs     string
	Resolution string
	AudioGroup string
	Language   string
}

// InitialPlaylist is a bounded media-playlist snapshot that can be reused as
// the initial load of one or more independent downloader instances. URL is the
// canonical media-playlist URL used to resolve relative segment references and
// to seed live reloads. Master-playlist selection remains network-backed unless
// the caller supplies the selected media playlist itself.
type InitialPlaylist struct {
	URL  string
	Body []byte
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
	if config.InitialPlaylist != nil {
		initial := *config.InitialPlaylist
		initial.Body = append([]byte(nil), initial.Body...)
		config.InitialPlaylist = &initial
	}
	if config.SelectedDiscontinuityGroup != nil {
		selected := *config.SelectedDiscontinuityGroup
		config.SelectedDiscontinuityGroup = &selected
	}
	if config.Checkpoint != nil {
		checkpoint := *config.Checkpoint
		if checkpoint.ResumeBoundary != nil {
			boundary := *checkpoint.ResumeBoundary
			checkpoint.ResumeBoundary = &boundary
		}
		config.Checkpoint = &checkpoint
		if !validRepresentationIdentity(config.RepresentationIdentity) {
			// Keep the invalid value so Download can reject before filesystem
			// work, rather than silently changing the representation scope.
			config.RepresentationIdentity = ""
		}
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
	mediaURL, media, selected, err := downloader.loadMedia(ctx, manifestURL)
	if err != nil {
		return fragment.Result{}, err
	}
	media, err = selectMediaPlaylistGroup(media, downloader.config.SelectedDiscontinuityGroup)
	if err != nil {
		return fragment.Result{}, err
	}
	// Only an EXT-X-ENDLIST snapshot is a finite VOD representation. A live
	// playlist may be stable enough to finish within this process, but it has
	// no cross-restart availability epoch contract and must not retain durable
	// fragment authority.
	initialVOD := media.EndList
	if downloader.config.Checkpoint != nil && downloader.config.RequireVODCheckpoint && !initialVOD {
		return fragment.Result{}, ErrVODCheckpointRequired
	}
	if downloader.config.Checkpoint != nil && downloader.config.RepresentationIdentity == "" {
		return fragment.Result{}, ErrInvalidPlaylist
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
	mapOrdinal := 0
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
			mapKey := mapIdentity{
				url: segment.Map.URL, rangeStart: segment.Map.RangeStart, rangeLength: segment.Map.RangeLength,
				iv: hex.EncodeToString(mapIV), epoch: key.epoch, discontinuity: segment.DiscontinuitySequence,
			}
			if segment.Map.Key != nil {
				mapKey.keyURL = segment.Map.Key.URL
				mapKey.keyDeclaration = segment.Map.Key.Declaration
				mapKey.keySnapshot = segment.Map.Key.snapshot
			}
			if segment.Discontinuity || segment.MapDeclared || lastMap == nil || *lastMap != mapKey {
				encryption, err := loadEncryption(segment.Map.Key, segment.Sequence)
				if err != nil {
					return fragment.Result{}, err
				}
				mapOrdinal++
				fragmentIdentity := downloader.fragmentIdentity(media, selected, FragmentIdentity{
					Map: true, DiscontinuitySequence: segment.DiscontinuitySequence,
					MediaSequence: segment.Sequence, RangeStart: segment.Map.RangeStart,
					RangeLength: segment.Map.RangeLength, MapOrdinal: mapOrdinal, KeyDeclaration: hlsKeyDeclaration(segment.Map.Key),
					Encrypted: segment.Map.Key != nil, Encryption: hlsEncryptionDeclaration(segment.Map.Key, segment.Sequence),
				})
				plan = append(plan, fragment.Segment{
					URL: segment.Map.URL, RangeStart: segment.Map.RangeStart,
					RangeLength: segment.Map.RangeLength, AES128: encryption,
					Scale: downloader.vodScale(fragmentIdentity),
				})
				mapKeyCopy := mapKey
				lastMap = &mapKeyCopy
			}
		}
		identity := downloader.fragmentIdentity(media, selected, FragmentIdentity{
			DiscontinuitySequence: segment.DiscontinuitySequence, MediaSequence: segment.Sequence,
			PartIndex: segment.PartIndex, Partial: segment.Partial, RangeStart: segment.RangeStart,
			RangeLength: segment.RangeLength, DurationNanos: segment.Duration.Nanoseconds(),
			MapOrdinal: mapOrdinal, KeyDeclaration: hlsKeyDeclaration(segment.Key), Encrypted: segment.Key != nil, Encryption: hlsEncryptionDeclaration(segment.Key, segment.Sequence),
		})
		planned := fragment.Segment{URL: segment.URL, RangeStart: segment.RangeStart, RangeLength: segment.RangeLength, Scale: downloader.vodScale(identity)}
		planned.AES128, err = loadEncryption(segment.Key, segment.Sequence)
		if err != nil {
			return fragment.Result{}, err
		}
		plan = append(plan, planned)
	}
	job := fragment.Job{
		Segments: plan, Headers: downloader.config.Headers, OutputRoot: outputRoot, Destination: destination,
		Concurrency: downloader.config.FragmentConcurrency, PerHostConcurrency: downloader.config.PerHostConcurrency,
		MaxSegments: downloader.config.MaxSegments, MaxSegmentSize: downloader.config.MaxSegmentSize,
		Attempts: downloader.config.Attempts, RetryBaseDelay: downloader.config.RetryBaseDelay,
		RetryMaxDelay: downloader.config.RetryMaxDelay, Overwrite: overwrite,
	}
	if initialVOD && downloader.config.Checkpoint != nil {
		checkpoint := *downloader.config.Checkpoint
		job.Checkpoint = &checkpoint
	} else {
		// Scales are only meaningful inside a durable VOD ledger. Avoid
		// changing legacy plan hashes or retaining live HLS state.
		for index := range job.Segments {
			job.Segments[index].Scale = nil
		}
	}
	return fragment.New(downloader.transport).Download(ctx, job, sink)
}

func (downloader *Downloader) vodScale(identity FragmentIdentity) *fragment.Scale {
	if !identity.CanonicalComplete {
		// Do not hash malformed untrusted playlist metadata. In particular, a
		// value that looks like a URI must never become a persisted URL-derived
		// digest. The proof-less sentinel forces a complete safe restart.
		return hlsSafeRestartScale("incomplete")
	}
	if identity.Encrypted {
		if downloader.config.StableKeyIdentity == nil {
			return hlsSafeRestartScale("encrypted-unproven")
		}
		keyIdentity, ok := downloader.config.StableKeyIdentity(identity)
		if !ok || !validRepresentationIdentity(keyIdentity) {
			return hlsSafeRestartScale("encrypted-unproven")
		}
		identity.StableKeyIdentity = keyIdentity
	}
	representationDigest := sha256.Sum256([]byte(hlsRepresentationCanonical(identity)))
	representation := hex.EncodeToString(representationDigest[:])
	segmentDigest := sha256.Sum256([]byte(hlsFragmentCanonical(identity)))
	key := hex.EncodeToString(segmentDigest[:])
	scale := &fragment.Scale{Key: key, Scope: representation}
	if downloader.config.EquivalenceProof == nil {
		return scale
	}
	if proof, ok := downloader.config.EquivalenceProof(identity); ok {
		if validHLSProof(proof) {
			scale.Kind, scale.Value, scale.Scope = proof.Kind, proof.Value, proof.Scope
		}
	}
	return scale
}

// A protocol callback is not trusted input for ledger serialization. Invalid
// proof shapes simply withhold reuse authorization; the structural scale then
// makes retained fragments restart as one scope rather than failing playback.
func validHLSProof(proof fragment.Scale) bool {
	if proof.Kind != "provider-immutable" && proof.Kind != "content-identity" {
		return false
	}
	return canonicalHLSProofDigest(proof.Value) && canonicalHLSProofDigest(proof.Scope)
}

func canonicalHLSProofDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func hlsSafeRestartScale(reason string) *fragment.Scale {
	key := sha256.Sum256([]byte("hls-v3-restart-key:" + reason))
	scope := sha256.Sum256([]byte("hls-v3-restart-scope:" + reason))
	return &fragment.Scale{Key: hex.EncodeToString(key[:]), Scope: hex.EncodeToString(scope[:])}
}

func (downloader *Downloader) fragmentIdentity(media *MediaPlaylist, selected selectedRendition, identity FragmentIdentity) FragmentIdentity {
	identity.RepresentationIdentity = downloader.config.RepresentationIdentity
	identity.SelectedBandwidth = selected.Bandwidth
	identity.SelectedCodecs = selected.Codecs
	identity.SelectedResolution = selected.Resolution
	identity.SelectedAudioGroup = selected.AudioGroup
	identity.SelectedAudioLanguage = selected.Language
	if media != nil {
		identity.PlaylistVersion = media.Version
		if identity.PlaylistVersion == 0 {
			identity.PlaylistVersion = 1 // RFC default when EXT-X-VERSION is absent.
		}
		identity.PlaylistMediaSequence = media.Sequence
		identity.PlaylistDiscontinuitySequence = media.DiscontinuitySequence
	}
	identity.SelectedDiscontinuityGroup = -1
	if downloader.config.SelectedDiscontinuityGroup != nil {
		identity.SelectedDiscontinuityGroup = downloader.config.SelectedDiscontinuityGroup.DiscontinuitySequence
	}
	identity.CanonicalComplete = media != nil && media.Sequence >= 0 && media.DiscontinuitySequence >= 0 &&
		validRepresentationIdentity(identity.RepresentationIdentity) && identity.PlaylistVersion > 0 &&
		identity.SelectedDiscontinuityGroup >= -1 && hlsCanonicalFieldsSafe(identity)
	return identity
}

func hlsCanonicalFieldsSafe(identity FragmentIdentity) bool {
	for _, value := range []string{identity.SelectedCodecs, identity.SelectedResolution, identity.SelectedAudioGroup, identity.SelectedAudioLanguage} {
		if !safeHLSCanonicalField(value) {
			return false
		}
	}
	return safeHLSEncryptionDeclaration(identity.Encryption)
}

func safeHLSCanonicalField(value string) bool {
	if len(value) > 256 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	return !strings.ContainsAny(value, ":/\\?@#&=")
}

func safeHLSEncryptionDeclaration(value string) bool {
	if value == "none" {
		return true
	}
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "aes-128" || parts[1] != "identity" || len(parts[2]) != 32 {
		return false
	}
	for _, character := range parts[2] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

// These canonical strings contain no URI, URI-derived hash, credential, key
// URI, or key material. Their SHA-256 digests are the persisted bounded keys.
func hlsRepresentationCanonical(identity FragmentIdentity) string {
	return fmt.Sprintf("v=3|provider=%s|variant-bw=%d|codecs=%s|resolution=%s|audio-group=%s|audio-lang=%s|selected-disc=%d",
		identity.RepresentationIdentity, identity.SelectedBandwidth, identity.SelectedCodecs,
		identity.SelectedResolution, identity.SelectedAudioGroup, identity.SelectedAudioLanguage,
		identity.SelectedDiscontinuityGroup)
}

func hlsFragmentCanonical(identity FragmentIdentity) string {
	return fmt.Sprintf("%s|playlist-v=%d|playlist-msn=%d|playlist-dsn=%d|map=%t|disc=%d|msn=%d|part=%d|partial=%t|range=%d:%d|duration=%d|map-ordinal=%d|key-declaration=%d|encrypted=%t|encryption=%s|stable-key=%s",
		hlsRepresentationCanonical(identity), identity.PlaylistVersion, identity.PlaylistMediaSequence,
		identity.PlaylistDiscontinuitySequence, identity.Map, identity.DiscontinuitySequence,
		identity.MediaSequence, identity.PartIndex, identity.Partial, identity.RangeStart,
		identity.RangeLength, identity.DurationNanos, identity.MapOrdinal, identity.KeyDeclaration, identity.Encrypted,
		identity.Encryption, identity.StableKeyIdentity)
}

func hlsKeyDeclaration(key *Key) int64 {
	if key == nil {
		return 0
	}
	return key.Declaration
}

func validRepresentationIdentity(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return value != "." && value != ".."
}

func hlsEncryptionDeclaration(key *Key, sequence int64) string {
	if key == nil {
		return "none"
	}
	iv := append([]byte(nil), key.IV...)
	if len(iv) == 0 {
		iv = make([]byte, 16)
		binary.BigEndian.PutUint64(iv[8:], uint64(sequence))
	}
	format := strings.ToLower(key.KeyFormat)
	if format == "" {
		format = "identity"
	}
	return strings.ToLower(key.Method) + ":" + format + ":" + hex.EncodeToString(iv)
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

func (downloader *Downloader) loadMedia(ctx context.Context, manifestURL string) (string, *MediaPlaylist, selectedRendition, error) {
	if initial := downloader.config.InitialPlaylist; initial != nil {
		if initial.URL == "" || len(initial.Body) > maxPlaylistBytes {
			return "", nil, selectedRendition{}, ErrInvalidPlaylist
		}
		if err := downloader.validateURL(initial.URL); err != nil {
			return "", nil, selectedRendition{}, err
		}
		playlist, err := Parse(initial.URL, initial.Body)
		if err != nil || playlist.Media == nil {
			return "", nil, selectedRendition{}, errors.Join(err, ErrInvalidPlaylist)
		}
		if err := downloader.validatePlaylistURLs(playlist); err != nil {
			return "", nil, selectedRendition{}, err
		}
		return initial.URL, playlist.Media, selectedRendition{}, nil
	}
	if err := downloader.validateURL(manifestURL); err != nil {
		return "", nil, selectedRendition{}, err
	}
	body, _, err := downloader.readPage(ctx, manifestURL)
	if err != nil {
		return "", nil, selectedRendition{}, err
	}
	playlist, err := Parse(manifestURL, body)
	if err != nil {
		annotateEncryptionMediaURL(err, manifestURL)
		return "", nil, selectedRendition{}, err
	}
	if playlist.Media != nil {
		if err := downloader.validatePlaylistURLs(playlist); err != nil {
			return "", nil, selectedRendition{}, err
		}
		return manifestURL, playlist.Media, selectedRendition{}, nil
	}
	if len(playlist.Variants) == 0 {
		return "", nil, selectedRendition{}, ErrInvalidPlaylist
	}
	if err := downloader.validatePlaylistURLs(playlist); err != nil {
		return "", nil, selectedRendition{}, err
	}
	selected := playlist.Variants[0]
	for _, variant := range playlist.Variants[1:] {
		if variant.Bandwidth > selected.Bandwidth {
			selected = variant
		}
	}
	selectedFacts := selectRendition(playlist, selected)
	body, _, err = downloader.readPage(ctx, selected.URL)
	if err != nil {
		return "", nil, selectedRendition{}, err
	}
	playlist, err = Parse(selected.URL, body)
	if err != nil || playlist.Media == nil {
		annotateEncryptionMediaURL(err, selected.URL)
		return "", nil, selectedRendition{}, errors.Join(err, ErrInvalidPlaylist)
	}
	if err := downloader.validatePlaylistURLs(playlist); err != nil {
		return "", nil, selectedRendition{}, err
	}
	return selected.URL, playlist.Media, selectedFacts, nil
}

func selectRendition(playlist Playlist, variant Variant) selectedRendition {
	selected := selectedRendition{Bandwidth: variant.Bandwidth, Codecs: variant.Codecs, Resolution: variant.Resolution, AudioGroup: variant.AudioGroup}
	if variant.AudioGroup == "" {
		return selected
	}
	for _, rendition := range playlist.Renditions {
		if rendition.Type == "AUDIO" && rendition.GroupID == variant.AudioGroup {
			selected.Language = rendition.Language
			break
		}
	}
	return selected
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
