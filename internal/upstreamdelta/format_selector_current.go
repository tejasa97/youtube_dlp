package upstreamdelta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"time"
)

const FormatSelectorCurrentSchemaVersion = 1

const formatSelectorBaseline = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type FormatSelectorCurrent struct {
	SchemaVersion    int                    `json:"schema_version"`
	Kind             string                 `json:"kind"`
	SourceRepository string                 `json:"source_repository"`
	From             string                 `json:"from"`
	To               string                 `json:"to"`
	ObservedAt       string                 `json:"observed_at"`
	Derivation       string                 `json:"derivation"`
	MergeBase        string                 `json:"merge_base"`
	AheadCount       int                    `json:"ahead_count"`
	Conclusion       string                 `json:"conclusion"`
	NormativeSources []FormatSelectorSource `json:"normative_sources"`
	Commits          []FormatSelectorCommit `json:"commits"`
}

type FormatSelectorSource struct {
	Path       string `json:"path"`
	BeforeBlob string `json:"before_blob"`
	AfterBlob  string `json:"after_blob"`
}
type FormatSelectorCommit struct {
	Hash        string               `json:"hash"`
	CommittedAt string               `json:"committed_at"`
	Subject     string               `json:"subject"`
	Paths       []FormatSelectorPath `json:"paths"`
}
type FormatSelectorPath struct {
	Path           string `json:"path"`
	Classification string `json:"classification"`
	Disposition    string `json:"disposition"`
}

func LoadFormatSelectorCurrentFile(path string) (*FormatSelectorCurrent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record FormatSelectorCurrent
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("artifact must contain one JSON object")
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *FormatSelectorCurrent) Validate() error {
	if r.SchemaVersion != FormatSelectorCurrentSchemaVersion || r.Kind != "format_selector_current_upstream_delta" {
		return errors.New("unsupported format-selector delta schema")
	}
	if r.SourceRepository != "https://github.com/yt-dlp/yt-dlp.git" || r.From != formatSelectorBaseline || r.MergeBase != r.From || !shaPattern.MatchString(r.To) {
		return errors.New("invalid frozen source identity")
	}
	if r.AheadCount != 7 || len(r.Commits) != r.AheadCount {
		return errors.New("incomplete intervening commits")
	}
	if _, err := time.Parse(time.RFC3339, r.ObservedAt); err != nil || r.Derivation == "" {
		return errors.New("invalid observation provenance")
	}
	if r.Conclusion != "no_format_selector_delta" {
		return fmt.Errorf("unsupported conclusion %q", r.Conclusion)
	}
	if err := validateSources(r.NormativeSources); err != nil {
		return err
	}
	seenCommits := map[string]bool{}
	for _, c := range r.Commits {
		if !shaPattern.MatchString(c.Hash) || seenCommits[c.Hash] || c.Subject == "" {
			return errors.New("invalid or duplicate intervening commit")
		}
		seenCommits[c.Hash] = true
		if _, err := time.Parse(time.RFC3339, c.CommittedAt); err != nil || len(c.Paths) == 0 {
			return errors.New("invalid intervening commit metadata")
		}
		seenPaths := map[string]bool{}
		for _, p := range c.Paths {
			if p.Path == "" || seenPaths[p.Path] || !validClassification(p.Classification) || !validDisposition(p.Disposition) || !validPair(p.Classification, p.Disposition) {
				return errors.New("invalid or duplicate classified path")
			}
			seenPaths[p.Path] = true
		}
		if !samePaths(seenPaths, frozenRangePaths[c.Hash]) {
			return errors.New("artifact does not completely enumerate frozen commit paths")
		}
	}
	if !seenCommits[r.To] {
		return errors.New("target is not the final intervening commit")
	}
	for _, hash := range []string{"93ceb95cdf0eb05d8a2515e3760fd62239683b82", "69ea200067c274667984c6495578a957ab7ca606", "a8be438aac1b90c3888e974056d967b8be90fa7e", "1f1101d0dc8a0ee316540fc938edbaca43e4977b", "aaf7405ba3a45b32c59f160426efc9b561af035a", "07591f601e55c9d23399c07bf5fe1136f0913888", "fdcc954df4955267ec1627cbeb347b661a110e7c"} {
		if !seenCommits[hash] {
			return errors.New("artifact does not completely enumerate the frozen range")
		}
	}
	return nil
}

var frozenRangePaths = map[string][]string{
	"93ceb95cdf0eb05d8a2515e3760fd62239683b82": {"yt_dlp/extractor/youtube/_video.py"},
	"69ea200067c274667984c6495578a957ab7ca606": {"README.md", "yt_dlp/extractor/youtube/_base.py", "yt_dlp/extractor/youtube/_video.py"},
	"a8be438aac1b90c3888e974056d967b8be90fa7e": {"yt_dlp/extractor/vimeo.py"},
	"1f1101d0dc8a0ee316540fc938edbaca43e4977b": {"yt_dlp/extractor/instagram.py"},
	"aaf7405ba3a45b32c59f160426efc9b561af035a": {"yt_dlp/extractor/appleconnect.py", "yt_dlp/extractor/applepodcasts.py"},
	"07591f601e55c9d23399c07bf5fe1136f0913888": {"README.md", "devscripts/changelog_override.json", "yt_dlp/extractor/vimeo.py"},
	"fdcc954df4955267ec1627cbeb347b661a110e7c": {"README.md", "yt_dlp/extractor/vimeo.py"},
}

func samePaths(got map[string]bool, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, path := range want {
		if !got[path] {
			return false
		}
	}
	return true
}

func validateSources(sources []FormatSelectorSource) error {
	want := []string{"test/test_YoutubeDL.py", "yt_dlp/YoutubeDL.py", "yt_dlp/options.py", "yt_dlp/utils/_utils.py"}
	if len(sources) != len(want) {
		return errors.New("incomplete normative source inventory")
	}
	got := make([]string, 0, len(sources))
	for _, source := range sources {
		if !shaPattern.MatchString(source.BeforeBlob) || !shaPattern.MatchString(source.AfterBlob) || source.BeforeBlob != source.AfterBlob {
			return errors.New("no-delta conclusion requires identical normative blobs")
		}
		got = append(got, source.Path)
	}
	sort.Strings(got)
	for i := range want {
		if got[i] != want[i] {
			return errors.New("unexpected normative source inventory")
		}
	}
	return nil
}
func validClassification(value string) bool {
	switch value {
	case "normative_implemented", "normative_intentional_deviation", "normative_scoped_follow_up", "extractor_input_adjacent_outside_selection_semantics", "unrelated":
		return true
	}
	return false
}
func validDisposition(value string) bool {
	switch value {
	case "implemented", "intentional_deviation", "scoped_follow_up", "outside_selection_semantics", "no_product_change":
		return true
	}
	return false
}
func validPair(classification, disposition string) bool {
	return (classification == "normative_implemented" && disposition == "implemented") || (classification == "normative_intentional_deviation" && disposition == "intentional_deviation") || (classification == "normative_scoped_follow_up" && disposition == "scoped_follow_up") || (classification == "extractor_input_adjacent_outside_selection_semantics" && disposition == "outside_selection_semantics") || (classification == "unrelated" && disposition == "no_product_change")
}
