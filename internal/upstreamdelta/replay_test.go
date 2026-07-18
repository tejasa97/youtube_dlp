package upstreamdelta

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestPinnedEightWeekInventory(t *testing.T) {
	data, err := os.ReadFile("../../conformance/upstream-delta/inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != SchemaVersion || report.ReferenceCommit != "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8" || report.CommitCount != 114 || len(report.Weeks) != WeekCount || len(report.Commits) != report.CommitCount {
		t.Fatalf("inventory header = %#v", report)
	}
	seen := make(map[string]bool, len(report.Commits))
	for _, commit := range report.Commits {
		if commit.Hash == "" || seen[commit.Hash] || commit.Week < 1 || commit.Week > WeekCount || len(commit.Categories) == 0 {
			t.Fatalf("invalid inventory commit = %#v", commit)
		}
		seen[commit.Hash] = true
	}
	for _, category := range []Category{CategoryExtractor, CategoryTransport, CategoryChallenge, CategoryCLI, CategoryDownloader, CategoryPostprocessor} {
		if report.CategoryCounts[category] == 0 {
			t.Fatalf("inventory has no %s changes", category)
		}
	}
}

func TestClassifyRecognizesRequiredChangeClasses(t *testing.T) {
	tests := []struct {
		subject string
		files   []string
		want    []Category
	}{
		{"[ie/youtube] Update player client", []string{"yt_dlp/extractor/youtube/_video.py"}, []Category{CategoryExtractor, CategoryChallenge}},
		{"[ie/youtube] Drop old runtime", []string{"yt_dlp/utils/_jsruntime.py"}, []Category{CategoryExtractor, CategoryChallenge}},
		{"[rh:curl_cffi] Fix proxy", []string{"yt_dlp/networking/_curlcffi.py"}, []Category{CategoryTransport}},
		{"Add --fixture option", []string{"yt_dlp/options.py"}, []Category{CategoryCLI}},
		{"[fd/hls] Fix fragments", []string{"yt_dlp/downloader/hls.py"}, []Category{CategoryDownloader}},
		{"[pp/FFmpegMetadata] Fix", []string{"yt_dlp/postprocessor/ffmpeg.py"}, []Category{CategoryPostprocessor}},
		{"Update docs", []string{"README.md"}, []Category{CategoryOther}},
	}
	for _, test := range tests {
		if got := Classify(test.subject, test.files); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("Classify(%q) = %v, want %v", test.subject, got, test.want)
		}
	}
}

func TestParseLogAssignsBoundaryCommitToLastWeek(t *testing.T) {
	start := time.Date(2026, 5, 19, 14, 14, 20, 0, time.UTC)
	end := start.Add(8 * 7 * 24 * time.Hour)
	input := "\x1eabc\x1f" + end.Format(time.RFC3339) + "\x1f[fd/hls] fix\nyt_dlp/downloader/hls.py\n"
	commits, err := parseLog(input, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].Week != 8 {
		t.Fatalf("commits = %#v", commits)
	}
}

func TestBuildReportCountsMultiCategoryCommit(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	commits := []Commit{{Week: 1, Categories: []Category{CategoryExtractor, CategoryChallenge}}}
	report := buildReport("abc", start, start.Add(8*7*24*time.Hour), commits)
	if report.CommitCount != 1 || len(report.Weeks) != 8 || report.CategoryCounts[CategoryExtractor] != 1 || report.CategoryCounts[CategoryChallenge] != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func FuzzParseLog(f *testing.F) {
	f.Add("\x1eabc\x1f2026-01-02T00:00:00Z\x1fsubject\nREADME.md\n")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(8 * 7 * 24 * time.Hour)
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		_, _ = parseLog(input, start, end)
	})
}
