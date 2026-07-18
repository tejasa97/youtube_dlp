package chromiumwindows

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
	"sort"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const (
	chromeEpochOffsetSeconds = int64(11_644_473_600)
	defaultMaxDatabaseBytes  = int64(2 << 30)
	defaultMaxStateBytes     = int64(4 << 20)
	defaultMaxCookies        = 1_000_000
)

func Import(ctx context.Context, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	maximumDatabase, maximumState, maximumCookies, err := importLimits(options)
	if err != nil {
		return Result{}, err
	}
	databasePath, localStatePath, err := locateInputs(options)
	if err != nil {
		return Result{}, err
	}
	snapshot, cleanup, err := snapshotDatabase(ctx, databasePath, options.TempRoot, maximumDatabase)
	if err != nil {
		return Result{}, err
	}
	defer cleanup()
	database, err := sql.Open("sqlite3", sqliteURI(snapshot))
	if err != nil {
		return Result{}, ErrInvalidDatabase
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return Result{}, databaseError(ctx)
	}
	metaVersion, err := readMetaVersion(ctx, database)
	if err != nil {
		return Result{}, err
	}
	columns, err := readCookieColumns(ctx, database)
	if err != nil {
		return Result{}, err
	}
	query, err := buildCookieQuery(columns)
	if err != nil {
		return Result{}, err
	}
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return Result{}, databaseError(ctx)
	}
	defer rows.Close()
	protector := options.Protector
	if protector == nil {
		protector = defaultProtector()
	}
	decryptor := cookieDecryptor{
		protector: protector, appBound: options.AppBound, localStatePath: localStatePath, maxStateBytes: maximumState,
	}
	defer decryptor.close()
	result := Result{MetaVersion: metaVersion}
	keyFailures, stateFailures, unsafeFailures, limitFailures, appBoundFailures, decryptFailures := 0, 0, 0, 0, 0, 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var host, name, plainValue, cookiePath string
		var encrypted []byte
		var expiresUTC int64
		var secure, httpOnly, sameSite int
		if err := rows.Scan(&host, &name, &plainValue, &encrypted, &cookiePath, &expiresUTC, &secure, &httpOnly, &sameSite); err != nil {
			return result, ErrInvalidDatabase
		}
		result.Total++
		if result.Total > maximumCookies {
			return result, ErrLimit
		}
		if len(encrypted) > maximumEncryptedCookieBytes || !validCookie(host, name, plainValue, cookiePath) {
			return result, ErrInvalidDatabase
		}
		cookieValue := plainValue
		if cookieValue == "" && len(encrypted) != 0 {
			result.Encrypted++
			var version string
			cookieValue, version, err = decryptor.decrypt(ctx, encrypted, host, metaVersion)
			switch version {
			case "v10":
				result.V10++
			case "v11":
				result.V11++
			case "v20":
				result.V20++
			default:
				result.Legacy++
			}
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return result, err
				}
				result.Failed++
				switch {
				case errors.Is(err, ErrKeyUnavailable):
					keyFailures++
				case errors.Is(err, ErrInvalidLocalState):
					stateFailures++
				case errors.Is(err, ErrUnsafePath):
					unsafeFailures++
				case errors.Is(err, ErrLimit):
					limitFailures++
				case errors.Is(err, ErrAppBound):
					appBoundFailures++
				default:
					decryptFailures++
				}
				continue
			}
		}
		if !validCookie(host, name, cookieValue, cookiePath) {
			result.Failed++
			decryptFailures++
			continue
		}
		cookie := &http.Cookie{
			Name: name, Value: cookieValue, Domain: host, Path: cookiePath,
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
	if rows.Err() != nil {
		return result, databaseError(ctx)
	}
	var failures []error
	if keyFailures != 0 {
		failures = append(failures, fmt.Errorf("%w: %d cookies skipped", ErrKeyUnavailable, keyFailures))
	}
	if stateFailures != 0 {
		failures = append(failures, fmt.Errorf("%w: %d cookies skipped", ErrInvalidLocalState, stateFailures))
	}
	if unsafeFailures != 0 {
		failures = append(failures, fmt.Errorf("%w: %d cookies skipped", ErrUnsafePath, unsafeFailures))
	}
	if limitFailures != 0 {
		failures = append(failures, fmt.Errorf("%w: %d cookies skipped", ErrLimit, limitFailures))
	}
	if appBoundFailures != 0 {
		failures = append(failures, fmt.Errorf("%w: %d cookies skipped", ErrAppBound, appBoundFailures))
	}
	if decryptFailures != 0 {
		failures = append(failures, fmt.Errorf("%w: %d cookies skipped", ErrDecrypt, decryptFailures))
	}
	return result, errors.Join(failures...)
}

func importLimits(options Options) (int64, int64, int, error) {
	database := options.MaxDatabaseBytes
	if database == 0 {
		database = defaultMaxDatabaseBytes
	}
	state := options.MaxLocalStateBytes
	if state == 0 {
		state = defaultMaxStateBytes
	}
	cookies := options.MaxCookies
	if cookies == 0 {
		cookies = defaultMaxCookies
	}
	if database < 1 || database > 8<<30 || state < 1 || state > 16<<20 || cookies < 1 || cookies > 10_000_000 {
		return 0, 0, 0, ErrLimit
	}
	return database, state, cookies, nil
}

type browserSettings struct {
	localDirectory   string
	roamingDirectory string
	noProfiles       bool
}

func settingsFor(browser Browser) (browserSettings, error) {
	if browser == "" {
		browser = Chrome
	}
	switch browser {
	case Chrome:
		return browserSettings{localDirectory: filepath.Join("Google", "Chrome", "User Data")}, nil
	case Chromium:
		return browserSettings{localDirectory: filepath.Join("Chromium", "User Data")}, nil
	case Edge:
		return browserSettings{localDirectory: filepath.Join("Microsoft", "Edge", "User Data")}, nil
	case Brave:
		return browserSettings{localDirectory: filepath.Join("BraveSoftware", "Brave-Browser", "User Data")}, nil
	case Vivaldi:
		return browserSettings{localDirectory: filepath.Join("Vivaldi", "User Data")}, nil
	case Opera:
		return browserSettings{roamingDirectory: filepath.Join("Opera Software", "Opera Stable"), noProfiles: true}, nil
	default:
		return browserSettings{}, ErrUnsupportedBrowser
	}
}

func locateInputs(options Options) (string, string, error) {
	settings, err := settingsFor(options.Browser)
	if err != nil {
		return "", "", err
	}
	if options.DatabasePath != "" {
		if options.Profile != "" {
			return "", "", ErrUnsafePath
		}
		state := options.LocalStatePath
		if state == "" && options.ProfileRoot != "" {
			state = filepath.Join(options.ProfileRoot, "Local State")
		}
		return options.DatabasePath, state, nil
	}
	root := options.ProfileRoot
	if root == "" {
		if runtime.GOOS != "windows" {
			return "", "", ErrUnsupportedPlatform
		}
		base := os.Getenv("LOCALAPPDATA")
		directory := settings.localDirectory
		if settings.roamingDirectory != "" {
			base = os.Getenv("APPDATA")
			directory = settings.roamingDirectory
		}
		if base == "" {
			return "", "", ErrNotFound
		}
		root = filepath.Join(base, directory)
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", ErrUnsafePath
	}
	profileRoot := root
	if !settings.noProfiles {
		profile := options.Profile
		if profile == "" {
			profile = "Default"
		}
		if !safeProfileName(profile) {
			return "", "", ErrUnsafePath
		}
		profileRoot = filepath.Join(root, profile)
	} else if options.Profile != "" {
		return "", "", ErrUnsupportedBrowser
	}
	candidates := []string{filepath.Join(profileRoot, "Network", "Cookies"), filepath.Join(profileRoot, "Cookies")}
	type candidate struct {
		path     string
		modified time.Time
	}
	available := make([]candidate, 0, 2)
	for _, path := range candidates {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			available = append(available, candidate{path: path, modified: info.ModTime()})
		}
	}
	if len(available) == 0 {
		return "", "", ErrNotFound
	}
	sort.Slice(available, func(i, j int) bool {
		if available[i].modified.Equal(available[j].modified) {
			return available[i].path < available[j].path
		}
		return available[i].modified.After(available[j].modified)
	})
	state := options.LocalStatePath
	if state == "" {
		state = filepath.Join(root, "Local State")
	}
	return available[0].path, state, nil
}

func safeProfileName(profile string) bool {
	return profile != "" && len(profile) <= 255 && profile != "." && profile != ".." && filepath.Base(profile) == profile && !strings.ContainsAny(profile, "\x00\r\n")
}

func sqliteURI(path string) string {
	target := &url.URL{Scheme: "file", Path: sqliteURLPath(path)}
	query := target.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(1000)")
	target.RawQuery = query.Encode()
	return target.String()
}

func sqliteURLPath(path string) string {
	path = filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func readMetaVersion(ctx context.Context, database *sql.DB) (int, error) {
	var raw string
	if err := database.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='version'").Scan(&raw); err != nil {
		return 0, databaseError(ctx)
	}
	var version int
	if _, err := fmt.Sscan(raw, &version); err != nil || version < 1 || version > 10_000 {
		return 0, ErrInvalidDatabase
	}
	return version, nil
}

func readCookieColumns(ctx context.Context, database *sql.DB) (map[string]bool, error) {
	rows, err := database.QueryContext(ctx, "PRAGMA table_info(cookies)")
	if err != nil {
		return nil, databaseError(ctx)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var index, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if rows.Scan(&index, &name, &dataType, &notNull, &defaultValue, &primaryKey) != nil {
			return nil, ErrInvalidDatabase
		}
		columns[strings.ToLower(name)] = true
	}
	if len(columns) == 0 || rows.Err() != nil {
		return nil, ErrInvalidDatabase
	}
	return columns, nil
}

func buildCookieQuery(columns map[string]bool) (string, error) {
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
	} else if columns["httponly"] {
		httpOnly = "httponly"
	}
	sameSite := "-1"
	if columns["samesite"] {
		sameSite = "samesite"
	}
	return "SELECT host_key,name,value,encrypted_value,path,expires_utc," + secure + "," + httpOnly + "," + sameSite + " FROM cookies", nil
}

func validCookie(host, name, cookieValue, cookiePath string) bool {
	return host != "" && len(host) <= 255 && len(name) <= 4096 && len(cookieValue) <= maximumEncryptedCookieBytes &&
		cookiePath != "" && len(cookiePath) <= 4096 && strings.HasPrefix(cookiePath, "/") &&
		!strings.ContainsAny(host, "\r\n\x00") && !strings.ContainsAny(name, "\r\n\x00") &&
		!strings.ContainsAny(cookieValue, "\r\n\x00") && !strings.ContainsAny(cookiePath, "\r\n\x00")
}

func chromiumTime(microseconds int64) time.Time {
	return time.Unix(microseconds/1_000_000-chromeEpochOffsetSeconds, (microseconds%1_000_000)*1000).UTC()
}

func chromiumSameSite(input int) http.SameSite {
	switch input {
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

func databaseError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrInvalidDatabase
}
