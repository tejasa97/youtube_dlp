package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type fakeXattrBackend struct {
	values           map[string][]byte
	setCalls         int
	failName         string
	failOnce         bool
	failRollbackName string
	failRollback     bool
	onSet            func()
}

func (backend *fakeXattrBackend) Supported() bool { return true }

func (backend *fakeXattrBackend) List(string) (map[string][]byte, error) {
	copyValues := make(map[string][]byte, len(backend.values))
	for name, value := range backend.values {
		copyValues[name] = append([]byte(nil), value...)
	}
	return copyValues, nil
}

func (backend *fakeXattrBackend) Set(_ string, name string, value []byte) error {
	backend.setCalls++
	if backend.onSet != nil {
		backend.onSet()
	}
	if backend.failOnce && name == backend.failName {
		backend.failOnce = false
		return errors.New("injected xattr write failure")
	}
	if backend.failRollback && name == backend.failRollbackName && backend.setCalls >= 2 {
		return errors.New("injected xattr rollback failure")
	}
	backend.values[name] = append([]byte(nil), value...)
	return nil
}

func (backend *fakeXattrBackend) Remove(_ string, name string) error {
	delete(backend.values, name)
	return nil
}

func TestXattrsPartialWriteFailureRestoresMappedValues(t *testing.T) {
	media := filepath.Join(t.TempDir(), "media.mp4")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &fakeXattrBackend{
		values:   map[string][]byte{"user.dublincore.description": []byte("old")},
		failName: "user.dublincore.title", failOnce: true,
	}
	xattrBackendOverride = backend
	defer func() { xattrBackendOverride = nil }()
	operation := &operation{client: NewClient(), request: Request{Xattrs: true, OutputDir: filepath.Dir(media)}}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("new title")},
		value.Field{Key: "description", Value: value.String("new description")},
	))
	err := operation.applyXattrs(context.Background(), info, media)
	if err == nil || !errors.Is(err, ErrXattrsUnsupported) {
		t.Fatalf("write failure error=%v", err)
	}
	if string(backend.values["user.dublincore.description"]) != "old" {
		t.Fatalf("prior attribute not restored: %q", backend.values)
	}
	if _, exists := backend.values["user.dublincore.title"]; exists {
		t.Fatalf("new attribute survived rollback: %q", backend.values)
	}
}

func TestXattrsCancellationBetweenWritesRestoresMappedValues(t *testing.T) {
	media := filepath.Join(t.TempDir(), "media.mp4")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	backend := &fakeXattrBackend{values: map[string][]byte{"user.dublincore.description": []byte("old")}}
	backend.onSet = func() {
		if backend.setCalls == 1 {
			cancel()
		}
	}
	xattrBackendOverride = backend
	defer func() { xattrBackendOverride = nil }()
	operation := &operation{client: NewClient(), request: Request{Xattrs: true, OutputDir: filepath.Dir(media)}}
	err := operation.applyXattrs(ctx, value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("new title")},
		value.Field{Key: "description", Value: value.String("new description")},
	)), media)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	if string(backend.values["user.dublincore.description"]) != "old" {
		t.Fatalf("prior attribute not restored after cancellation: %q", backend.values)
	}
	if _, exists := backend.values["user.dublincore.title"]; exists {
		t.Fatalf("new attribute survived cancellation: %q", backend.values)
	}
}

func TestXattrsRollbackFailureIsReported(t *testing.T) {
	media := filepath.Join(t.TempDir(), "media.mp4")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &fakeXattrBackend{
		values:           map[string][]byte{"user.dublincore.description": []byte("old")},
		failName:         "user.dublincore.title",
		failOnce:         true,
		failRollbackName: "user.dublincore.description",
		failRollback:     true,
	}
	xattrBackendOverride = backend
	defer func() { xattrBackendOverride = nil }()
	operation := &operation{client: NewClient(), request: Request{Xattrs: true, OutputDir: filepath.Dir(media)}}
	err := operation.applyXattrs(context.Background(), value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("new title")},
		value.Field{Key: "description", Value: value.String("new description")},
	)), media)
	if err == nil || !errors.Is(err, ErrXattrsUnsupported) || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("rollback failure error=%v", err)
	}
}
