package archive

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIdentityReferenceSemantics(t *testing.T) {
	identity, err := NewIdentity("YouTube", "BaW_jenozKc")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := identity.String(), "youtube BaW_jenozKc"; got != want {
		t.Fatalf("identity = %q, want %q", got, want)
	}
	parsed, err := ParseIdentity(identity.String())
	if err != nil || parsed != identity {
		t.Fatalf("parsed = %#v, %v", parsed, err)
	}
	for _, invalid := range []string{"", "youtube", "YouTube id", "youtube ", " youtube id", "youtube id\nnext"} {
		if _, err := ParseIdentity(invalid); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("ParseIdentity(%q) error = %v", invalid, err)
		}
	}
}

func TestArchivePreloadStripsSurroundingWhitespaceLikeReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive")
	if err := os.WriteFile(path, []byte("  youtube id  \n\tvimeo legacy\t\n\u2003generic opaque\u2003\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"youtube id", "vimeo legacy", "generic opaque"}
	if strings.Join(entries, "|") != strings.Join(want, "|") {
		t.Fatalf("stripped entries = %#v", entries)
	}
	identity, _ := NewIdentity("youtube", "id")
	found, err := store.Contains(context.Background(), identity)
	if err != nil || !found {
		t.Fatalf("Contains stripped identity = %v, %v", found, err)
	}
}

func TestArchiveMatchMigrationAndAtomicFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.txt")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "conformance", "archive", "reference.archive"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := NewIdentity("GenericV2", "new-id")
	matched, found, err := store.Match(context.Background(), current, []string{"generic legacy-id"})
	if err != nil || !found || matched != "generic legacy-id" {
		t.Fatalf("Match = %q, %v, %v", matched, found, err)
	}
	changed, err := store.Migrate(context.Background(), map[string]Identity{"generic legacy-id": current})
	if err != nil || !changed {
		t.Fatalf("Migrate = %v, %v", changed, err)
	}
	entries, err := store.Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"youtube BaW_jenozKc", "vimeo 123456", "genericv2 new-id"}
	if strings.Join(entries, "|") != strings.Join(want, "|") {
		t.Fatalf("entries = %#v", entries)
	}
	before, _ := os.ReadFile(path)
	limited, err := Open(context.Background(), path, Options{MaxFileBytes: int64(len(before))})
	if err != nil {
		t.Fatal(err)
	}
	extra, _ := NewIdentity("youtube", "another")
	if _, err := limited.Record(context.Background(), extra); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Record error = %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("failed atomic update changed archive")
	}
}

func TestArchiveConcurrentDuplicatePrevention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.txt")
	const workers = 48
	var wait sync.WaitGroup
	wait.Add(workers)
	errorsFound := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			store, err := Open(context.Background(), path, Options{})
			if err != nil {
				errorsFound <- err
				return
			}
			identity, _ := NewIdentity("YouTube", "same-id")
			_, err = store.Record(context.Background(), identity)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "youtube same-id\n"); got != 1 {
		t.Fatalf("record count = %d, archive = %q", got, data)
	}
}

func TestArchiveCrossProcessLocking(t *testing.T) {
	if os.Getenv("YT_DLP_GO_ARCHIVE_HELPER") != "" {
		path := os.Getenv("YT_DLP_GO_ARCHIVE_PATH")
		store, err := Open(context.Background(), path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 20; index++ {
			identity, _ := NewIdentity("helper", strconv.Itoa(index))
			if _, err := store.Record(context.Background(), identity); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	path := filepath.Join(t.TempDir(), "archive.txt")
	commands := make([]*exec.Cmd, 4)
	for index := range commands {
		commands[index] = exec.Command(os.Args[0], "-test.run=^TestArchiveCrossProcessLocking$")
		commands[index].Env = append(os.Environ(), "YT_DLP_GO_ARCHIVE_HELPER=1", "YT_DLP_GO_ARCHIVE_PATH="+path)
		if err := commands[index].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper: %v", err)
		}
	}
	store, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := store.Entries(context.Background())
	if len(entries) != 20 {
		t.Fatalf("entries = %d, want 20", len(entries))
	}
}

func TestArchiveLockCancellationAndStaleRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.txt")
	lockPath := path + ".lock"
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, "owner"), []byte("owner\n"+strconv.FormatInt(time.Now().UnixNano(), 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path, Options{LockPoll: time.Millisecond, StaleLockAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	identity, _ := NewIdentity("youtube", "cancel")
	if _, err := store.Record(ctx, identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Record cancellation = %v", err)
	}
	now := time.Now()
	store.options.Clock = func() time.Time { return now.Add(2 * time.Hour) }
	if added, err := store.Record(context.Background(), identity); err != nil || !added {
		t.Fatalf("stale recovery = %v, %v", added, err)
	}
}

func TestArchiveRejectsSymlinkCorruptionAndOversize(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("youtube safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "archive")
	if err := os.Symlink(target, link); err == nil {
		if _, err := Open(context.Background(), link, Options{}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("symlink error = %v", err)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	corrupt := filepath.Join(directory, "corrupt")
	if err := os.WriteFile(corrupt, []byte{'x', ' ', 0xff, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), corrupt, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt error = %v", err)
	}
	oversize := filepath.Join(directory, "oversize")
	if err := os.WriteFile(oversize, []byte("youtube "+strings.Repeat("x", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), oversize, Options{MaxRecordBytes: 16}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestArchiveAcceptsTruncatedFinalRecordAndDoesNotMutateHardlink(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "archive")
	if err := os.WriteFile(path, []byte("youtube first\nvimeo final-without-newline"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.Entries(context.Background())
	if err != nil || len(entries) != 2 || entries[1] != "vimeo final-without-newline" {
		t.Fatalf("truncated final record = %#v, %v", entries, err)
	}
	linked := filepath.Join(directory, "linked")
	if err := os.Link(path, linked); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("hard links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	linkedBefore, _ := os.ReadFile(linked)
	identity, _ := NewIdentity("youtube", "second")
	if _, err := store.Record(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	linkedAfter, _ := os.ReadFile(linked)
	if string(linkedAfter) != string(linkedBefore) {
		t.Fatal("atomic archive replacement mutated a hard-linked inode")
	}
}

func TestArchiveRejectsMaliciousLockSymlink(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "archive")
	target := filepath.Join(directory, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".lock"); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := NewIdentity("youtube", "safe")
	if _, err := store.Record(context.Background(), identity); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("lock symlink error = %v", err)
	}
}

func TestArchiveErrorsDoNotExposeSecrets(t *testing.T) {
	secret := "token=top-secret"
	path := filepath.Join(t.TempDir(), secret)
	if err := os.WriteFile(path, []byte{'x', 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), path, Options{})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("unsafe diagnostic: %v", err)
	}
}

func FuzzParseIdentity(f *testing.F) {
	for _, seed := range []string{"youtube BaW_jenozKc", "generic videos", "", "YouTube id", "x a b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 32<<10 {
			return
		}
		identity, err := ParseIdentity(input)
		if err == nil {
			if identity.String() != input {
				t.Fatalf("round trip = %q, want %q", identity.String(), input)
			}
			if _, err := NewIdentity(identity.Extractor, identity.VideoID); err != nil {
				t.Fatalf("NewIdentity rejected parsed value: %v", err)
			}
		}
	})
}
