package extractor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// BBCCoUkIPlayerGroup extracts iPlayer group listings via the public IBL REST
// API and hands episode URLs to bbciplayer.
type BBCCoUkIPlayerGroup struct{}

func NewBBCCoUkIPlayerGroup() BBCCoUkIPlayerGroup { return BBCCoUkIPlayerGroup{} }

func (BBCCoUkIPlayerGroup) Name() string { return "bbc_co_uk_iplayer_group" }

func (BBCCoUkIPlayerGroup) Suitable(parsed *url.URL) bool {
	_, ok := classifyBBCCoUkIPlayerGroupURL(parsed)
	return ok
}

func (BBCCoUkIPlayerGroup) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	pid, ok := classifyBBCCoUkIPlayerGroupURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	explicitPage, hasPage, err := bbcExplicitPageNumber(parsed)
	if err != nil {
		return Extraction{}, err
	}
	perPage := bbcGroupPageSize
	if hasPage {
		perPage = bbcExplicitPageSize
	}
	metadata, err := requestBBCGroupPage(ctx, request.Transport, pid, 1, 1)
	if err != nil {
		return Extraction{}, err
	}
	title := metadata.Group.Title
	if title == "" {
		title = pid
	}
	sequence, err := newBBCGroupPlaylistSource(request.Transport, pid, perPage, hasPage, explicitPage)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(bbcPlaylistInfo(pid, title, request.URL, firstBBCSynopsis(metadata.Group.Synopses)), sequence)
}

func classifyBBCCoUkIPlayerGroupURL(parsed *url.URL) (string, bool) {
	if !bbcValidHost(parsed) {
		return "", false
	}
	match := bbcIPlayerGroup.FindStringSubmatch(parsed.Path)
	if len(match) != 2 || !bbcPIDPattern.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

type bbcGroupAPIResponse struct {
	GroupEpisodes struct {
		Elements []bbcGroupEpisode `json:"elements"`
	} `json:"group_episodes"`
	Group struct {
		Title    string            `json:"title"`
		Synopses map[string]string `json:"synopses"`
	} `json:"group"`
}

type bbcGroupEpisode struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle struct {
		Default string `json:"default"`
	} `json:"subtitle"`
}

func requestBBCGroupPage(ctx context.Context, transport Transport, pid string, page, perPage int) (bbcGroupAPIResponse, error) {
	endpoint, err := url.Parse(bbcGroupAPIBase + pid + "/episodes")
	if err != nil {
		return bbcGroupAPIResponse{}, fmt.Errorf("%w: invalid BBC group API URL", ErrInvalidMetadata)
	}
	query := endpoint.Query()
	query.Set("page", strconv.Itoa(page))
	query.Set("per_page", strconv.Itoa(perPage))
	endpoint.RawQuery = query.Encode()
	var response bbcGroupAPIResponse
	err = requestBBCIsolatedJSON(ctx, transport, http.MethodGet, endpoint.String(), nil, make(http.Header), &response)
	if err != nil {
		return bbcGroupAPIResponse{}, categorizeBBCAPIError(err)
	}
	if response.GroupEpisodes.Elements == nil {
		return bbcGroupAPIResponse{}, fmt.Errorf("%w: missing BBC group episodes envelope", ErrInvalidMetadata)
	}
	return response, nil
}

func entriesFromBBCGroupPage(response bbcGroupAPIResponse) ([]Entry, error) {
	entries := make([]Entry, 0, len(response.GroupEpisodes.Elements))
	for index, episode := range response.GroupEpisodes.Elements {
		if episode.ID == "" {
			return nil, fmt.Errorf("%w: missing BBC group episode id at index %d", ErrInvalidMetadata, index)
		}
		if !bbcPIDPattern.MatchString(episode.ID) {
			return nil, fmt.Errorf("%w: malformed BBC group episode id at index %d", ErrInvalidMetadata, index)
		}
		title := episode.Subtitle.Default
		if title == "" {
			title = episode.Title
		}
		entries = append(entries, bbcEpisodeChildEntry(episode.ID, title))
	}
	return bbcDedupeEntries(entries), nil
}

func newBBCGroupPlaylistSource(transport Transport, pid string, perPage int, singlePage bool, explicitPage int) (EntrySequence, error) {
	fetch := func(ctx context.Context, pageIndex int) (bbcPlaylistPageResult, error) {
		if singlePage {
			if pageIndex != 0 {
				return bbcPlaylistPageResult{}, nil
			}
			response, err := requestBBCGroupPage(ctx, transport, pid, explicitPage, perPage)
			if err != nil {
				return bbcPlaylistPageResult{}, err
			}
			entries, err := entriesFromBBCGroupPage(response)
			if err != nil {
				return bbcPlaylistPageResult{}, err
			}
			return bbcPlaylistPageResult{entries: entries, lastPage: true}, nil
		}
		response, err := requestBBCGroupPage(ctx, transport, pid, pageIndex+1, perPage)
		if err != nil {
			return bbcPlaylistPageResult{}, err
		}
		entries, err := entriesFromBBCGroupPage(response)
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
