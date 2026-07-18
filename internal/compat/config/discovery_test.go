package config

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultGroupsLinuxAndDarwin(t *testing.T) {
	for _, platform := range []Platform{PlatformLinux, PlatformDarwin} {
		groups := DefaultGroups(Environment{
			Platform: platform, HomeDir: "/home/tester", XDGConfigHome: "/xdg",
			AppData: "/roaming", ExecutableDir: "/bin", HomeConfigDir: "/downloads",
			SystemConfigDir: "/etc",
		})
		got := groupPaths(groups)
		want := [][]string{
			{"/bin/yt-dlp.conf"},
			{"/downloads/yt-dlp.conf"},
			{
				"/xdg/yt-dlp.conf", "/xdg/yt-dlp/config", "/xdg/yt-dlp/config.txt",
				"/roaming/yt-dlp.conf", "/roaming/yt-dlp/config", "/roaming/yt-dlp/config.txt",
				"/home/tester/yt-dlp.conf", "/home/tester/yt-dlp.conf.txt",
				"/home/tester/.yt-dlp/config", "/home/tester/.yt-dlp/config.txt",
			},
			{"/etc/yt-dlp.conf", "/etc/yt-dlp/config", "/etc/yt-dlp/config.txt"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s discovery mismatch:\n got %#v\nwant %#v", platform, got, want)
		}
	}
}

func TestDefaultGroupsWindows(t *testing.T) {
	groups := DefaultGroups(Environment{
		Platform: PlatformWindows, HomeDir: `C:\Users\tester`, XDGConfigHome: `C:\xdg`,
		AppData: `C:\Users\tester\AppData\Roaming`, ExecutableDir: `C:\bin`,
		HomeConfigDir: `D:\downloads`, SystemConfigDir: `C:\etc`,
	})
	got := groupPaths(groups)
	want := [][]string{
		{`C:\bin\yt-dlp.conf`},
		{`D:\downloads\yt-dlp.conf`},
		{
			`C:\xdg\yt-dlp.conf`, `C:\xdg\yt-dlp\config`, `C:\xdg\yt-dlp\config.txt`,
			`C:\Users\tester\AppData\Roaming\yt-dlp.conf`, `C:\Users\tester\AppData\Roaming\yt-dlp\config`, `C:\Users\tester\AppData\Roaming\yt-dlp\config.txt`,
			`C:\Users\tester\yt-dlp.conf`, `C:\Users\tester\yt-dlp.conf.txt`, `C:\Users\tester\.yt-dlp\config`, `C:\Users\tester\.yt-dlp\config.txt`,
		},
		{`C:\etc\yt-dlp.conf`, `C:\etc\yt-dlp\config`, `C:\etc\yt-dlp\config.txt`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("windows discovery mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestDefaultGroupsFallBackToHomeDotConfig(t *testing.T) {
	groups := DefaultGroups(Environment{Platform: PlatformLinux, HomeDir: "/home/t", SystemConfigDir: "/etc"})
	if got, want := groups[2].Candidates[0].Path, "/home/t/.config/yt-dlp.conf"; got != want {
		t.Fatalf("first user candidate = %q, want %q", got, want)
	}
}

func TestDiscoverSelectsExistingCandidatesAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	environment := Environment{Platform: PlatformLinux, HomeDir: filepath.Join(root, "home"), XDGConfigHome: filepath.Join(root, "xdg"), ExecutableDir: filepath.Join(root, "bin"), HomeConfigDir: filepath.Join(root, "home-config"), SystemConfigDir: filepath.Join(root, "etc")}
	groups := DefaultGroups(environment)
	writeFixture(t, groups[0].Candidates[0].Path, "--output portable")
	writeFixture(t, groups[2].Candidates[2].Path, "--output user")
	selected, err := Discover(context.Background(), environment, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got := []SourceKind{selected[0].Kind, selected[1].Kind}; !reflect.DeepEqual(got, []SourceKind{SourcePortable, SourceUser}) {
		t.Fatalf("selected = %#v", selected)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, environment, DefaultLimits()); !IsCategory(err, ErrorCanceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func groupPaths(groups []Group) [][]string {
	result := make([][]string, len(groups))
	for groupIndex, group := range groups {
		for _, candidate := range group.Candidates {
			result[groupIndex] = append(result[groupIndex], filepath.ToSlash(candidate.Path))
		}
	}
	return result
}
