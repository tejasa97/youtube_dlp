package youtubeump

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	checkpointVersion = 1
	// MaxCheckpointSegments bounds committed media segments recorded in one checkpoint.
	MaxCheckpointSegments = 8192
	// MaxCheckpointBytes bounds one checkpoint JSON file on disk.
	MaxCheckpointBytes           = 1 << 20
	maxCheckpointVideoIDBytes    = 128
	maxCheckpointClientVerBytes  = 128
	maxCheckpointXTagsBytes      = 512
	maxCheckpointAudioTrackBytes = 128
	maxCheckpointDigestHexBytes  = sha256.Size * 2
)

// sabrCheckpoint is durable SABR media progress. It must never contain signed
// URLs, PO tokens, playback cookies, visitor data, SABR contexts, or headers.
type sabrCheckpoint struct {
	Version int `json:"v"`

	VideoID         string `json:"video_id,omitempty"`
	ClientName      int32  `json:"client_name"`
	ClientVersion   string `json:"client_version"`
	TrackKind       string `json:"track_kind"`
	Itag            int32  `json:"itag"`
	LastModified    uint64 `json:"last_modified,omitempty"`
	XTags           string `json:"xtags,omitempty"`
	DurationSec     int64  `json:"duration_sec"`
	UstreamerSHA256 string `json:"ustreamer_sha256"`
	AudioTrackID    string `json:"audio_track_id,omitempty"`
	DrcEnabled      bool   `json:"drc_enabled,omitempty"`

	InitWritten    bool                    `json:"init_written"`
	InitDigest     string                  `json:"init_digest,omitempty"`
	InitLength     int64                   `json:"init_length,omitempty"`
	FormatVerified bool                    `json:"format_verified"`
	EndOfTrack     bool                    `json:"end_of_track,omitempty"`
	NextSequence   uint64                  `json:"next_sequence"`
	HasSequence    bool                    `json:"has_sequence"`
	CumulativeMs   int64                   `json:"cumulative_ms"`
	TotalWritten   int64                   `json:"total_written"`
	Segments       []sabrCheckpointSegment `json:"segments"`
}

type sabrCheckpointSegment struct {
	Sequence      uint64 `json:"seq"`
	Digest        string `json:"digest"`
	DurationMs    int64  `json:"duration_ms"`
	StartTimeMs   int64  `json:"start_time_ms"`
	Length        int64  `json:"length"`
	StartTicks    int64  `json:"start_ticks,omitempty"`
	DurationTicks int64  `json:"duration_ticks,omitempty"`
	Timescale     int32  `json:"timescale,omitempty"`
}

type checkpointIdentity struct {
	VideoID         string
	ClientName      int32
	ClientVersion   string
	TrackKind       TrackKind
	Format          FormatID
	DurationSec     int64
	UstreamerSHA256 string
	AudioTrackID    string
	DrcEnabled      bool
}

func checkpointPaths(destination string) (partPath, statePath string) {
	partPath = destination + ".part"
	statePath = partPath + ".json"
	return partPath, statePath
}

// CheckpointPaths returns the deterministic SABR partial and checkpoint paths.
func CheckpointPaths(destination string) (partPath, statePath string) {
	return checkpointPaths(destination)
}

// ValidateOutputPath ensures destination stays inside outputRoot without symlinks.
func ValidateOutputPath(outputRoot, destination string) error {
	return validateDestination(outputRoot, destination)
}

func hashUstreamerConfig(config []byte) string {
	sum := sha256.Sum256(config)
	return hex.EncodeToString(sum[:])
}

// HashUstreamerConfig exposes the checkpoint-compatible SHA-256 hex digest for
// identity equality checks outside the youtubeump package.
func HashUstreamerConfig(config []byte) string {
	return hashUstreamerConfig(config)
}

func validateResumeVideoID(videoID string) error {
	if videoID == "" {
		return fmt.Errorf("%w: missing SABR video id", ErrMissingConfig)
	}
	if len(videoID) > maxCheckpointVideoIDBytes || strings.ContainsAny(videoID, "\x00\r\n") {
		return fmt.Errorf("%w: invalid SABR video id", ErrMissingConfig)
	}
	return nil
}

// ValidateResumeVideoID requires a non-empty, bounded video id for resumable SABR.
func ValidateResumeVideoID(videoID string) error {
	return validateResumeVideoID(videoID)
}

func identityFromConfig(config Config) checkpointIdentity {
	return checkpointIdentity{
		VideoID:         config.VideoID,
		ClientName:      config.ClientInfo.ClientName,
		ClientVersion:   config.ClientInfo.ClientVersion,
		TrackKind:       config.TrackKind,
		Format:          config.Format,
		DurationSec:     config.DurationSec,
		UstreamerSHA256: hashUstreamerConfig(config.UstreamerConfig),
		AudioTrackID:    config.AudioTrackID,
		DrcEnabled:      config.DrcEnabled,
	}
}

func (identity checkpointIdentity) matches(state sabrCheckpoint) bool {
	return state.VideoID == identity.VideoID &&
		state.ClientName == identity.ClientName &&
		state.ClientVersion == identity.ClientVersion &&
		state.TrackKind == string(identity.TrackKind) &&
		state.Itag == identity.Format.Itag &&
		state.LastModified == identity.Format.LastModified &&
		state.XTags == identity.Format.XTags &&
		state.DurationSec == identity.DurationSec &&
		state.UstreamerSHA256 == identity.UstreamerSHA256 &&
		state.AudioTrackID == identity.AudioTrackID &&
		state.DrcEnabled == identity.DrcEnabled
}

func (identity checkpointIdentity) baseCheckpoint() sabrCheckpoint {
	return sabrCheckpoint{
		Version:         checkpointVersion,
		VideoID:         identity.VideoID,
		ClientName:      identity.ClientName,
		ClientVersion:   identity.ClientVersion,
		TrackKind:       string(identity.TrackKind),
		Itag:            identity.Format.Itag,
		LastModified:    identity.Format.LastModified,
		XTags:           identity.Format.XTags,
		DurationSec:     identity.DurationSec,
		UstreamerSHA256: identity.UstreamerSHA256,
		AudioTrackID:    identity.AudioTrackID,
		DrcEnabled:      identity.DrcEnabled,
		Segments:        []sabrCheckpointSegment{},
	}
}

func encodeDigest(digest segmentDigest) string {
	return hex.EncodeToString(digest[:])
}

func decodeDigest(encoded string) (segmentDigest, error) {
	var digest segmentDigest
	if len(encoded) != maxCheckpointDigestHexBytes {
		return digest, fmt.Errorf("%w: digest length", ErrCheckpointInvalid)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) != sha256.Size {
		return digest, fmt.Errorf("%w: digest encoding", ErrCheckpointInvalid)
	}
	copy(digest[:], raw)
	return digest, nil
}

func (assembler *trackAssembler) snapshotCheckpoint(identity checkpointIdentity) (sabrCheckpoint, error) {
	state := identity.baseCheckpoint()
	state.InitWritten = assembler.initWritten
	state.InitDigest = encodeDigest(assembler.initDigest)
	state.InitLength = assembler.initLength
	state.FormatVerified = assembler.formatVerified
	state.EndOfTrack = assembler.endOfTrackDone
	state.NextSequence = assembler.nextSequence
	state.HasSequence = assembler.hasSequence
	state.CumulativeMs = assembler.cumulativeMs
	state.TotalWritten = assembler.totalWritten
	if len(assembler.bufferedRanges) > MaxCheckpointSegments {
		return sabrCheckpoint{}, fmt.Errorf("%w: too many buffered ranges", ErrCheckpointInvalid)
	}
	state.Segments = make([]sabrCheckpointSegment, 0, len(assembler.bufferedRanges))
	for _, buffered := range assembler.bufferedRanges {
		sequence := uint64(buffered.StartSegmentIndex)
		digest, ok := assembler.writtenSequences[sequence]
		if !ok {
			return sabrCheckpoint{}, fmt.Errorf("%w: missing segment digest", ErrCheckpointInvalid)
		}
		state.Segments = append(state.Segments, sabrCheckpointSegment{
			Sequence:      sequence,
			Digest:        encodeDigest(digest),
			DurationMs:    buffered.DurationMs,
			StartTimeMs:   buffered.StartTimeMs,
			Length:        assembler.segmentLengths[sequence],
			StartTicks:    buffered.TimeRange.StartTicks,
			DurationTicks: buffered.TimeRange.DurationTicks,
			Timescale:     buffered.TimeRange.Timescale,
		})
	}
	if !state.InitWritten {
		state.InitDigest = ""
		state.InitLength = 0
	}
	if err := validateCheckpoint(state); err != nil {
		return sabrCheckpoint{}, err
	}
	return state, nil
}

func (assembler *trackAssembler) restoreCheckpoint(state sabrCheckpoint, remaining int64) error {
	if err := validateCheckpoint(state); err != nil {
		return err
	}
	if state.TotalWritten > remaining {
		return fmt.Errorf("%w: restored bytes exceed media bound", ErrCheckpointInvalid)
	}
	assembler.initWritten = state.InitWritten
	assembler.initLength = state.InitLength
	if state.InitWritten {
		digest, err := decodeDigest(state.InitDigest)
		if err != nil {
			return err
		}
		assembler.initDigest = digest
	}
	assembler.formatVerified = state.FormatVerified
	assembler.endOfTrackDone = state.EndOfTrack
	assembler.nextSequence = state.NextSequence
	assembler.hasSequence = state.HasSequence
	assembler.cumulativeMs = state.CumulativeMs
	assembler.totalWritten = state.TotalWritten
	assembler.remaining = remaining - state.TotalWritten
	assembler.bufferedRanges = make([]BufferedRange, 0, len(state.Segments))
	assembler.writtenSequences = make(map[uint64]segmentDigest, len(state.Segments))
	assembler.segmentLengths = make(map[uint64]int64, len(state.Segments))
	for _, segment := range state.Segments {
		digest, err := decodeDigest(segment.Digest)
		if err != nil {
			return err
		}
		assembler.writtenSequences[segment.Sequence] = digest
		assembler.segmentLengths[segment.Sequence] = segment.Length
		startIndex, err := int32SegmentIndex(segment.Sequence)
		if err != nil {
			return err
		}
		assembler.bufferedRanges = append(assembler.bufferedRanges, BufferedRange{
			FormatID:          assembler.format,
			StartTimeMs:       segment.StartTimeMs,
			DurationMs:        segment.DurationMs,
			StartSegmentIndex: startIndex,
			EndSegmentIndex:   startIndex,
			TimeRange: TimeRange{
				StartTicks:    segment.StartTicks,
				DurationTicks: segment.DurationTicks,
				Timescale:     segment.Timescale,
			},
		})
	}
	return nil
}

func validateCheckpoint(state sabrCheckpoint) error {
	if state.Version != checkpointVersion {
		return fmt.Errorf("%w: unsupported version", ErrCheckpointInvalid)
	}
	if state.VideoID == "" || state.ClientName == 0 || state.ClientVersion == "" || state.Itag == 0 || state.DurationSec <= 0 {
		return fmt.Errorf("%w: incomplete identity", ErrCheckpointInvalid)
	}
	if state.TrackKind != string(TrackAudio) && state.TrackKind != string(TrackVideo) {
		return fmt.Errorf("%w: track kind", ErrCheckpointInvalid)
	}
	if state.UstreamerSHA256 == "" || len(state.UstreamerSHA256) != maxCheckpointDigestHexBytes {
		return fmt.Errorf("%w: ustreamer hash", ErrCheckpointInvalid)
	}
	if _, err := hex.DecodeString(state.UstreamerSHA256); err != nil {
		return fmt.Errorf("%w: ustreamer hash encoding", ErrCheckpointInvalid)
	}
	if len(state.VideoID) > maxCheckpointVideoIDBytes ||
		len(state.ClientVersion) > maxCheckpointClientVerBytes ||
		len(state.XTags) > maxCheckpointXTagsBytes ||
		len(state.AudioTrackID) > maxCheckpointAudioTrackBytes ||
		strings.ContainsAny(state.VideoID, "\x00\r\n") {
		return fmt.Errorf("%w: identity field too large", ErrCheckpointInvalid)
	}
	if len(state.Segments) > MaxCheckpointSegments {
		return fmt.Errorf("%w: too many segments", ErrCheckpointInvalid)
	}
	if state.TotalWritten < 0 || state.CumulativeMs < 0 || state.InitLength < 0 {
		return fmt.Errorf("%w: negative counters", ErrCheckpointInvalid)
	}
	if state.TotalWritten > MaxMediaBytes {
		return fmt.Errorf("%w: total written bound", ErrCheckpointInvalid)
	}
	var accounted int64
	if state.InitWritten {
		if state.InitLength <= 0 || state.InitDigest == "" {
			return fmt.Errorf("%w: init metadata", ErrCheckpointInvalid)
		}
		if _, err := decodeDigest(state.InitDigest); err != nil {
			return err
		}
		accounted += state.InitLength
	} else if state.InitLength != 0 || state.InitDigest != "" || len(state.Segments) > 0 || state.EndOfTrack {
		return fmt.Errorf("%w: init state", ErrCheckpointInvalid)
	}
	seen := make(map[uint64]struct{}, len(state.Segments))
	var cumulative int64
	for index, segment := range state.Segments {
		if segment.Sequence != uint64(index) {
			return fmt.Errorf("%w: non-contiguous sequence at %d", ErrCheckpointInvalid, index)
		}
		if segment.Length <= 0 || segment.DurationMs <= 0 || segment.StartTimeMs < 0 {
			return fmt.Errorf("%w: segment %d bounds", ErrCheckpointInvalid, index)
		}
		if _, err := decodeDigest(segment.Digest); err != nil {
			return err
		}
		if _, exists := seen[segment.Sequence]; exists {
			return fmt.Errorf("%w: duplicate sequence", ErrCheckpointInvalid)
		}
		seen[segment.Sequence] = struct{}{}
		if segment.StartTimeMs != cumulative {
			return fmt.Errorf("%w: segment timeline", ErrCheckpointInvalid)
		}
		next, ok := addInt64(cumulative, segment.DurationMs)
		if !ok {
			return fmt.Errorf("%w: duration overflow", ErrCheckpointInvalid)
		}
		cumulative = next
		sum, ok := addInt64(accounted, segment.Length)
		if !ok {
			return fmt.Errorf("%w: byte overflow", ErrCheckpointInvalid)
		}
		accounted = sum
	}
	if accounted != state.TotalWritten {
		return fmt.Errorf("%w: byte accounting", ErrCheckpointInvalid)
	}
	if cumulative != state.CumulativeMs {
		return fmt.Errorf("%w: duration accounting", ErrCheckpointInvalid)
	}
	if state.HasSequence {
		if len(state.Segments) == 0 {
			return fmt.Errorf("%w: sequence without segments", ErrCheckpointInvalid)
		}
		if state.NextSequence != uint64(len(state.Segments)) {
			return fmt.Errorf("%w: next sequence", ErrCheckpointInvalid)
		}
	} else if state.NextSequence != 0 || len(state.Segments) > 0 {
		return fmt.Errorf("%w: sequence flags", ErrCheckpointInvalid)
	}
	if len(state.Segments) > 0 && !state.FormatVerified {
		return fmt.Errorf("%w: segments without format verification", ErrCheckpointInvalid)
	}
	if state.EndOfTrack && (!state.InitWritten || len(state.Segments) == 0 || !state.FormatVerified) {
		return fmt.Errorf("%w: end of track state", ErrCheckpointInvalid)
	}
	return nil
}

func loadCheckpoint(statePath string, identity checkpointIdentity) (sabrCheckpoint, bool, error) {
	if err := regularOrAbsent(statePath); err != nil {
		return sabrCheckpoint{}, false, err
	}
	info, err := os.Lstat(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return sabrCheckpoint{}, false, nil
	}
	if err != nil {
		return sabrCheckpoint{}, false, err
	}
	if !info.Mode().IsRegular() {
		return sabrCheckpoint{}, false, ErrUnsafeDestination
	}
	if info.Size() <= 0 {
		return sabrCheckpoint{}, false, nil
	}
	if info.Size() > MaxCheckpointBytes {
		return sabrCheckpoint{}, false, fmt.Errorf("%w: checkpoint exceeds size bound", ErrCheckpointInvalid)
	}
	encoded, err := os.ReadFile(statePath)
	if err != nil {
		return sabrCheckpoint{}, false, err
	}
	if int64(len(encoded)) > MaxCheckpointBytes {
		return sabrCheckpoint{}, false, fmt.Errorf("%w: checkpoint exceeds size bound", ErrCheckpointInvalid)
	}
	if containsForbiddenCheckpointBytes(encoded) {
		return sabrCheckpoint{}, false, fmt.Errorf("%w: forbidden checkpoint content", ErrCheckpointInvalid)
	}
	var state sabrCheckpoint
	if err := decodeStrictJSON(encoded, &state); err != nil {
		return sabrCheckpoint{}, false, nil
	}
	if err := validateCheckpoint(state); err != nil {
		return sabrCheckpoint{}, false, nil
	}
	if !identity.matches(state) {
		return sabrCheckpoint{}, false, nil
	}
	return state, true, nil
}

func decodeStrictJSON(data []byte, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrCheckpointInvalid)
		}
		return fmt.Errorf("%w: trailing JSON garbage", ErrCheckpointInvalid)
	}
	return nil
}

func containsForbiddenCheckpointBytes(encoded []byte) bool {
	lower := bytes.ToLower(encoded)
	for _, needle := range [][]byte{
		[]byte(`"po_token"`),
		[]byte(`"playback_cookie"`),
		[]byte(`"cookie"`),
		[]byte(`"authorization"`),
		[]byte(`"proxy-authorization"`),
		[]byte(`"visitor_data"`),
		[]byte(`"visitor"`),
		[]byte(`"server_url"`),
		[]byte(`"signed_url"`),
		[]byte(`"sabr_context"`),
		[]byte(`"contexts"`),
		[]byte(`"headers"`),
		[]byte("googlevideo.com"),
		[]byte("pot="),
		[]byte("sig="),
	} {
		if bytes.Contains(lower, needle) {
			return true
		}
	}
	// Reject URL-looking signed query material even under unexpected keys.
	if bytes.Contains(lower, []byte("https://")) || bytes.Contains(lower, []byte("http://")) {
		return true
	}
	return false
}

func saveCheckpoint(statePath string, state sabrCheckpoint) error {
	if err := validateCheckpoint(state); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(encoded) > MaxCheckpointBytes {
		return fmt.Errorf("%w: checkpoint exceeds size bound", ErrCheckpointInvalid)
	}
	if containsForbiddenCheckpointBytes(encoded) {
		return fmt.Errorf("%w: forbidden checkpoint content", ErrCheckpointInvalid)
	}
	if err := regularOrAbsent(statePath); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(statePath), "."+filepath.Base(statePath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("%w: create checkpoint: %v", ErrDownloadFailed, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(encoded); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%w: write checkpoint: %v", ErrDownloadFailed, err)
	}
	if err := regularOrAbsent(statePath); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, statePath); err != nil {
		if replaceErr := regularOrAbsent(statePath); replaceErr != nil {
			return replaceErr
		}
		if removeErr := os.Remove(statePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("%w: replace checkpoint: %v", ErrDownloadFailed, removeErr)
		}
		if retryErr := os.Rename(temporaryPath, statePath); retryErr != nil {
			return fmt.Errorf("%w: finalize checkpoint: %v", ErrDownloadFailed, retryErr)
		}
	}
	return nil
}

func removeCheckpoint(statePath string) {
	_ = os.Remove(statePath)
}

func clearResumeArtifacts(partPath, statePath string) {
	_ = os.Remove(partPath)
	_ = os.Remove(statePath)
}

func openResumePart(partPath string, offset int64) (*outputFile, error) {
	if err := regularOrAbsent(partPath); err != nil {
		return nil, err
	}
	flags := os.O_CREATE | os.O_RDWR
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partPath, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open partial output: %w", err)
	}
	if offset > 0 {
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, statErr
		}
		if info.Size() < offset {
			_ = file.Close()
			return nil, fmt.Errorf("%w: partial shorter than checkpoint", ErrCheckpointInvalid)
		}
		if info.Size() > offset {
			if err := file.Truncate(offset); err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("truncate uncommitted tail: %w", err)
			}
		}
		if _, err := file.Seek(offset, 0); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("seek partial output: %w", err)
		}
	}
	return &outputFile{path: partPath, file: file, persistOnFailure: true}, nil
}

func reopenPartFresh(output *outputFile, partPath string) error {
	if output.file != nil {
		_ = output.file.Close()
		output.file = nil
	}
	_ = os.Remove(partPath)
	fresh, err := openResumePart(partPath, 0)
	if err != nil {
		return err
	}
	output.path = fresh.path
	output.file = fresh.file
	output.persistOnFailure = true
	output.published = false
	return nil
}

func verifyCheckpointPartBytes(partPath string, state sabrCheckpoint) error {
	if err := validateCheckpoint(state); err != nil {
		return err
	}
	if err := regularOrAbsent(partPath); err != nil {
		return err
	}
	info, err := os.Lstat(partPath)
	if err != nil {
		return fmt.Errorf("%w: missing partial for checkpoint", ErrCheckpointInvalid)
	}
	if !info.Mode().IsRegular() {
		return ErrUnsafeDestination
	}
	if info.Size() < state.TotalWritten {
		return fmt.Errorf("%w: partial shorter than checkpoint", ErrCheckpointInvalid)
	}
	file, err := os.Open(partPath)
	if err != nil {
		return err
	}
	defer file.Close()
	if state.InitWritten {
		if err := verifyPartChunk(file, state.InitLength, state.InitDigest); err != nil {
			return err
		}
	}
	for index, segment := range state.Segments {
		if err := verifyPartChunk(file, segment.Length, segment.Digest); err != nil {
			return fmt.Errorf("%w: segment %d: %v", ErrCheckpointInvalid, index, err)
		}
	}
	return nil
}

func verifyPartChunk(file *os.File, length int64, wantDigest string) error {
	if length <= 0 {
		return fmt.Errorf("%w: empty chunk", ErrCheckpointInvalid)
	}
	want, err := decodeDigest(wantDigest)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, length))
	if err != nil {
		return err
	}
	if written != length {
		return fmt.Errorf("%w: truncated chunk", ErrCheckpointInvalid)
	}
	var got segmentDigest
	copy(got[:], hasher.Sum(nil))
	if got != want {
		return fmt.Errorf("%w: digest mismatch", ErrCheckpointInvalid)
	}
	return nil
}

// ResumeIdentity binds stable SABR track identity for resume and completion markers.
type ResumeIdentity struct {
	VideoID         string
	ClientName      int32
	ClientVersion   string
	TrackKind       TrackKind
	Format          FormatID
	DurationSec     int64
	UstreamerSHA256 string
	AudioTrackID    string
	DrcEnabled      bool
}

// IdentityFromConfig builds a resume identity from download configuration.
func IdentityFromConfig(config Config) ResumeIdentity {
	identity := identityFromConfig(config)
	return ResumeIdentity{
		VideoID:         identity.VideoID,
		ClientName:      identity.ClientName,
		ClientVersion:   identity.ClientVersion,
		TrackKind:       identity.TrackKind,
		Format:          identity.Format,
		DurationSec:     identity.DurationSec,
		UstreamerSHA256: identity.UstreamerSHA256,
		AudioTrackID:    identity.AudioTrackID,
		DrcEnabled:      identity.DrcEnabled,
	}
}

type sabrCompletionMarker struct {
	Version         int    `json:"v"`
	VideoID         string `json:"video_id,omitempty"`
	ClientName      int32  `json:"client_name"`
	ClientVersion   string `json:"client_version"`
	TrackKind       string `json:"track_kind"`
	Itag            int32  `json:"itag"`
	LastModified    uint64 `json:"last_modified,omitempty"`
	XTags           string `json:"xtags,omitempty"`
	DurationSec     int64  `json:"duration_sec"`
	UstreamerSHA256 string `json:"ustreamer_sha256"`
	AudioTrackID    string `json:"audio_track_id,omitempty"`
	DrcEnabled      bool   `json:"drc_enabled,omitempty"`
	TotalBytes      int64  `json:"total_bytes"`
	ContentSHA256   string `json:"content_sha256"`
}

func completionMarkerPath(destination string) string {
	return destination + ".sabr.json"
}

// CompletionMarkerPath returns the identity-bound completion marker path.
func CompletionMarkerPath(destination string) string {
	return completionMarkerPath(destination)
}

func (identity ResumeIdentity) matchesCompletion(marker sabrCompletionMarker) bool {
	return marker.VideoID == identity.VideoID &&
		marker.ClientName == identity.ClientName &&
		marker.ClientVersion == identity.ClientVersion &&
		marker.TrackKind == string(identity.TrackKind) &&
		marker.Itag == identity.Format.Itag &&
		marker.LastModified == identity.Format.LastModified &&
		marker.XTags == identity.Format.XTags &&
		marker.DurationSec == identity.DurationSec &&
		marker.UstreamerSHA256 == identity.UstreamerSHA256 &&
		marker.AudioTrackID == identity.AudioTrackID &&
		marker.DrcEnabled == identity.DrcEnabled
}

// WriteCompletionMarker persists identity-bound completion metadata for a published track.
func WriteCompletionMarker(destination string, identity ResumeIdentity, totalBytes int64) error {
	return writeCompletionMarker(destination, destination, identity, totalBytes)
}

// completionMarkerWriter is the durable marker writer. Tests may replace it to
// inject deterministic marker-write / post-marker crash boundaries.
var completionMarkerWriter = writeCompletionMarkerDurable

func writeCompletionMarker(mediaPath, destination string, identity ResumeIdentity, totalBytes int64) error {
	return completionMarkerWriter(mediaPath, destination, identity, totalBytes)
}

func writeCompletionMarkerDurable(mediaPath, destination string, identity ResumeIdentity, totalBytes int64) error {
	if totalBytes <= 0 {
		return fmt.Errorf("%w: empty completion", ErrCheckpointInvalid)
	}
	contentHash, err := hashFileSHA256(mediaPath)
	if err != nil {
		return err
	}
	marker := sabrCompletionMarker{
		Version:         checkpointVersion,
		VideoID:         identity.VideoID,
		ClientName:      identity.ClientName,
		ClientVersion:   identity.ClientVersion,
		TrackKind:       string(identity.TrackKind),
		Itag:            identity.Format.Itag,
		LastModified:    identity.Format.LastModified,
		XTags:           identity.Format.XTags,
		DurationSec:     identity.DurationSec,
		UstreamerSHA256: identity.UstreamerSHA256,
		AudioTrackID:    identity.AudioTrackID,
		DrcEnabled:      identity.DrcEnabled,
		TotalBytes:      totalBytes,
		ContentSHA256:   contentHash,
	}
	if err := validateCompletionMarker(marker); err != nil {
		return err
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if containsForbiddenCheckpointBytes(encoded) {
		return fmt.Errorf("%w: forbidden completion content", ErrCheckpointInvalid)
	}
	path := completionMarkerPath(destination)
	if err := regularOrAbsent(path); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("%w: create completion marker: %v", ErrDownloadFailed, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(encoded); err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("%w: write completion marker: %v", ErrDownloadFailed, err)
	}
	if err := regularOrAbsent(path); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if replaceErr := regularOrAbsent(path); replaceErr != nil {
			return replaceErr
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("%w: replace completion marker: %v", ErrDownloadFailed, removeErr)
		}
		if retryErr := os.Rename(temporaryPath, path); retryErr != nil {
			return fmt.Errorf("%w: finalize completion marker: %v", ErrDownloadFailed, retryErr)
		}
	}
	return nil
}

func validateCompletionMarker(marker sabrCompletionMarker) error {
	if marker.Version != checkpointVersion {
		return fmt.Errorf("%w: unsupported completion version", ErrCheckpointInvalid)
	}
	if marker.VideoID == "" || marker.ClientName == 0 || marker.ClientVersion == "" || marker.Itag == 0 || marker.DurationSec <= 0 {
		return fmt.Errorf("%w: incomplete completion identity", ErrCheckpointInvalid)
	}
	if marker.TrackKind != string(TrackAudio) && marker.TrackKind != string(TrackVideo) {
		return fmt.Errorf("%w: completion track kind", ErrCheckpointInvalid)
	}
	if marker.UstreamerSHA256 == "" || len(marker.UstreamerSHA256) != maxCheckpointDigestHexBytes {
		return fmt.Errorf("%w: completion ustreamer hash", ErrCheckpointInvalid)
	}
	if _, err := hex.DecodeString(marker.UstreamerSHA256); err != nil {
		return fmt.Errorf("%w: completion ustreamer hash encoding", ErrCheckpointInvalid)
	}
	if marker.TotalBytes <= 0 || marker.TotalBytes > MaxMediaBytes {
		return fmt.Errorf("%w: completion size", ErrCheckpointInvalid)
	}
	if _, err := decodeDigest(marker.ContentSHA256); err != nil {
		return fmt.Errorf("%w: completion content digest", ErrCheckpointInvalid)
	}
	if len(marker.VideoID) > maxCheckpointVideoIDBytes ||
		len(marker.ClientVersion) > maxCheckpointClientVerBytes ||
		len(marker.XTags) > maxCheckpointXTagsBytes ||
		len(marker.AudioTrackID) > maxCheckpointAudioTrackBytes ||
		strings.ContainsAny(marker.VideoID, "\x00\r\n") {
		return fmt.Errorf("%w: completion identity field too large", ErrCheckpointInvalid)
	}
	return nil
}

// ValidateCompletedTrack verifies an identity-bound completed SABR sidecar.
func ValidateCompletedTrack(destination string, identity ResumeIdentity) (int64, bool, error) {
	if err := regularOrAbsent(destination); err != nil {
		return 0, false, err
	}
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !info.Mode().IsRegular() {
		return 0, false, ErrUnsafeDestination
	}
	if info.Size() <= 0 {
		return 0, false, nil
	}
	markerPath := completionMarkerPath(destination)
	if err := regularOrAbsent(markerPath); err != nil {
		return 0, false, err
	}
	encoded, err := os.ReadFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if int64(len(encoded)) > MaxCheckpointBytes || containsForbiddenCheckpointBytes(encoded) {
		return 0, false, fmt.Errorf("%w: invalid completion marker", ErrCheckpointInvalid)
	}
	var marker sabrCompletionMarker
	if err := decodeStrictJSON(encoded, &marker); err != nil {
		return 0, false, nil
	}
	if err := validateCompletionMarker(marker); err != nil {
		return 0, false, nil
	}
	if !identity.matchesCompletion(marker) {
		return 0, false, nil
	}
	if marker.TotalBytes != info.Size() {
		return 0, false, nil
	}
	got, err := hashFileSHA256(destination)
	if err != nil {
		return 0, false, err
	}
	if got != marker.ContentSHA256 {
		return 0, false, fmt.Errorf("%w: completed track content mismatch", ErrCheckpointInvalid)
	}
	// Durable marker wins: leftover resume artifacts after publish are stale.
	partPath, statePath := checkpointPaths(destination)
	clearResumeArtifacts(partPath, statePath)
	return marker.TotalBytes, true, nil
}

func hashFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.LimitReader(file, MaxMediaBytes+1)); err != nil {
		return "", err
	}
	sum := hasher.Sum(nil)
	if len(sum) != sha256.Size {
		return "", fmt.Errorf("%w: hash size", ErrCheckpointInvalid)
	}
	return hex.EncodeToString(sum), nil
}

func removeCompletionMarker(destination string) {
	_ = os.Remove(completionMarkerPath(destination))
}
