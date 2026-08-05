package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/platform/xattrs"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

var ErrXattrsUnsupported = errors.New("extended attributes are unsupported")

const (
	maxXattrMappings   = 7
	maxXattrNameBytes  = 128
	maxXattrValueBytes = 4096
	maxXattrTotalBytes = 16 << 10
)

type xattrMapping struct {
	name       string
	field      string
	appleOnly  bool
	formatDate bool
	applePlist bool
}

var supportedXattrMappings = []xattrMapping{
	{name: "user.xdg.referrer.url", field: "webpage_url"},
	{name: "user.dublincore.title", field: "title"},
	{name: "user.dublincore.date", field: "upload_date", formatDate: true},
	{name: "user.dublincore.contributor", field: "uploader"},
	{name: "user.dublincore.format", field: "format"},
	{name: "user.dublincore.description", field: "description"},
	{name: "com.apple.metadata:kMDItemWhereFroms", field: "webpage_url", appleOnly: true, applePlist: true},
}

type xattrBackend interface {
	Supported() bool
	List(string) (map[string][]byte, error)
	Set(string, string, []byte) error
	Remove(string, string) error
}

type platformXattrBackend struct{}

func (platformXattrBackend) Supported() bool { return xattrs.Supported() }
func (platformXattrBackend) List(path string) (map[string][]byte, error) {
	return xattrs.List(path)
}
func (platformXattrBackend) Set(path, name string, value []byte) error {
	return xattrs.Set(path, name, value)
}
func (platformXattrBackend) Remove(path, name string) error {
	return xattrs.Remove(path, name)
}

var xattrBackendOverride xattrBackend

func currentXattrBackend() xattrBackend {
	if xattrBackendOverride != nil {
		return xattrBackendOverride
	}
	return platformXattrBackend{}
}

func (operation *operation) applyXattrs(ctx context.Context, info value.Info, mediaPath string) error {
	if !operation.request.Xattrs {
		return nil
	}
	backend := currentXattrBackend()
	if !backend.Supported() {
		return ErrXattrsUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	home := operation.request.outputRoot(OutputPathHome)
	confined, err := confinedPostprocessPath(home, mediaPath)
	if err != nil {
		return fmt.Errorf("%w: unsafe media path", ErrXattrsUnsupported)
	}
	fileInfo, err := os.Lstat(confined)
	if err != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("%w: media path is not a regular file", ErrXattrsUnsupported)
	}
	values, err := xattrValues(info)
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return nil
	}
	if err := operation.client.emit(ctx, Event{Kind: EventPostprocessStarting, Path: confined}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	previous, err := backend.List(confined)
	if err != nil {
		return fmt.Errorf("%w: read existing metadata", ErrXattrsUnsupported)
	}
	written := make([]string, 0, len(values))
	rollback := func() error {
		var rollbackErrs []error
		for index := len(written) - 1; index >= 0; index-- {
			name := written[index]
			if old, ok := previous[name]; ok {
				if err := backend.Set(confined, name, old); err != nil {
					rollbackErrs = append(rollbackErrs, err)
				}
			} else {
				if err := backend.Remove(confined, name); err != nil {
					rollbackErrs = append(rollbackErrs, err)
				}
			}
		}
		return errors.Join(rollbackErrs...)
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return joinXattrRollback(err, rollback())
		}
		payload := values[name]
		if err := backend.Set(confined, name, payload); err != nil {
			return joinXattrRollback(fmt.Errorf("%w: write metadata", ErrXattrsUnsupported), rollback())
		}
		written = append(written, name)
	}
	if err := operation.client.emit(ctx, Event{Kind: EventPostprocessCompleted, Path: confined}); err != nil {
		return joinXattrRollback(err, rollback())
	}
	return nil
}

func joinXattrRollback(primary, rollback error) error {
	if rollback == nil {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("xattrs rollback failed: %w", rollback))
}

func xattrValues(info value.Info) (map[string][]byte, error) {
	if len(supportedXattrMappings) > maxXattrMappings {
		return nil, fmt.Errorf("%w: mapping count", ErrXattrsUnsupported)
	}
	values := make(map[string][]byte, len(supportedXattrMappings))
	total := 0
	for _, mapping := range supportedXattrMappings {
		if mapping.appleOnly && runtime.GOOS != "darwin" {
			continue
		}
		if len(mapping.name) > maxXattrNameBytes {
			return nil, fmt.Errorf("%w: metadata name bound", ErrXattrsUnsupported)
		}
		text, ok := info.Lookup(mapping.field).StringValue()
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		if mapping.formatDate && len(text) == 8 {
			text = text[:4] + "-" + text[4:6] + "-" + text[6:]
		}
		if mapping.applePlist {
			text = `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><array><string>` + html.EscapeString(text) + `</string></array></plist>`
		}
		if len(text) > maxXattrValueBytes || strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
			return nil, fmt.Errorf("%w: metadata value bound", ErrXattrsUnsupported)
		}
		payload := []byte(text)
		values[mapping.name] = payload
		total += len(payload)
		if total > maxXattrTotalBytes {
			return nil, fmt.Errorf("%w: metadata total bound", ErrXattrsUnsupported)
		}
	}
	return values, nil
}
