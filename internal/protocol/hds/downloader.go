package hds

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

// Transport is the bounded HTTP surface used by the HDS downloader. It must
// neither auto-follow redirects nor attach ambient credentials: callers wrap
// their network.Client in a small adapter that exposes only this surface so
// the boundedFetch helper can walk redirects hop-by-hop and strip
// Authorization/Cookie/Proxy-Authorization/Referer before every request.
type Transport interface {
	DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error)
}

// Config configures a Downloader. Zero values select sensible defaults that
// match the documented VOD corpus. Validation rejects negative values before
// defaults are applied so caller mistakes cannot be silently masked.
type Config struct {
	Headers           http.Header
	Attempts          int
	RetryBaseDelay    time.Duration
	RetryMaxDelay     time.Duration
	MaxFragmentSize   int64
	MaxOutputBytes    int64
	ExtraSegmentQuery string
	RequestedBitrate  int64
}

// Default limits applied when Config leaves them unset. They mirror the
// documented VOD corpus sizes and the bounded page budgets used elsewhere in
// the product (HLS, ISM).
const (
	defaultAttempts       = 3
	defaultRetryBaseDelay = 250 * time.Millisecond
	defaultRetryMaxDelay  = 4 * time.Second
	defaultMaxFragment    = 64 << 20 // 64 MiB
	defaultMaxOutput      = 8 << 30  // 8 GiB
	maxRedirectHops       = 5
	tempFileRetries       = 8
)

// Result is the final outcome of a Download.
type Result struct {
	Path       string
	Bytes      int64
	Downloaded int
	Plan       []Fragment
}

// Downloader fetches an F4M manifest, resolves the bootstrap, plans the
// fragment schedule, fetches every F4F fragment in order, and assembles a
// single FLV file with one header, an optional metadata tag, and the
// concatenated mdat payloads.
type Downloader struct {
	transport Transport
	config    Config
}

// NewDownloader constructs a Downloader with a copy of the supplied config.
// Raw validation rejects negative values before defaults are applied so the
// caller cannot accidentally accept attacker-controlled inputs.
func NewDownloader(transport Transport, config Config) (*Downloader, error) {
	if transport == nil {
		return nil, fmt.Errorf("%w: nil transport", ErrInvalidConfig)
	}
	if config.Headers != nil {
		config.Headers = sanitizeHeaders(config.Headers)
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if config.Attempts == 0 {
		config.Attempts = defaultAttempts
	}
	if config.RetryBaseDelay == 0 {
		config.RetryBaseDelay = defaultRetryBaseDelay
	}
	if config.RetryMaxDelay == 0 {
		config.RetryMaxDelay = defaultRetryMaxDelay
	}
	if config.MaxFragmentSize == 0 {
		config.MaxFragmentSize = defaultMaxFragment
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = defaultMaxOutput
	}
	return &Downloader{transport: transport, config: config}, nil
}

// Download fetches the manifest at manifestURL, builds a fragment plan, and
// writes a single FLV output to destination under outputRoot. All intermediate
// artifacts are cleaned up on any failure so callers never observe a partial
// file at destination.
func (downloader *Downloader) Download(ctx context.Context, manifestURL, outputRoot, destination string, overwrite bool, sink events.Sink) (Result, error) {
	body, finalURL, err := downloader.fetchWithRetry(ctx, manifestURL, maxManifestBytes, "manifest")
	if err != nil {
		return Result{}, err
	}
	manifest, err := Parse(finalURL, body)
	if err != nil {
		return Result{}, err
	}
	media, err := manifest.SelectMedia(downloader.config.RequestedBitrate)
	if err != nil {
		return Result{}, err
	}
	var bootstrapBytes []byte
	if manifest.Bootstrap.URL != "" {
		bootstrapBytes, _, err = downloader.fetchWithRetry(ctx, manifest.Bootstrap.URL, maxBootstrapBytes, "bootstrap")
		if err != nil {
			return Result{}, err
		}
	} else {
		bootstrapBytes = manifest.Bootstrap.Inline
	}
	bootstrap, err := ParseBootstrap(bootstrapBytes)
	if err != nil {
		return Result{}, err
	}
	plan, err := BuildPlan(bootstrap)
	if err != nil {
		return Result{}, err
	}
	plan, err = ResolveFragmentURLs(media.URL, manifest.PV2Query, downloader.config.ExtraSegmentQuery, plan)
	if err != nil {
		return Result{}, err
	}
	return downloader.fetchAndAssemble(ctx, media, plan, outputRoot, destination, overwrite, sink)
}

// fetchWithRetry performs a bounded GET through a small redirect walker and
// retries up to Config.Attempts times with exponential backoff between the
// configured base and max delays. Status codes that look transient (5xx,
// 408, 429) are retried; everything else surfaces immediately.
func (downloader *Downloader) fetchWithRetry(ctx context.Context, rawURL string, limit int64, kind string) ([]byte, string, error) {
	var lastErr error
	for attempt := 1; attempt <= downloader.config.Attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		body, finalURL, err := downloader.boundedFetch(ctx, rawURL, limit, kind)
		if err == nil {
			return body, finalURL, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == downloader.config.Attempts {
			return nil, "", err
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(backoffDelay(downloader.config.RetryBaseDelay, downloader.config.RetryMaxDelay, attempt)):
		}
	}
	return nil, "", lastErr
}

// boundedFetch performs a credential-isolated GET that walks redirects
// hop-by-hop (capped at maxRedirectHops) and enforces a hard byte limit on
// each hop. The returned finalURL is the post-redirect URL, taken from the
// final hop's request URL rather than any server-supplied header.
func (downloader *Downloader) boundedFetch(ctx context.Context, rawURL string, limit int64, kind string) ([]byte, string, error) {
	current := rawURL
	for hop := 0; hop <= maxRedirectHops; hop++ {
		parsed, err := url.Parse(current)
		if err != nil {
			return nil, "", fmt.Errorf("%s fetch: invalid url", kind)
		}
		if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, "", fmt.Errorf("%s fetch: disallowed url scheme=%q host=%q", kind, parsed.Scheme, parsed.Host)
		}
		if parsed.User != nil {
			return nil, "", fmt.Errorf("%s fetch: url carries credentials", kind)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil, "", fmt.Errorf("%s fetch: build request: %w", kind, err)
		}
		// Headers are always the configured (already credential-isolated) set.
		// Each hop is a fresh request so cookies or auth added by the server
		// cannot leak between hops.
		request.Header = downloader.config.Headers.Clone()
		response, err := downloader.transport.DoWithoutCredentialsNoRedirect(ctx, request)
		if err != nil {
			return nil, "", redactAllURLs(fmt.Errorf("%s fetch: %w", kind, err))
		}
		if response == nil {
			return nil, "", fmt.Errorf("%s fetch: nil response", kind)
		}
		if response.Body == nil {
			return nil, "", fmt.Errorf("%s fetch: nil body", kind)
		}
		status := response.StatusCode
		location := response.Header.Get("Location")
		finalURL := request.URL.String()
		if response.Request != nil && response.Request.URL != nil {
			finalURL = response.Request.URL.String()
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
		closeErr := response.Body.Close()
		if readErr != nil {
			return nil, "", redactAllURLs(fmt.Errorf("%s fetch: read body: %w", kind, readErr))
		}
		if closeErr != nil {
			return nil, "", redactAllURLs(fmt.Errorf("%s fetch: close body: %w", kind, closeErr))
		}
		if int64(len(body)) > limit {
			return nil, "", fmt.Errorf("%s fetch: response exceeds %d bytes", kind, limit)
		}
		if isRedirect(status) {
			if hop == maxRedirectHops {
				return nil, "", fmt.Errorf("%s fetch: redirect chain exceeds %d hops", kind, maxRedirectHops)
			}
			next, err := resolveRedirect(parsed, location)
			if err != nil {
				return nil, "", fmt.Errorf("%s fetch: %w", kind, err)
			}
			current = next
			continue
		}
		if status < 200 || status >= 300 {
			return nil, "", &httpStatusError{Kind: kind, Status: status, URL: finalURL, Body: body}
		}
		return body, finalURL, nil
	}
	return nil, "", fmt.Errorf("fetch: redirect loop")
}

func isRedirect(status int) bool {
	return status == http.StatusMovedPermanently || status == http.StatusFound || status == http.StatusSeeOther || status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect
}

func resolveRedirect(base *url.URL, location string) (string, error) {
	if strings.TrimSpace(location) == "" {
		return "", fmt.Errorf("missing Location")
	}
	next, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("invalid Location: %w", err)
	}
	resolved := base.ResolveReference(next)
	if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Host == "" {
		return "", fmt.Errorf("disallowed redirect target scheme=%q host=%q", resolved.Scheme, resolved.Host)
	}
	if resolved.User != nil {
		return "", fmt.Errorf("redirect target carries credentials")
	}
	return resolved.String(), nil
}

// isRetryable returns true only when err looks like a transient network or
// retryable status. Non-network errors (config, manifest, bootstrap, DRM,
// live, redirect, oversize, missing-input) and context deadlines never
// benefit from another attempt.
func isRetryable(err error) bool {
	var status *httpStatusError
	if errors.As(err, &status) {
		return network.RetryableStatus(status.Status)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Domain errors are never retried because retrying them just wastes
	// the budget on deterministic failures.
	if errors.Is(err, ErrInvalidConfig) ||
		errors.Is(err, ErrInvalidManifest) ||
		errors.Is(err, ErrInvalidBootstrap) ||
		errors.Is(err, ErrUnsupportedDRM) ||
		errors.Is(err, ErrUnsupportedLive) ||
		errors.Is(err, ErrUnsafeDestination) ||
		errors.Is(err, ErrFragmentTooLarge) ||
		errors.Is(err, ErrTooManyFragments) ||
		errors.Is(err, ErrTooManySegments) {
		return false
	}
	// "invalid url" / "redirect" failures are deterministic.
	msg := err.Error()
	if strings.Contains(msg, "invalid url") ||
		strings.Contains(msg, "redirect") ||
		strings.Contains(msg, "disallowed") {
		return false
	}
	return true
}

// backoffDelay returns the wait between attempt N and N+1 using exponential
// growth bounded by the configured maximum.
func backoffDelay(base, max time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	if max <= 0 {
		max = defaultRetryMaxDelay
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= max {
			return max
		}
	}
	if delay > max {
		delay = max
	}
	return delay
}

// httpStatusError carries the kind, status, and redacted URL for a non-2xx
// response. We do NOT export the URL so callers must redact it explicitly.
type httpStatusError struct {
	Kind   string
	Status int
	URL    string
	Body   []byte
}

func (err *httpStatusError) Error() string {
	return fmt.Sprintf("%s fetch: HTTP %d for %s", err.Kind, err.Status, err.URL)
}

// redactAllURLs walks err and any wrapped error for a string-shaped URL or
// path; this best-effort scrub prevents signed URLs from reaching logs even
// when transport implementations embed them in error messages.
func redactAllURLs(err error) error {
	if err == nil {
		return nil
	}
	var status *httpStatusError
	if errors.As(err, &status) {
		status.URL = redactString(status.URL)
		return status
	}
	msg := err.Error()
	if !strings.Contains(msg, "http://") && !strings.Contains(msg, "https://") {
		return err
	}
	return fmt.Errorf("%s", redactMessage(msg))
}

// redactMessage replaces any http(s) URL substring with its redacted form.
// It always picks the earliest occurrence of either scheme so a stray
// later https cannot leak an earlier unredacted http URL.
func redactMessage(s string) string {
	var builder strings.Builder
	rest := s
	for {
		idx := indexAnyURL(rest)
		if idx < 0 {
			builder.WriteString(rest)
			return builder.String()
		}
		end := urlEnd(rest[idx:])
		builder.WriteString(rest[:idx])
		builder.WriteString(redactString(rest[idx : idx+end]))
		rest = rest[idx+end:]
	}
}

func indexAnyURL(s string) int {
	iHTTP := strings.Index(s, "http://")
	iHTTPS := strings.Index(s, "https://")
	switch {
	case iHTTP < 0:
		return iHTTPS
	case iHTTPS < 0:
		return iHTTP
	case iHTTP < iHTTPS:
		return iHTTP
	default:
		return iHTTPS
	}
}

func urlEnd(s string) int {
	for i, ch := range s {
		if ch == ' ' || ch == '\n' || ch == '\t' || ch == '"' || ch == '\'' || ch == ')' || ch == ']' || ch == '>' {
			return i
		}
	}
	return len(s)
}

func redactString(s string) string {
	parsed, err := url.Parse(s)
	if err != nil {
		return "<invalid URL>"
	}
	// network.RedactURL only strips sensitive query values. For error
	// messages we also strip the host because the URL itself may carry
	// signed tokens in the path or other side channels that the simple
	// query-redaction policy cannot reach.
	parsed.Host = "redacted"
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.ForceQuery = false
	parsed.RawQuery = ""
	if parsed.User != nil {
		parsed.User = nil
	}
	return parsed.String()
}

// fetchAndAssemble writes a single FLV file: header, optional metadata tag,
// then one mdat per fetched fragment. Every fragment fetch is bounded, the
// running output size is enforced incrementally, and any failure rolls back
// the partially-written file.
func (downloader *Downloader) fetchAndAssemble(
	ctx context.Context,
	media Media,
	plan []Fragment,
	outputRoot, destination string,
	overwrite bool,
	sink events.Sink,
) (Result, error) {
	absRoot, absDest, err := prepareOutputPaths(outputRoot, destination)
	if err != nil {
		return Result{}, err
	}
	if err := rejectSymlinkedAncestor(absRoot, absDest); err != nil {
		return Result{}, err
	}
	tempPath, output, err := createExclusiveTemp(absDest)
	if err != nil {
		return Result{}, err
	}
	cleanup := func() {
		_ = output.Close()
		_ = os.Remove(tempPath)
	}
	if err := writeFLVHeader(output); err != nil {
		cleanup()
		return Result{}, err
	}
	if err := writeFLVMetadataTag(output, media.Metadata); err != nil {
		cleanup()
		return Result{}, err
	}
	written := int64(len(flvHeader))
	if len(media.Metadata) > 0 {
		written += int64(flvTagHeaderLen) + int64(len(media.Metadata)) + 4
	}
	// Enforce the max-output cap immediately after header/metadata so even a
	// single oversized fragment cannot push us past the limit.
	if written > downloader.config.MaxOutputBytes {
		cleanup()
		return Result{}, fmt.Errorf("%w: header/metadata %d exceeds %d", ErrFragmentTooLarge, written, downloader.config.MaxOutputBytes)
	}
	downloads := 0
	for index, item := range plan {
		if err := ctx.Err(); err != nil {
			cleanup()
			return Result{}, err
		}
		data, err := downloader.fetchFragmentWithRetry(ctx, item.URL)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("fragment %d: %w", index+1, redactAllURLs(err))
		}
		mdat, err := extractMDAT(data)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("fragment %d mdat: %w", index+1, err)
		}
		if written+int64(len(mdat)) > downloader.config.MaxOutputBytes {
			cleanup()
			return Result{}, fmt.Errorf("%w: output would exceed %d bytes", ErrFragmentTooLarge, downloader.config.MaxOutputBytes)
		}
		if _, err := output.Write(mdat); err != nil {
			cleanup()
			return Result{}, fmt.Errorf("fragment %d append: %w", index+1, err)
		}
		written += int64(len(mdat))
		downloads++
		if sink != nil {
			if err := sink.Emit(ctx, events.Event{Kind: events.KindFragmentCompleted, Fragment: index + 1, Fragments: len(plan), Bytes: int64(len(mdat))}); err != nil {
				cleanup()
				return Result{}, fmt.Errorf("sink emit: %w", err)
			}
		}
	}
	if err := output.Sync(); err != nil {
		cleanup()
		return Result{}, err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(tempPath)
		return Result{}, err
	}
	if err := commitFile(tempPath, absDest, overwrite); err != nil {
		_ = os.Remove(tempPath)
		return Result{}, err
	}
	return Result{Path: absDest, Bytes: written, Downloaded: downloads, Plan: plan}, nil
}

// fetchFragmentWithRetry is the single-fragment counterpart of
// fetchWithRetry: bounded GET + hop walker + retry/backoff.
func (downloader *Downloader) fetchFragmentWithRetry(ctx context.Context, rawURL string) ([]byte, error) {
	body, _, err := downloader.fetchWithRetry(ctx, rawURL, downloader.config.MaxFragmentSize, "fragment")
	return body, err
}

// prepareOutputPaths validates that outputRoot exists (or can be created),
// that destination lives inside outputRoot, that any nested parent directory
// is created as a real directory (never a symlink), and that destination's
// own parent directory is not a symlink that would let the final rename
// escape. After this returns, absRoot and absDest are absolute, both real,
// and absDest sits strictly inside absRoot.
func prepareOutputPaths(outputRoot, destination string) (string, string, error) {
	if outputRoot == "" || destination == "" {
		return "", "", fmt.Errorf("%w: empty root or destination", ErrUnsafeDestination)
	}
	absRoot, err := filepath.Abs(outputRoot)
	if err != nil {
		return "", "", fmt.Errorf("%w: root abs: %v", ErrUnsafeDestination, err)
	}
	absDest, err := filepath.Abs(destination)
	if err != nil {
		return "", "", fmt.Errorf("%w: destination abs: %v", ErrUnsafeDestination, err)
	}
	if rel, relErr := filepath.Rel(absRoot, absDest); relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", ErrUnsafeDestination
	}
	// Create absRoot itself first, then walk every parent of absDest that is
	// strictly below absRoot and create it as a regular directory. We never
	// follow symlinks because each Mkdir call creates the directory atomically.
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir output: %w", err)
	}
	destParent := filepath.Dir(absDest)
	if destParent != absRoot {
		relParents, relErr := filepath.Rel(absRoot, destParent)
		if relErr != nil || strings.HasPrefix(relParents, "..") {
			return "", "", ErrUnsafeDestination
		}
		if err := os.MkdirAll(destParent, 0o755); err != nil {
			return "", "", fmt.Errorf("mkdir destination parent: %w", err)
		}
	}
	return absRoot, absDest, nil
}

// rejectSymlinkedAncestor walks from the destination's parent directory UP
// to (and including) absRoot, rejecting any symlink that would let the
// eventual rename escape. We deliberately stop at absRoot so system-level
// symlinks (macOS /var -> /private/var, /tmp -> /private/tmp) above the
// output root are out of scope for this product.
func rejectSymlinkedAncestor(absRoot, absDest string) error {
	current := filepath.Dir(absDest)
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("lstat %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: ancestor %s is a symlink", ErrUnsafeDestination, current)
		}
		if current == absRoot {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without finding absRoot; this means
			// absDest is not actually inside absRoot, which is a separate
			// invariant prepareOutputPaths already enforces. Stop here.
			return nil
		}
		current = parent
	}
}

// createExclusiveTemp creates a unique temp file in the same directory as
// absDest. It returns the path and an open writer. The temp file is created
// with O_CREATE|O_EXCL so concurrent writers cannot clobber it.
func createExclusiveTemp(absDest string) (string, *os.File, error) {
	dir := filepath.Dir(absDest)
	for attempt := 0; attempt < tempFileRetries; attempt++ {
		suffix, err := randomSuffix(16)
		if err != nil {
			return "", nil, fmt.Errorf("temp suffix: %w", err)
		}
		path := filepath.Join(dir, ".hds-"+suffix+".part")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return path, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("create temp: %w", err)
		}
	}
	return "", nil, fmt.Errorf("create temp: exhausted %d attempts", tempFileRetries)
}

func randomSuffix(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// commitFile replaces absDest with tempPath atomically. If the destination
// already exists, we rename it aside as a backup first, then rename tempPath
// into place. If the final rename fails, we restore from the backup so the
// old destination is never destroyed on failure. overwrite=false rejects any
// pre-existing destination.
//
// The destination is also revalidated at commit time: a TOCTOU race between
// prepareOutputPaths and commitFile could let a symlink be planted where we
// intend to write.
func commitFile(tempPath, absDest string, overwrite bool) error {
	info, statErr := os.Lstat(absDest)
	switch {
	case statErr == nil:
		// Destination exists; validate it.
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: destination is a symlink", ErrUnsafeDestination)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("destination is not a regular file")
		}
		if !overwrite {
			return fmt.Errorf("destination already exists")
		}
	case errors.Is(statErr, os.ErrNotExist):
		// Nothing to back up.
		return os.Rename(tempPath, absDest)
	default:
		return fmt.Errorf("destination lstat: %w", statErr)
	}
	// overwrite==true path. Rename the existing file aside, then rename
	// tempPath into place. If the second rename fails, restore the backup.
	backup := absDest + ".hdsbak"
	if err := os.Rename(absDest, backup); err != nil {
		return fmt.Errorf("backup destination: %w", err)
	}
	if err := os.Rename(tempPath, absDest); err != nil {
		// Rollback: restore the backup so the user's previous file is intact.
		// Best effort; if this fails too the user is left with a backup file
		// they can recover by hand.
		_ = os.Rename(backup, absDest)
		_ = os.Remove(tempPath)
		return fmt.Errorf("commit output: %w", err)
	}
	_ = os.Remove(backup)
	return nil
}

// validateConfig catches caller mistakes on the raw Config before defaults
// are applied, so negative values cannot be silently masked by the defaults.
func validateConfig(c Config) error {
	if c.Attempts < 0 || c.Attempts > 32 {
		return fmt.Errorf("%w: attempts", ErrInvalidConfig)
	}
	if c.RetryBaseDelay < 0 || c.RetryBaseDelay > time.Minute {
		return fmt.Errorf("%w: retry base delay", ErrInvalidConfig)
	}
	if c.RetryMaxDelay < 0 || c.RetryMaxDelay > time.Minute {
		return fmt.Errorf("%w: retry max delay", ErrInvalidConfig)
	}
	if c.RetryBaseDelay > c.RetryMaxDelay {
		return fmt.Errorf("%w: retry base > retry max", ErrInvalidConfig)
	}
	if c.MaxFragmentSize < 0 || c.MaxFragmentSize > (1<<30) {
		return fmt.Errorf("%w: max fragment size", ErrInvalidConfig)
	}
	if c.MaxOutputBytes < 0 || c.MaxOutputBytes > (32<<30) {
		return fmt.Errorf("%w: max output bytes", ErrInvalidConfig)
	}
	return nil
}

// sanitizeHeaders returns a copy of input with Authorization, Cookie,
// Proxy-Authorization, and Referer stripped. These are the headers most likely
// to leak credentials or user-tracking information across hosts. The downloader
// re-applies this filter before every fetch, so callers may pre-populate the
// config without worrying about cross-host leakage.
func sanitizeHeaders(input http.Header) http.Header {
	out := http.Header{}
	for k, v := range input {
		switch strings.ToLower(k) {
		case "authorization", "cookie", "proxy-authorization", "referer":
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}
