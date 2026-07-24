package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/sponsorblock"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestBuildSponsorBlockOptionsMapping(t *testing.T) {
	all := make([]string, 0, len(sponsorblock.AllCategories()))
	for _, category := range sponsorblock.AllCategories() {
		all = append(all, string(category))
	}
	withoutPreview := make([]string, 0, len(all)-1)
	withoutIntro := make([]string, 0, len(all)-1)
	for _, category := range all {
		if category != "preview" {
			withoutPreview = append(withoutPreview, category)
		}
		if category != "intro" {
			withoutIntro = append(withoutIntro, category)
		}
	}

	for _, tc := range []struct {
		name      string
		markSpecs []string
		api       string
		want      ytdlp.SponsorBlockOptions
		wantErr   string
	}{
		{name: "disabled empty", want: ytdlp.SponsorBlockOptions{}},
		{
			name:      "mark sponsor",
			markSpecs: []string{"sponsor"},
			want:      ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: []string{"sponsor"}},
		},
		{
			name:      "mark all",
			markSpecs: []string{"all"},
			want:      ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: all},
		},
		{
			name:      "mark default alias",
			markSpecs: []string{"default"},
			want:      ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: all},
		},
		{
			name:      "dedupe and preserve order",
			markSpecs: []string{"sponsor, intro, sponsor"},
			want:      ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: []string{"sponsor", "intro"}},
		},
		{
			name:      "repeated flags accumulate",
			markSpecs: []string{"sponsor", "intro", "selfpromo"},
			want: ytdlp.SponsorBlockOptions{
				Enabled: true, Mark: true, Categories: []string{"sponsor", "intro", "selfpromo"},
			},
		},
		{
			name:      "all excludes preview",
			markSpecs: []string{"all,-preview"},
			want:      ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: withoutPreview},
		},
		{
			name:      "default excludes intro",
			markSpecs: []string{"default,-intro"},
			want:      ytdlp.SponsorBlockOptions{Enabled: true, Mark: true, Categories: withoutIntro},
		},
		{
			name:      "accumulate then exclude",
			markSpecs: []string{"sponsor,intro", "-intro,preview"},
			want: ytdlp.SponsorBlockOptions{
				Enabled: true, Mark: true, Categories: []string{"sponsor", "preview"},
			},
		},
		{
			name:      "exclude all alone disables",
			markSpecs: []string{"-all"},
			want:      ytdlp.SponsorBlockOptions{},
		},
		{
			name:      "sponsor then exclude sponsor disables",
			markSpecs: []string{"sponsor,-sponsor"},
			want:      ytdlp.SponsorBlockOptions{},
		},
		{
			name:      "exclusion on initially empty set allowed",
			markSpecs: []string{"-sponsor"},
			want:      ytdlp.SponsorBlockOptions{},
		},
		{
			name: "api base without mark",
			api:  "https://sponsor.example.test",
			want: ytdlp.SponsorBlockOptions{APIBase: "https://sponsor.example.test"},
		},
		{
			name:      "mark with api",
			markSpecs: []string{"selfpromo"},
			api:       "http://127.0.0.1:9",
			want: ytdlp.SponsorBlockOptions{
				Enabled: true, Mark: true, Categories: []string{"selfpromo"}, APIBase: "http://127.0.0.1:9",
			},
		},
		{name: "unknown category", markSpecs: []string{"sponsor,not-a-category"}, wantErr: "unknown SponsorBlock category"},
		{name: "empty mark list", markSpecs: []string{" , "}, wantErr: "sponsorblock-mark requires at least one category"},
		{name: "explicit empty mark", markSpecs: []string{""}, wantErr: "sponsorblock-mark requires at least one category"},
		{name: "malformed empty comma token", markSpecs: []string{"sponsor,,intro"}, wantErr: "sponsorblock-mark requires at least one category"},
		{name: "bad api scheme", api: "javascript:alert(1)", wantErr: "SponsorBlock API base scheme"},
		{name: "bad api host", markSpecs: []string{"sponsor"}, api: "https:///missing-host", wantErr: "SponsorBlock API base host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var categories []string
			var err error
			for _, spec := range tc.markSpecs {
				categories, err = parseSponsorBlockCategories(spec, categories)
				if err != nil {
					break
				}
			}
			var got ytdlp.SponsorBlockOptions
			if err == nil {
				got, err = buildSponsorBlockOptions(categories, tc.api)
			}
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
			name:      "explicit empty mark",
			arguments: []string{"--sponsorblock-mark=", "https://example.invalid/watch"},
			want:      "sponsorblock-mark requires at least one category",
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

func TestNoSponsorBlockKeepsAPIBase(t *testing.T) {
	got, err := buildSponsorBlockOptions(nil, "https://sponsor.example.test")
	if err != nil {
		t.Fatal(err)
	}
	want := ytdlp.SponsorBlockOptions{APIBase: "https://sponsor.example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
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

func TestRunNoSponsorBlockAuthoritativeOrdering(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":"cli-sb","title":"CLI SB","ext":"mp4","duration":60,"formats":[{"format_id":"f","url":"https://media.invalid/f.mp4","ext":"mp4"}]}`)
	}))
	defer page.Close()

	for _, tc := range []struct {
		name      string
		arguments []string
	}{
		{
			name: "no-sponsorblock before mark",
			arguments: []string{
				"--no-sponsorblock",
				"--sponsorblock-mark", "sponsor",
				"--sponsorblock-api", "https://sponsor.example.test",
				"--skip-download",
				"--print-json",
				page.URL + "/page",
			},
		},
		{
			name: "no-sponsorblock after mark",
			arguments: []string{
				"--sponsorblock-mark", "sponsor",
				"--sponsorblock-api", "https://sponsor.example.test",
				"--no-sponsorblock",
				"--skip-download",
				"--print-json",
				page.URL + "/page",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tc.arguments, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			var metadata map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &metadata); err != nil {
				t.Fatal(err)
			}
			if _, exists := metadata["sponsorblock_chapters"]; exists {
				t.Fatalf("no-sponsorblock still wrote sponsorblock_chapters: %#v", metadata["sponsorblock_chapters"])
			}
		})
	}
}

func TestRunSponsorBlockConfigCommandLinePrecedence(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"id":"cli-sb","title":"CLI SB","ext":"mp4","duration":60,"formats":[{"format_id":"f","url":"https://media.invalid/f.mp4","ext":"mp4"}]}`)
	}))
	defer page.Close()

	t.Run("config no-sponsorblock plus cli mark remains disabled", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
		if err := os.WriteFile(configPath, []byte("--no-sponsorblock\n--sponsorblock-api https://sponsor.example.test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Pinned yt-dlp applies opts.no_sponsorblock after option parsing, so a
		// CLI mark cannot re-enable marking once --no-sponsorblock appears
		// anywhere in config or command line.
		var stdout, stderr bytes.Buffer
		code := Run([]string{
			"--config-location", configPath,
			"--sponsorblock-mark", "sponsor",
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
			t.Fatalf("no-sponsorblock still wrote sponsorblock_chapters: %#v", metadata["sponsorblock_chapters"])
		}
	})

	t.Run("cli no-sponsorblock overrides config mark", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
		if err := os.WriteFile(configPath, []byte("--sponsorblock-mark all\n"), 0o600); err != nil {
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
	})

	t.Run("cli api overrides config api", func(t *testing.T) {
		categories, err := parseSponsorBlockCategories("sponsor", nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := buildSponsorBlockOptions(categories, "https://cli.example.test")
		if err != nil {
			t.Fatal(err)
		}
		if got.APIBase != "https://cli.example.test" {
			t.Fatalf("APIBase = %q", got.APIBase)
		}
	})

	t.Run("api base survives no-sponsorblock", func(t *testing.T) {
		got, err := buildSponsorBlockOptions(nil, "https://sponsor.example.test")
		if err != nil {
			t.Fatal(err)
		}
		if got.APIBase != "https://sponsor.example.test" {
			t.Fatalf("APIBase = %q", got.APIBase)
		}
		if got.Enabled || got.Mark || len(got.Categories) != 0 {
			t.Fatalf("marking not disabled: %#v", got)
		}
	})

	t.Run("repeated marks accumulate", func(t *testing.T) {
		categories, err := parseSponsorBlockCategories("sponsor", nil)
		if err != nil {
			t.Fatal(err)
		}
		categories, err = parseSponsorBlockCategories("intro", categories)
		if err != nil {
			t.Fatal(err)
		}
		got, err := buildSponsorBlockOptions(categories, "")
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"sponsor", "intro"}
		if !reflect.DeepEqual(got.Categories, want) {
			t.Fatalf("categories = %#v, want %#v", got.Categories, want)
		}
	})
}
