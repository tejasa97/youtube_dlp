package chromium

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const chromeEpochOffsetSeconds int64 = 11_644_473_600

type browserSettings struct {
	keychain  KeychainItem
	directory string
}

func Import(ctx context.Context, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	settings, err := settingsFor(options.Browser)
	if err != nil {
		return Result{}, err
	}
	databasePath, err := locateDatabase(options, settings)
	if err != nil {
		return Result{}, err
	}
	snapshot, cleanup, err := copyDatabaseSnapshot(ctx, databasePath, options.TempRoot)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()

	database, err := sql.Open("sqlite3", snapshotURI(snapshot))
	if err != nil {
		return Result{}, ErrInvalidDatabase
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return Result{}, ErrInvalidDatabase
	}

	metaVersion, err := readMetaVersion(ctx, database)
	if err != nil {
		return Result{}, err
	}
	columns, err := readColumns(ctx, database)
	if err != nil {
		return Result{}, err
	}
	query, err := cookieQuery(columns)
	if err != nil {
		return Result{}, err
	}
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return Result{}, ErrInvalidDatabase
	}
	defer rows.Close()

	provider := options.KeyProvider
	if provider == nil {
		provider = MacOSKeychain{}
	}
	decryptor := &macDecryptor{provider: provider, item: settings.keychain}
	defer decryptor.Close()
	result := Result{MetaVersion: metaVersion}
	keyFailures, decryptFailures := 0, 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var host, name, plainValue, path string
		var encryptedValue []byte
		var expiresUTC int64
		var secure, httpOnly, sameSite int
		if err := rows.Scan(&host, &name, &plainValue, &encryptedValue, &path, &expiresUTC, &secure, &httpOnly, &sameSite); err != nil {
			return result, ErrInvalidDatabase
		}
		result.Total++
		value := plainValue
		if len(encryptedValue) > 0 {
			result.Encrypted++
			value, err = decryptor.Decrypt(ctx, host, encryptedValue, metaVersion)
			if err != nil {
				result.Failed++
				if errors.Is(err, ErrKeyUnavailable) {
					keyFailures++
				} else {
					decryptFailures++
				}
				continue
			}
		}
		cookie := &http.Cookie{
			Name: name, Value: value, Domain: host, Path: path,
			Secure: secure != 0, HttpOnly: httpOnly != 0, SameSite: chromiumSameSite(sameSite),
		}
		if expiresUTC == 0 {
			result.Session++
		} else {
			cookie.Expires = chromiumTime(expiresUTC)
			result.Persistent++
		}
		result.Cookies = append(result.Cookies, cookie)
		result.Imported++
	}
	if err := rows.Err(); err != nil {
		return result, ErrInvalidDatabase
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

func settingsFor(browser Browser) (browserSettings, error) {
	if browser == "" {
		browser = Chrome
	}
	if browser != Chrome {
		return browserSettings{}, ErrUnsupportedBrowser
	}
	return browserSettings{
		keychain:  KeychainItem{Account: "Chrome", Service: "Chrome Safe Storage"},
		directory: filepath.Join("Google", "Chrome"),
	}, nil
}

func locateDatabase(options Options, settings browserSettings) (string, error) {
	if options.DatabasePath != "" {
		return options.DatabasePath, nil
	}
	profileDirectory := options.ProfileDir
	if profileDirectory == "" {
		if runtime.GOOS != "darwin" {
			return "", ErrUnsupportedPlatform
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ErrDatabaseNotFound
		}
		profile := options.Profile
		if profile == "" {
			profile = "Default"
		}
		if profile == "." || profile == ".." || filepath.Base(profile) != profile {
			return "", ErrUnsafeDatabase
		}
		profileDirectory = filepath.Join(home, "Library", "Application Support", settings.directory, profile)
	}
	candidates := []string{
		filepath.Join(profileDirectory, "Network", "Cookies"),
		filepath.Join(profileDirectory, "Cookies"),
	}
	var selected string
	var selectedTime time.Time
	for _, candidate := range candidates {
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if selected == "" || info.ModTime().After(selectedTime) {
			selected, selectedTime = candidate, info.ModTime()
		}
	}
	if selected == "" {
		return "", ErrDatabaseNotFound
	}
	return selected, nil
}

func snapshotURI(path string) string {
	target := &url.URL{Scheme: "file", Path: sqliteURLPath(path)}
	query := target.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(1000)")
	target.RawQuery = query.Encode()
	return target.String()
}

// sqliteURLPath converts the native filename to the slash-separated path
// component required by a file URI. In particular, a Windows drive path must
// be encoded as file:///C:/path, not file://C:%5Cpath (which treats the drive
// as a URL host and makes SQLite open the wrong file).
func sqliteURLPath(path string) string {
	path = filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func readMetaVersion(ctx context.Context, database *sql.DB) (int, error) {
	var raw string
	if err := database.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'version'").Scan(&raw); err != nil {
		return 0, ErrInvalidDatabase
	}
	var version int
	if _, err := fmt.Sscan(raw, &version); err != nil || version < 1 || version > 10_000 {
		return 0, ErrInvalidDatabase
	}
	return version, nil
}

func readColumns(ctx context.Context, database *sql.DB) (map[string]bool, error) {
	rows, err := database.QueryContext(ctx, "PRAGMA table_info(cookies)")
	if err != nil {
		return nil, ErrInvalidDatabase
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&index, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, ErrInvalidDatabase
		}
		columns[strings.ToLower(name)] = true
	}
	if len(columns) == 0 || rows.Err() != nil {
		return nil, ErrInvalidDatabase
	}
	return columns, nil
}

func cookieQuery(columns map[string]bool) (string, error) {
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
	return "SELECT host_key, name, value, encrypted_value, path, expires_utc, " + secure + ", " + httpOnly + ", " + sameSite + " FROM cookies", nil
}

func chromiumTime(microseconds int64) time.Time {
	seconds := microseconds/1_000_000 - chromeEpochOffsetSeconds
	nanoseconds := (microseconds % 1_000_000) * 1_000
	return time.Unix(seconds, nanoseconds).UTC()
}

func chromiumSameSite(value int) http.SameSite {
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
