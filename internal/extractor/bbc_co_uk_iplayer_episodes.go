package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// BBCCoUkIPlayerEpisodes extracts iPlayer episodes listings via the public
// GraphQL playlist API and hands episode URLs to bbciplayer.
type BBCCoUkIPlayerEpisodes struct{}

func NewBBCCoUkIPlayerEpisodes() BBCCoUkIPlayerEpisodes { return BBCCoUkIPlayerEpisodes{} }

func (BBCCoUkIPlayerEpisodes) Name() string { return "bbc_co_uk_iplayer_episodes" }

func (BBCCoUkIPlayerEpisodes) Suitable(parsed *url.URL) bool {
	_, ok := classifyBBCCoUkIPlayerEpisodesURL(parsed)
	return ok
}

func (BBCCoUkIPlayerEpisodes) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	pid, ok := classifyBBCCoUkIPlayerEpisodesURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	seriesID := parsed.Query().Get("seriesId")
	explicitPage, hasPage, err := bbcExplicitPageNumber(parsed)
	if err != nil {
		return Extraction{}, err
	}
	perPage := bbcEpisodesPageSize
	if hasPage {
		perPage = bbcExplicitPageSize
	}
	metadata, err := requestBBCEpisodesGraphQLPage(ctx, request.Transport, pid, seriesID, 1, 1)
	if err != nil {
		return Extraction{}, err
	}
	title := metadata.Title.Default
	if title == "" {
		title = pid
	}
	sequence, err := newBBCEpisodesPlaylistSource(request.Transport, pid, seriesID, perPage, hasPage, explicitPage)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(bbcPlaylistInfo(pid, title, request.URL, firstBBCSynopsis(metadata.Synopsis)), sequence)
}

func classifyBBCCoUkIPlayerEpisodesURL(parsed *url.URL) (string, bool) {
	if !bbcValidHost(parsed) {
		return "", false
	}
	match := bbcIPlayerEpisodes.FindStringSubmatch(parsed.Path)
	if len(match) != 2 || !bbcPIDPattern.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

type bbcEpisodesGraphQLResponse struct {
	Title struct {
		Default string `json:"default"`
	} `json:"title"`
	Synopsis map[string]string `json:"synopsis"`
	Entities struct {
		Results []struct {
			Episode struct {
				ID       string `json:"id"`
				Subtitle struct {
					Default string `json:"default"`
				} `json:"subtitle"`
			} `json:"episode"`
		} `json:"results"`
	} `json:"entities"`
}

func requestBBCEpisodesGraphQLPage(ctx context.Context, transport Transport, pid, seriesID string, page, perPage int) (bbcEpisodesGraphQLResponse, error) {
	variables := map[string]any{"id": pid, "page": page, "perPage": perPage}
	if seriesID != "" {
		variables["sliceId"] = seriesID
	}
	body, _ := json.Marshal(map[string]any{"id": bbcGraphQLQueryID, "variables": variables})
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	var envelope struct {
		Data *struct {
			Programme *bbcEpisodesGraphQLResponse `json:"programme"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	err := requestBBCIsolatedJSON(ctx, transport, http.MethodPost, bbcPlaylistGraphQL, body, headers, &envelope)
	if err != nil {
		return bbcEpisodesGraphQLResponse{}, categorizeBBCAPIError(err)
	}
	if len(envelope.Errors) != 0 {
		return bbcEpisodesGraphQLResponse{}, fmt.Errorf("%w: BBC episodes GraphQL error", ErrInvalidMetadata)
	}
	if envelope.Data == nil || envelope.Data.Programme == nil {
		return bbcEpisodesGraphQLResponse{}, fmt.Errorf("%w: missing BBC episodes GraphQL programme", ErrInvalidMetadata)
	}
	return *envelope.Data.Programme, nil
}

func entriesFromBBCEpisodesGraphQL(response bbcEpisodesGraphQLResponse) ([]Entry, error) {
	entries := make([]Entry, 0, len(response.Entities.Results))
	for index, result := range response.Entities.Results {
		episode := result.Episode
		if episode.ID == "" {
			return nil, fmt.Errorf("%w: missing BBC episode id at index %d", ErrInvalidMetadata, index)
		}
		if !bbcPIDPattern.MatchString(episode.ID) {
			return nil, fmt.Errorf("%w: malformed BBC episode id at index %d", ErrInvalidMetadata, index)
		}
		entries = append(entries, bbcEpisodeChildEntry(episode.ID, episode.Subtitle.Default))
	}
	return bbcDedupeEntries(entries), nil
}

func newBBCEpisodesPlaylistSource(transport Transport, pid, seriesID string, perPage int, singlePage bool, explicitPage int) (EntrySequence, error) {
	fetch := func(ctx context.Context, pageIndex int) (bbcPlaylistPageResult, error) {
		if singlePage {
			if pageIndex != 0 {
				return bbcPlaylistPageResult{}, nil
			}
			response, err := requestBBCEpisodesGraphQLPage(ctx, transport, pid, seriesID, explicitPage, perPage)
			if err != nil {
				return bbcPlaylistPageResult{}, err
			}
			entries, err := entriesFromBBCEpisodesGraphQL(response)
			if err != nil {
				return bbcPlaylistPageResult{}, err
			}
			return bbcPlaylistPageResult{entries: entries, lastPage: true}, nil
		}
		response, err := requestBBCEpisodesGraphQLPage(ctx, transport, pid, seriesID, pageIndex+1, perPage)
		if err != nil {
			return bbcPlaylistPageResult{}, err
		}
		entries, err := entriesFromBBCEpisodesGraphQL(response)
		if err != nil {
			return bbcPlaylistPageResult{}, err
		}
		return bbcPlaylistPageResult{
			entries:  entries,
			lastPage: len(entries) < perPage,
		}, nil
	}
	return newBBCReusablePlaylistSource(perPage, defaultMaxPlaylistPages, bbcMaxPlaylistEntries, singlePage, "", fetch)
}
