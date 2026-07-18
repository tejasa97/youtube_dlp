// Package upstreamdelta classifies a bounded yt-dlp history window for the
// Phase 1 maintenance replay. It is development tooling and is not linked into
// the product binary.
package upstreamdelta

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion = 1
	WeekCount     = 8
)

type Category string

const (
	CategoryExtractor     Category = "extractor"
	CategoryTransport     Category = "transport"
	CategoryChallenge     Category = "challenge"
	CategoryCLI           Category = "cli"
	CategoryDownloader    Category = "downloader"
	CategoryPostprocessor Category = "postprocessor"
	CategoryOther         Category = "other"
)

var categoryOrder = []Category{
	CategoryExtractor, CategoryTransport, CategoryChallenge, CategoryCLI,
	CategoryDownloader, CategoryPostprocessor, CategoryOther,
}

type Commit struct {
	Hash       string     `json:"hash"`
	Committed  string     `json:"committed"`
	Subject    string     `json:"subject"`
	Week       int        `json:"week"`
	Categories []Category `json:"categories"`
	Files      []string   `json:"files"`
}

type Week struct {
	Number         int              `json:"number"`
	Start          string           `json:"start"`
	End            string           `json:"end"`
	CommitCount    int              `json:"commit_count"`
	CategoryCounts map[Category]int `json:"category_counts"`
}

type Report struct {
	SchemaVersion   int              `json:"schema_version"`
	ReferenceCommit string           `json:"reference_commit"`
	WindowStart     string           `json:"window_start"`
	WindowEnd       string           `json:"window_end"`
	CommitCount     int              `json:"commit_count"`
	CategoryCounts  map[Category]int `json:"category_counts"`
	Weeks           []Week           `json:"weeks"`
	Commits         []Commit         `json:"commits"`
}

type Config struct {
	Repository string
	Commit     string
}

func Replay(ctx context.Context, config Config) (Report, error) {
	if config.Repository == "" || config.Commit == "" {
		return Report{}, errors.New("repository and commit are required")
	}
	commit, err := gitOutput(ctx, config.Repository, "rev-parse", "--verify", config.Commit+"^{commit}")
	if err != nil {
		return Report{}, fmt.Errorf("resolve reference commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	dateText, err := gitOutput(ctx, config.Repository, "show", "-s", "--format=%cI", commit)
	if err != nil {
		return Report{}, fmt.Errorf("read reference date: %w", err)
	}
	end, err := time.Parse(time.RFC3339, strings.TrimSpace(dateText))
	if err != nil {
		return Report{}, fmt.Errorf("parse reference date: %w", err)
	}
	start := end.Add(-WeekCount * 7 * 24 * time.Hour)
	log, err := gitOutput(ctx, config.Repository, "log", "--reverse", "--format=%x1e%H%x1f%cI%x1f%s", "--name-only", "--since="+start.Format(time.RFC3339), "--until="+end.Format(time.RFC3339), commit)
	if err != nil {
		return Report{}, fmt.Errorf("read reference history: %w", err)
	}
	commits, err := parseLog(log, start, end)
	if err != nil {
		return Report{}, err
	}
	return buildReport(commit, start, end, commits), nil
}

func gitOutput(ctx context.Context, repository string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, arguments...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = "git command failed"
		}
		return "", errors.New(message)
	}
	return stdout.String(), nil
}

func parseLog(input string, start, end time.Time) ([]Commit, error) {
	var commits []Commit
	for _, raw := range strings.Split(input, "\x1e") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(raw))
		if !scanner.Scan() {
			continue
		}
		header := strings.SplitN(scanner.Text(), "\x1f", 3)
		if len(header) != 3 {
			return nil, errors.New("malformed git log header")
		}
		committed, err := time.Parse(time.RFC3339, header[1])
		if err != nil {
			return nil, errors.New("malformed git commit date")
		}
		if committed.Before(start) || committed.After(end) {
			continue
		}
		files := make([]string, 0)
		for scanner.Scan() {
			if file := strings.TrimSpace(scanner.Text()); file != "" {
				files = append(files, file)
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		sort.Strings(files)
		week := int(committed.Sub(start) / (7 * 24 * time.Hour))
		if week >= WeekCount {
			week = WeekCount - 1
		}
		commits = append(commits, Commit{
			Hash: header[0], Committed: committed.Format(time.RFC3339), Subject: header[2],
			Week: week + 1, Categories: Classify(header[2], files), Files: files,
		})
	}
	return commits, nil
}

func Classify(subject string, files []string) []Category {
	found := make(map[Category]bool)
	lowerSubject := strings.ToLower(subject)
	if strings.HasPrefix(lowerSubject, "[ie/") || strings.HasPrefix(lowerSubject, "[extractor") {
		found[CategoryExtractor] = true
	}
	for _, file := range files {
		path := strings.ToLower(file)
		switch {
		case strings.HasPrefix(path, "yt_dlp/extractor/"):
			found[CategoryExtractor] = true
		case strings.HasPrefix(path, "yt_dlp/networking/") || strings.Contains(path, "request_handler"):
			found[CategoryTransport] = true
		case strings.HasPrefix(path, "yt_dlp/downloader/"):
			found[CategoryDownloader] = true
		case strings.HasPrefix(path, "yt_dlp/postprocessor/"):
			found[CategoryPostprocessor] = true
		case path == "yt_dlp/options.py" || path == "yt_dlp/__init__.py" || path == "yt_dlp/youtubedl.py":
			found[CategoryCLI] = true
		}
		challengePath := strings.Contains(path, "extractor/youtube/jsc") || strings.Contains(path, "extractor/youtube/pot") ||
			strings.Contains(path, "jsinterp") || strings.Contains(path, "ejs") || strings.Contains(path, "_jsruntime")
		if challengePath || strings.Contains(path, "extractor/youtube") {
			if containsAny(lowerSubject, "player", "challenge", "signature", "nsig", "ejs", "po token", "pot", "javascript", "js ") {
				found[CategoryChallenge] = true
			}
			if challengePath && strings.Contains(lowerSubject, "youtube") {
				found[CategoryChallenge] = true
			}
		}
	}
	if containsAny(lowerSubject, "[network", "[rh/", "impersonat", "cookie leak", "proxy") {
		found[CategoryTransport] = true
	}
	if containsAny(lowerSubject, "[cli]", "option", "--") {
		found[CategoryCLI] = true
	}
	if containsAny(lowerSubject, "[fd/", "downloader", "fragment") {
		found[CategoryDownloader] = true
	}
	if containsAny(lowerSubject, "[pp/", "postprocessor", "ffmpegmetadata") {
		found[CategoryPostprocessor] = true
	}
	if len(found) == 0 {
		found[CategoryOther] = true
	}
	result := make([]Category, 0, len(found))
	for _, category := range categoryOrder {
		if found[category] {
			result = append(result, category)
		}
	}
	return result
}

func containsAny(input string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(input, needle) {
			return true
		}
	}
	return false
}

func buildReport(commit string, start, end time.Time, commits []Commit) Report {
	report := Report{
		SchemaVersion: SchemaVersion, ReferenceCommit: commit,
		WindowStart: start.Format(time.RFC3339), WindowEnd: end.Format(time.RFC3339),
		CommitCount: len(commits), CategoryCounts: make(map[Category]int), Commits: commits,
	}
	for index := 0; index < WeekCount; index++ {
		weekStart := start.Add(time.Duration(index) * 7 * 24 * time.Hour)
		report.Weeks = append(report.Weeks, Week{
			Number: index + 1, Start: weekStart.Format(time.RFC3339),
			End: weekStart.Add(7 * 24 * time.Hour).Format(time.RFC3339), CategoryCounts: make(map[Category]int),
		})
	}
	for _, commit := range commits {
		week := &report.Weeks[commit.Week-1]
		week.CommitCount++
		for _, category := range commit.Categories {
			report.CategoryCounts[category]++
			week.CategoryCounts[category]++
		}
	}
	return report
}

func Marshal(report Report) ([]byte, error) {
	return json.MarshalIndent(report, "", "  ")
}
