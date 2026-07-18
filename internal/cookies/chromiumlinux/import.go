package chromiumlinux

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const chromeEpochOffsetSeconds int64 = 11_644_473_600

func Import(ctx context.Context, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	settings, err := browserSettingsFor(options.Browser)
	if err != nil {
		return Result{}, err
	}
	path, err := locate(options, settings)
	if err != nil {
		return Result{}, err
	}
	snapshot, cleanup, err := snapshot(ctx, path, options.TempRoot)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	db, err := sql.Open("sqlite3", sqliteURI(snapshot))
	if err != nil {
		return Result{}, ErrInvalidDatabase
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		return Result{}, dbError(ctx)
	}
	version, err := metaVersion(ctx, db)
	if err != nil {
		return Result{}, err
	}
	columns, err := columns(ctx, db)
	if err != nil {
		return Result{}, err
	}
	query, err := query(columns)
	if err != nil {
		return Result{}, err
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return Result{}, dbError(ctx)
	}
	defer rows.Close()
	max := options.MaxCookies
	if max <= 0 {
		max = 1_000_000
	}
	decrypt := &decryptor{metaVersion: version, provider: options.PasswordProvider, service: settings.service}
	defer decrypt.Close()
	result := Result{MetaVersion: version}
	keyFailures, decryptFailures := 0, 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var host, name, value, path string
		var encrypted []byte
		var expires int64
		var secure, httpOnly, sameSite int
		if err := rows.Scan(&host, &name, &value, &encrypted, &path, &expires, &secure, &httpOnly, &sameSite); err != nil {
			return result, ErrInvalidDatabase
		}
		result.Total++
		if result.Total > max {
			return result, ErrLimit
		}
		if !validCookie(host, name, value, path) {
			return result, ErrInvalidDatabase
		}
		if len(encrypted) > 16<<20 {
			return result, ErrLimit
		}
		if len(encrypted) > 0 {
			result.Encrypted++
			value, err = decrypt.decrypt(ctx, host, encrypted)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return result, err
				}
				result.Failed++
				if errors.Is(err, ErrKeyUnavailable) {
					keyFailures++
				} else {
					decryptFailures++
				}
				continue
			}
		}
		cookie := &http.Cookie{Name: name, Value: value, Domain: host, Path: path, Secure: secure != 0, HttpOnly: httpOnly != 0, SameSite: sameSiteValue(sameSite)}
		if expires == 0 {
			result.Session++
		} else {
			cookie.Expires = chromiumTime(expires)
			result.Persistent++
		}
		result.Cookies = append(result.Cookies, cookie)
		result.Imported++
	}
	if rows.Err() != nil {
		return result, dbError(ctx)
	}
	var failures []error
	if keyFailures > 0 {
		failures = append(failures, fmt.Errorf("%w: %d encrypted cookies skipped", ErrKeyUnavailable, keyFailures))
	}
	if decryptFailures > 0 {
		failures = append(failures, fmt.Errorf("%w: %d cookies skipped", ErrDecrypt, decryptFailures))
	}
	return result, errors.Join(failures...)
}

type browserSettings struct{ directory, service string }

func browserSettingsFor(browser Browser) (browserSettings, error) {
	if browser == "" {
		browser = Chrome
	}
	switch browser {
	case Chrome:
		return browserSettings{"google-chrome", "Chrome Safe Storage"}, nil
	case Chromium:
		return browserSettings{"chromium", "Chromium Safe Storage"}, nil
	case Brave:
		return browserSettings{filepath.Join("BraveSoftware", "Brave-Browser"), "Brave Safe Storage"}, nil
	default:
		return browserSettings{}, ErrUnsupportedBrowser
	}
}
func locate(options Options, settings browserSettings) (string, error) {
	if options.DatabasePath != "" {
		info, err := os.Lstat(options.DatabasePath)
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		if err != nil || !info.Mode().IsRegular() {
			return "", ErrUnsafePath
		}
		return options.DatabasePath, nil
	}
	root := options.ProfileDir
	if root == "" {
		var err error
		root, err = defaultProfileRoot(settings.directory)
		if err != nil {
			return "", err
		}
		profile := options.Profile
		if profile == "" {
			profile = "Default"
		}
		if profile == "." || profile == ".." || filepath.Base(profile) != profile {
			return "", ErrUnsafePath
		}
		root = filepath.Join(root, profile)
	} else if options.Profile != "" {
		return "", ErrUnsafePath
	}
	for _, path := range []string{filepath.Join(root, "Network", "Cookies"), filepath.Join(root, "Cookies")} {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", ErrNotFound
}

func snapshot(ctx context.Context, source, tempRoot string) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", func() {}, err
	}
	dir, err := os.MkdirTemp(tempRoot, "ytdlp-chromium-linux-")
	if err != nil {
		return "", func() {}, ErrSnapshot
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if os.Chmod(dir, 0o700) != nil {
		cleanup()
		return "", func() {}, ErrSnapshot
	}
	destination := filepath.Join(dir, "Cookies")
	// Copy durable database/WAL state, but never the process-local SHM locks.
	for _, suffix := range []string{"", "-wal"} {
		if err := copyFile(ctx, source+suffix, destination+suffix, suffix != ""); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return destination, cleanup, nil
}
func copyFile(ctx context.Context, source, destination string, optional bool) error {
	info, err := os.Lstat(source)
	if optional && os.IsNotExist(err) {
		return nil
	}
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxSnapshotBytes {
		return ErrUnsafePath
	}
	input, err := os.Open(source)
	if err != nil {
		return ErrSnapshot
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !safeOpenedSnapshot(info, opened) {
		return ErrUnsafePath
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrSnapshot
	}
	defer output.Close()
	buffer := make([]byte, 64<<10)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			copied += int64(n)
			if copied > maxSnapshotBytes {
				return ErrUnsafePath
			}
			if _, err := output.Write(buffer[:n]); err != nil {
				return ErrSnapshot
			}
		}
		if errors.Is(readErr, io.EOF) {
			after, statErr := input.Stat()
			if statErr != nil || !safeOpenedSnapshot(opened, after) {
				return ErrUnsafePath
			}
			return nil
		}
		if readErr != nil {
			return ErrSnapshot
		}
	}
}

const maxSnapshotBytes int64 = 2 << 30

func safeOpenedSnapshot(expected, opened os.FileInfo) bool {
	return expected != nil && opened != nil && expected.Mode().IsRegular() && opened.Mode().IsRegular() &&
		expected.Size() >= 0 && expected.Size() <= maxSnapshotBytes && opened.Size() >= 0 && opened.Size() <= maxSnapshotBytes &&
		os.SameFile(expected, opened) && expected.Size() == opened.Size() && expected.ModTime() == opened.ModTime()
}
func sqliteURI(path string) string {
	u := &url.URL{Scheme: "file", Path: sqliteURLPath(path)}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	return u.String()
}
func sqliteURLPath(path string) string {
	path = filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}
func metaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var raw string
	if err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='version'").Scan(&raw); err != nil {
		return 0, dbError(ctx)
	}
	var version int
	if _, err := fmt.Sscan(raw, &version); err != nil || version < 1 || version > 10000 {
		return 0, ErrInvalidDatabase
	}
	return version, nil
}
func columns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(cookies)")
	if err != nil {
		return nil, dbError(ctx)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var index, notNull, pk int
		var name, typ string
		var defaultValue any
		if rows.Scan(&index, &name, &typ, &notNull, &defaultValue, &pk) != nil {
			return nil, ErrInvalidDatabase
		}
		out[strings.ToLower(name)] = true
	}
	if len(out) == 0 || rows.Err() != nil {
		return nil, ErrInvalidDatabase
	}
	return out, nil
}
func query(columns map[string]bool) (string, error) {
	for _, required := range []string{"host_key", "name", "value", "encrypted_value", "path", "expires_utc"} {
		if !columns[required] {
			return "", ErrInvalidDatabase
		}
	}
	secure := "0"
	if columns["is_secure"] {
		secure = "is_secure"
	} else if columns["secure"] {
		secure = "secure"
	}
	httpOnly := "0"
	if columns["is_httponly"] {
		httpOnly = "is_httponly"
	}
	sameSite := "-1"
	if columns["samesite"] {
		sameSite = "samesite"
	}
	return "SELECT host_key,name,value,encrypted_value,path,expires_utc," + secure + "," + httpOnly + "," + sameSite + " FROM cookies", nil
}
func validCookie(host, name, value, path string) bool {
	return host != "" && len(host) <= 255 && len(name) <= 4096 && len(value) <= 16<<20 && path != "" && len(path) <= 4096 && strings.HasPrefix(path, "/") &&
		!strings.ContainsAny(host, "\r\n\x00") && !strings.ContainsAny(name, "\r\n\x00") && !strings.ContainsAny(value, "\r\n\x00") && !strings.ContainsAny(path, "\r\n\x00")
}
func chromiumTime(microseconds int64) time.Time {
	return time.Unix(microseconds/1_000_000-chromeEpochOffsetSeconds, (microseconds%1_000_000)*1000).UTC()
}
func sameSiteValue(value int) http.SameSite {
	switch value {
	case 0:
		return http.SameSiteNoneMode
	case 1:
		return http.SameSiteLaxMode
	case 2:
		return http.SameSiteStrictMode
	default:
		return http.SameSiteDefaultMode
	}
}
func dbError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrInvalidDatabase
}
