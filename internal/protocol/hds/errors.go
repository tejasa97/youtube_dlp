package hds

import "errors"

// Categorized error sentinels for the HDS downloader. They are exported so the
// product layer can map them to user-visible taxonomy without scraping strings.
var (
	ErrInvalidManifest   = errors.New("invalid HDS manifest")
	ErrInvalidBootstrap  = errors.New("invalid HDS bootstrap")
	ErrInvalidMedia      = errors.New("invalid HDS media descriptor")
	ErrInvalidConfig     = errors.New("invalid HDS downloader configuration")
	ErrUnsupportedDRM    = errors.New("HDS DRM/encrypted media is not supported")
	ErrUnsupportedLive   = errors.New("HDS live streams are not supported")
	ErrUnsupportedEmpty  = errors.New("HDS manifest has no selectable media")
	ErrFragmentTooLarge  = errors.New("HDS fragment exceeds size limit")
	ErrTooManySegments   = errors.New("HDS segment count exceeds limit")
	ErrTooManyFragments  = errors.New("HDS fragment count exceeds limit")
	ErrFragmentFetch     = errors.New("HDS fragment fetch failed")
	ErrUnsafeDestination = errors.New("HDS destination escapes output root")
)
