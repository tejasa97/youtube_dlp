package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	soundCloudPrivateSetBatchIDs = 200
	soundCloudPrivateSetURLRoom  = 128
)

var (
	ErrSoundCloudPrivateSetNetwork     = errors.New("SoundCloud private-set network failure")
	ErrSoundCloudPrivateSetRateLimited = errors.New("SoundCloud private-set rate limited")
)

func soundCloudPrivateSetNeedsHydration(tracks []soundCloudTrack, token string) bool {
	if token == "" {
		return false
	}
	for _, track := range tracks {
		if track.PermalinkURL == "" {
			return true
		}
	}
	return false
}

func (extractor *SoundCloud) hydrateSoundCloudPrivateSet(
	ctx context.Context,
	transport Transport,
	playlistID, token string,
	tracks []soundCloudTrack,
) ([]soundCloudTrack, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	positions := make(map[string][]int, len(tracks))
	ids := make([]string, 0, len(tracks))
	for index, track := range tracks {
		id := track.ID.String()
		if !validSoundCloudPrivateSetID(id) {
			return nil, fmt.Errorf("%w: malformed SoundCloud private-set track", ErrInvalidMetadata)
		}
		if _, exists := positions[id]; !exists {
			ids = append(ids, id)
		}
		positions[id] = append(positions[id], index)
	}
	requests, err := soundCloudPrivateSetBatches(playlistID, token, ids)
	if err != nil {
		return nil, err
	}
	hydrated := append([]soundCloudTrack(nil), tracks...)
	seen := make(map[string]bool, len(ids))
	for _, endpoint := range requests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var response []soundCloudTrack
		if err := extractor.requestJSON(ctx, transport, endpoint, &response); err != nil {
			return nil, categorizeSoundCloudPrivateSetError(ctx, err)
		}
		if response == nil || len(response) > soundCloudPrivateSetBatchIDs {
			return nil, fmt.Errorf("%w: malformed SoundCloud private-set hydration", ErrInvalidMetadata)
		}
		parsed, _ := url.Parse(endpoint)
		batchIDs := strings.Split(parsed.Query().Get("ids"), ",")
		batchExpected := make(map[string]bool, len(batchIDs))
		for _, id := range batchIDs {
			batchExpected[id] = true
		}
		batchSeen := make(map[string]bool, len(response))
		for _, track := range response {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			id := track.ID.String()
			indexes, expected := positions[id]
			if !validSoundCloudPrivateSetID(id) || !expected || !batchExpected[id] ||
				batchSeen[id] || seen[id] {
				return nil, fmt.Errorf("%w: malformed SoundCloud private-set hydration", ErrInvalidMetadata)
			}
			batchSeen[id] = true
			seen[id] = true
			for _, index := range indexes {
				hydrated[index] = track
			}
		}
	}
	return hydrated, nil
}

func soundCloudPrivateSetBatches(playlistID, token string, ids []string) ([]string, error) {
	if !validSoundCloudPrivateSetID(playlistID) || !soundCloudTokenPattern.MatchString(token) ||
		len(token) > 256 || len(ids) == 0 || len(ids) > soundCloudMaxSetEntries {
		return nil, fmt.Errorf("%w: invalid SoundCloud private-set hydration", ErrInvalidMetadata)
	}
	requests := make([]string, 0, (len(ids)+soundCloudPrivateSetBatchIDs-1)/soundCloudPrivateSetBatchIDs)
	batch := make([]string, 0, soundCloudPrivateSetBatchIDs)
	for _, id := range ids {
		if !validSoundCloudPrivateSetID(id) {
			return nil, fmt.Errorf("%w: invalid SoundCloud private-set track ID", ErrInvalidMetadata)
		}
		candidate := append(batch, id)
		endpoint := soundCloudPrivateSetURL(playlistID, token, candidate)
		if len(candidate) > soundCloudPrivateSetBatchIDs ||
			len(endpoint) > soundCloudMaxURLBytes-soundCloudPrivateSetURLRoom {
			if len(batch) == 0 {
				return nil, fmt.Errorf("%w: SoundCloud private-set request exceeds bound", ErrInvalidMetadata)
			}
			requests = append(requests, soundCloudPrivateSetURL(playlistID, token, batch))
			batch = []string{id}
			endpoint = soundCloudPrivateSetURL(playlistID, token, batch)
			if len(endpoint) > soundCloudMaxURLBytes-soundCloudPrivateSetURLRoom {
				return nil, fmt.Errorf("%w: SoundCloud private-set request exceeds bound", ErrInvalidMetadata)
			}
			continue
		}
		batch = candidate
	}
	if len(batch) > 0 {
		requests = append(requests, soundCloudPrivateSetURL(playlistID, token, batch))
	}
	return requests, nil
}

func validSoundCloudPrivateSetID(id string) bool {
	return id != "" && soundCloudNumericID(id) == id && (len(id) == 1 || id[0] != '0')
}

func soundCloudPrivateSetURL(playlistID, token string, ids []string) string {
	query := make(url.Values, 3)
	query.Set("ids", strings.Join(ids, ","))
	query.Set("playlistId", playlistID)
	query.Set("playlistSecretToken", token)
	return soundCloudAPIBase + "tracks?" + query.Encode()
}

func categorizeSoundCloudPrivateSetError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		if status.Code == http.StatusTooManyRequests {
			return fmt.Errorf("%w: HTTP status %d", ErrSoundCloudPrivateSetRateLimited, status.Code)
		}
		return fmt.Errorf("%w: HTTP status %d", ErrSoundCloudPrivateSetNetwork, status.Code)
	}
	return fmt.Errorf("%w: request failed", ErrSoundCloudPrivateSetNetwork)
}
