package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// These limits keep --list-subs useful for untrusted InfoJSON without making it
// an unbounded formatting sink. They deliberately exceed normal extractor data.
const (
	maxSubtitleListJSON        = 4 << 20
	maxSubtitleListLanguages   = 200
	maxSubtitleListTracks      = 100
	maxSubtitleListStringRunes = 512
)

var errInvalidSubtitleListing = errors.New("invalid subtitle metadata")

type subtitleListing struct {
	ID        string
	Automatic *subtitleSection // nil means the field was absent
	Manual    subtitleSection
}

type subtitleSection struct{ Languages []subtitleLanguage }
type subtitleLanguage struct {
	Language string
	Tracks   []subtitleTrack
}
type subtitleTrack struct{ Ext, Name string }

// decodeSubtitleListing accepts only the small portion of normalized InfoJSON
// needed for listing. A streaming decoder retains extractor-provided object-key
// order, unlike decoding subtitle maps into Go maps.
func decodeSubtitleListing(raw json.RawMessage) (subtitleListing, error) {
	if len(raw) == 0 || len(raw) > maxSubtitleListJSON {
		return subtitleListing{}, errInvalidSubtitleListing
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return subtitleListing{}, errInvalidSubtitleListing
	}
	var listing subtitleListing
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return subtitleListing{}, errInvalidSubtitleListing
		}
		switch key {
		case "id":
			var id string
			if err := decoder.Decode(&id); err != nil {
				return subtitleListing{}, errInvalidSubtitleListing
			}
			listing.ID = boundedText(id)
		case "automatic_captions":
			section, err := decodeSubtitleSection(decoder)
			if err != nil {
				return subtitleListing{}, errInvalidSubtitleListing
			}
			listing.Automatic = &section
		case "subtitles":
			section, err := decodeSubtitleSection(decoder)
			if err != nil {
				return subtitleListing{}, errInvalidSubtitleListing
			}
			listing.Manual = section
		default:
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return subtitleListing{}, errInvalidSubtitleListing
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return subtitleListing{}, errInvalidSubtitleListing
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return subtitleListing{}, errInvalidSubtitleListing
	}
	if listing.ID == "" {
		listing.ID = "unknown"
	}
	return listing, nil
}

func decodeSubtitleSection(decoder *json.Decoder) (subtitleSection, error) {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return subtitleSection{}, err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return subtitleSection{}, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return subtitleSection{}, errInvalidSubtitleListing
	}
	section := subtitleSection{Languages: make([]subtitleLanguage, 0)}
	for d.More() {
		if len(section.Languages) >= maxSubtitleListLanguages {
			return subtitleSection{}, errInvalidSubtitleListing
		}
		token, err := d.Token()
		if err != nil {
			return subtitleSection{}, err
		}
		language, ok := token.(string)
		if !ok {
			return subtitleSection{}, errInvalidSubtitleListing
		}
		var tracksRaw json.RawMessage
		if err := d.Decode(&tracksRaw); err != nil {
			return subtitleSection{}, err
		}
		tracks, err := decodeSubtitleTracks(tracksRaw)
		if err != nil {
			return subtitleSection{}, err
		}
		section.Languages = append(section.Languages, subtitleLanguage{Language: boundedText(language), Tracks: tracks})
	}
	_, err := d.Token()
	return section, err
}

func decodeSubtitleTracks(raw json.RawMessage) ([]subtitleTrack, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return nil, errInvalidSubtitleListing
	}
	tracks := make([]subtitleTrack, 0)
	for d.More() {
		if len(tracks) >= maxSubtitleListTracks {
			return nil, errInvalidSubtitleListing
		}
		var item map[string]json.RawMessage
		if err := d.Decode(&item); err != nil {
			return nil, err
		}
		ext, err := subtitleTrackText(item, "ext")
		if err != nil {
			return nil, err
		}
		name, err := subtitleTrackText(item, "name")
		if err != nil {
			return nil, err
		}
		if ext == "" {
			ext = "unknown"
		}
		if name == "" {
			name = "unknown"
		}
		tracks = append(tracks, subtitleTrack{Ext: boundedText(ext), Name: boundedText(name)})
	}
	_, err := d.Token()
	return tracks, err
}

func subtitleTrackText(item map[string]json.RawMessage, key string) (string, error) {
	raw, found := item[key]
	if !found {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errInvalidSubtitleListing
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", errInvalidSubtitleListing
	}
	return text, nil
}

func boundedText(input string) string {
	input = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, input)
	if utf8.RuneCountInString(input) <= maxSubtitleListStringRunes {
		return input
	}
	runes := []rune(input)
	return string(runes[:maxSubtitleListStringRunes-1]) + "…"
}

// renderSubtitleListing returns table output for stdout and status output for
// stderr, matching yt-dlp's split between tables and informational messages.
func renderSubtitleListing(ctx context.Context, raw json.RawMessage) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	listing, err := decodeSubtitleListing(raw)
	if err != nil {
		return "", "", fmt.Errorf("subtitle listing: %w", errInvalidSubtitleListing)
	}
	var stdout, stderr strings.Builder
	if listing.Automatic != nil {
		if err := renderSubtitleSection(ctx, &stdout, &stderr, listing.ID, "automatic captions", *listing.Automatic); err != nil {
			return "", "", err
		}
	}
	if err := renderSubtitleSection(ctx, &stdout, &stderr, listing.ID, "subtitles", listing.Manual); err != nil {
		return "", "", err
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return stdout.String(), stderr.String(), nil
}

func renderSubtitleSection(ctx context.Context, stdout, stderr *strings.Builder, id, name string, section subtitleSection) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(section.Languages) == 0 {
		fmt.Fprintf(stderr, "%s has no %s\n", id, name)
		return nil
	}
	rows := make([][3]string, 0, len(section.Languages))
	for _, language := range section.Languages {
		if err := ctx.Err(); err != nil {
			return err
		}
		// An empty format list is not a usable subtitle language. Treat it like
		// absent extractor data rather than indexing an empty names slice.
		if len(language.Tracks) == 0 {
			continue
		}
		exts, names := make([]string, 0, len(language.Tracks)), make([]string, 0, len(language.Tracks))
		for index := len(language.Tracks) - 1; index >= 0; index-- {
			if err := ctx.Err(); err != nil {
				return err
			}
			exts = append(exts, language.Tracks[index].Ext)
			names = append(names, language.Tracks[index].Name)
		}
		allNamesSame := len(names) > 0
		for _, value := range names[1:] {
			allNamesSame = allNamesSame && value == names[0]
		}
		nameCell := strings.Join(names, ", ")
		if allNamesSame {
			if names[0] == "unknown" {
				nameCell = ""
			} else {
				nameCell = names[0]
			}
		}
		rows = append(rows, [3]string{language.Language, nameCell, strings.Join(exts, ", ")})
	}
	if len(rows) == 0 {
		fmt.Fprintf(stderr, "%s has no %s\n", id, name)
		return nil
	}
	fmt.Fprintf(stderr, "[info] Available %s for %s:\n", name, id)
	widths := [3]int{len("Language"), len("Name"), len("Formats")}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		for i, cell := range row {
			if width := utf8.RuneCountInString(cell); width > widths[i] {
				widths[i] = width
			}
		}
	}
	fmt.Fprintf(stdout, "%-*s  %-*s  %-*s\n", widths[0], "Language", widths[1], "Name", widths[2], "Formats")
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%-*s  %-*s  %-*s\n", widths[0], row[0], widths[1], row[1], widths[2], row[2])
	}
	return nil
}
