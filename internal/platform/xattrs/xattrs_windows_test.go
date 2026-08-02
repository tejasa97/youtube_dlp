//go:build windows

package xattrs

import (
	"errors"
	"testing"
)

func TestWindowsXattrsFailClosed(t *testing.T) {
	if Supported() {
		t.Fatal("windows xattrs unexpectedly reported supported")
	}
	if err := Set("fixture", "user.ytdlp", []byte("value")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("set error=%v", err)
	}
}
