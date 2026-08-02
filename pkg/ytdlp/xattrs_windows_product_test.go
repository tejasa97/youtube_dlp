//go:build windows

package ytdlp

import "testing"

func TestProductXattrsUnsupportedIsPubliclyCategorized(t *testing.T) {
	err := categorized("write xattrs", ErrXattrsUnsupported)
	if !IsCategory(err, ErrorUnsupported) {
		t.Fatalf("xattrs error=%v", err)
	}
}
