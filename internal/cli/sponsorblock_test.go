package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/sponsorblock"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestBuildSponsorBlockOptionsMapping(t *testing.T) {
	all := make([]string, 0, len(sponsorblock.AllCategories()))
	for _, category := range sponsorblock.AllCategories() {
		all = append(all, string(category))
	}
	for _, tc := range []struct {
		name    string
		mark    string
		api     string
		want    ytdlp.SponsorBlockOptions
		wantErr string
	}{
		{name: "disabled empty", want: ytdlp.SponsorBlockOptions{}},
		{
			name: "mark sponsor",
			mark: "sponsor",
			want: ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: []string{"sponsor"}},
		},
		{
			name: "mark all",
			mark: "all",
			want: ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: all},
		},
		{
			name: "mark default alias",
			mark: "default",
			want: ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: all},
		},
		{
			name: "dedupe and preserve order",
			mark: "sponsor, intro, sponsor",
			want: ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: []string{"sponsor", "intro"}},
		},
		{
			name: "api base without mark",
			api:  "https://sponsor.example.test",
			want: ytdlp.SponsorBlockOptions{APIBase: "https://sponsor.example.test"},
		},
		{
			name: "mark with api",
			mark: "selfpromo",
			api:  "http://127.0.0.1:9",
			want: ytdlp.SponsorBlockOptions{
				Enabled: true, Mark: true, Categories: []string{"selfpromo"}, APIBase: "http://127.0.0.1:9",
			},
		},
		{name: "unknown category", mark: "sponsor,not-a-category", wantErr: "unknown SponsorBlock category"},
		{name: "empty mark list", mark: " , ", wantErr: "sponsorblock-mark requires at least one category"},
		{name: "bad api scheme", api: "javascript:alert(1)", wantErr: "SponsorBlock API base scheme"},
		{name: "bad api host", mark: "sponsor", api: "https:///missing-host", wantErr: "SponsorBlock API base host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildSponsorBlockOptions(tc.mark, tc.api)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("options = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRunRejectsInvalidSponsorBlockBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{
			name:      "unknown category",
			arguments: []string{"--sponsorblock-mark", "nope", "https://example.invalid/watch"},
			want:      "unknown SponsorBlock category",
		},
		{
			name:      "bad api base",
			arguments: []string{"--sponsorblock-mark", "sponsor", "--sponsorblock-api", "javascript:alert(1)", "https://example.invalid/watch"},
			want:      "SponsorBlock API base scheme",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tc.arguments, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestRunNoSponsorBlockClearsInheritedMark(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":"cli-sb","title":"CLI SB","ext":"mp4","duration":60,"formats":[{"format_id":"f","url":"https://media.invalid/f.mp4","ext":"mp4"}]}`)
	}))
	defer page.Close()

	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	if err := os.WriteFile(configPath, []byte("--sponsorblock-mark all\n--sponsorblock-api https://sponsor.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--config-location", configPath,
		"--no-sponsorblock",
		"--skip-download",
		"--print-json",
		page.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var metadata map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &metadata); err != nil {
		t.Fatal(err)
	}
	if _, exists := metadata["sponsorblock_chapters"]; exists {
		t.Fatalf("cleared enablement still wrote sponsorblock_chapters: %#v", metadata["sponsorblock_chapters"])
	}
}

func TestRunSponsorBlockMarkEndToEnd(t *testing.T) {
	var calls atomic.Int32
	sponsor := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" ||
			request.Header.Get("Proxy-Authorization") != "" {
			t.Errorf("credential reached SponsorBlock: %v", request.Header)
		}
		if request.URL.Query().Get("service") != "YouTube" {
			t.Errorf("service = %q", request.URL.Query().Get("service"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[{"videoID":"abc123","segments":[
			{"segment":[10,20],"category":"sponsor","actionType":"skip","videoDuration":60},
			{"segment":[30,40],"category":"intro","actionType":"skip","videoDuration":60}
		]}]`)
	}))
	defer sponsor.Close()

	page := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{
			"id":"abc123","title":"Marked Video","ext":"mp4","duration":60,
			"chapters":[{"start_time":0,"end_time":60,"title":"Full","custom":"kept"}],
			"formats":[{"format_id":"f","url":"https://media.invalid/f.mp4","ext":"mp4","vcodec":"none","acodec":"mp4a"}]
		}`)
	}))
	defer page.Close()

	cookiePath := filepath.Join(t.TempDir(), "cookies.txt")
	cookieBody := "# Netscape HTTP Cookie File\n.example.test\tTRUE\t/\tFALSE\t0\tsession\tmust-not-leak\n"
	if err := os.WriteFile(cookiePath, []byte(cookieBody), 0o600); err != nil {
		t.Fatal(err)
	}

	previous := extraClientOptions
	extraClientOptions = []ytdlp.Option{ytdlp.WithExtractors(sponsorBlockYouTubeFixture{})}
	defer func() { extraClientOptions = previous }()

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--skip-download",
		"--print-json",
		"--cookies", cookiePath,
		"--sponsorblock-mark", "sponsor,intro",
		"--sponsorblock-api", sponsor.URL,
		page.URL + "/sponsorblock-page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("SponsorBlock calls = %d", calls.Load())
	}
	var metadata map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &metadata); err != nil {
		t.Fatal(err)
	}
	chapters, ok := metadata["sponsorblock_chapters"].([]any)
	if !ok || len(chapters) != 2 {
		t.Fatalf("sponsorblock_chapters = %#v", metadata["sponsorblock_chapters"])
	}
	first := chapters[0].(map[string]any)
	if first["category"] != "sponsor" || first["title"] != "Sponsor" {
		t.Fatalf("first chapter = %#v", first)
	}
	marked, ok := metadata["chapters"].([]any)
	if !ok || len(marked) < 3 {
		t.Fatalf("marked chapters = %#v", metadata["chapters"])
	}
	rendered := stderr.String() + stdout.String()
	if strings.Contains(rendered, "must-not-leak") {
		t.Fatalf("cookie leaked into CLI output: %q", rendered)
	}
}

type sponsorBlockYouTubeFixture struct{}

func (sponsorBlockYouTubeFixture) Name() string { return "youtube" }

func (sponsorBlockYouTubeFixture) Suitable(parsed *url.URL) bool {
	return parsed != nil && strings.HasSuffix(parsed.Path, "/sponsorblock-page")
}

func (sponsorBlockYouTubeFixture) Extract(ctx context.Context, request extractor.Request) (extractor.Extraction, error) {
	body, _, err := request.Transport.ReadPage(ctx, request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	var root value.Value
	if err := json.Unmarshal(body, &root); err != nil {
		return extractor.Extraction{}, err
	}
	object, ok := root.Object()
	if !ok {
		return extractor.Extraction{}, fmt.Errorf("fixture root")
	}
	return extractor.Media(value.NewInfo(object)), nil
}
