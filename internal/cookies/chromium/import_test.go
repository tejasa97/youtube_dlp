package chromium

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const meta24Ciphertext = "djEwqXxQ4y30kpveilVsRpOEWHeXf0ktvFV7qM/zzpUUkM+o5GVwq/qK5pFiBXHYMk4b"

func TestSnapshotURIUsesCanonicalNativeFilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile with space", "Cookies")
	uri := snapshotURI(path)
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "file" || parsed.Host != "" || parsed.Path != sqliteURLPath(path) {
		t.Fatalf("snapshotURI(%q) = %q (host=%q path=%q)", path, uri, parsed.Host, parsed.Path)
	}
	if parsed.Query().Get("mode") != "rw" || len(parsed.Query()["_pragma"]) != 2 {
		t.Fatalf("snapshot URI query = %v", parsed.Query())
	}
}

func TestImportCopiesLiveWALAndReturnsPartialResults(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "Cookies")
	database := createCookieDatabase(t, databasePath, true)
	defer database.Close()

	encrypted, err := base64.StdEncoding.DecodeString(meta24Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2030, 1, 2, 3, 4, 5, 123456000, time.UTC)
	insertCookie(t, database, ".example.com", "encrypted", "", encrypted, "/", chromiumMicros(expires), 1, 1, 2)
	insertCookie(t, database, "example.com", "plain", "visible", nil, "/plain", 0, 0, 0, 1)
	insertCookie(t, database, ".example.com", "legacy", "", []byte("legacy-value"), "/", 0, 0, 0, -1)
	insertCookie(t, database, ".example.com", "secret-cookie-name", "", []byte("v10broken"), "/", 0, 0, 0, -1)
	if _, err := os.Stat(databasePath + "-wal"); err != nil {
		t.Fatalf("live WAL was not created: %v", err)
	}

	tempRoot := filepath.Join(root, "snapshots")
	if err := os.Mkdir(tempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := &staticKeyProvider{password: []byte("abc")}
	result, importErr := Import(context.Background(), Options{
		Browser: Chrome, DatabasePath: databasePath, KeyProvider: provider, TempRoot: tempRoot,
	})
	if !errors.Is(importErr, ErrDecrypt) {
		t.Fatalf("Import() error = %v", importErr)
	}
	if strings.Contains(importErr.Error(), "secret-cookie-name") || strings.Contains(importErr.Error(), databasePath) {
		t.Fatalf("error leaked private data: %v", importErr)
	}
	if result.MetaVersion != 24 || result.Total != 4 || result.Imported != 3 || result.Encrypted != 3 || result.Failed != 1 {
		t.Fatalf("result counts = %#v", result)
	}
	if result.Session != 2 || result.Persistent != 1 {
		t.Fatalf("lifetime counts = %#v", result)
	}
	cookies := cookiesByName(result.Cookies)
	got := cookies["encrypted"]
	if got == nil || got.Value != "meta-cookie" || !got.Expires.Equal(expires) || !got.Secure || !got.HttpOnly || got.SameSite != http.SameSiteStrictMode {
		t.Fatalf("encrypted cookie = %#v", got)
	}
	if got := cookies["plain"]; got == nil || got.Value != "visible" || got.Path != "/plain" || got.SameSite != http.SameSiteLaxMode {
		t.Fatalf("plain cookie = %#v", got)
	}
	if got := cookies["legacy"]; got == nil || got.Value != "legacy-value" {
		t.Fatalf("legacy cookie = %#v", got)
	}
	if provider.item != (KeychainItem{Account: "Chrome", Service: "Chrome Safe Storage"}) {
		t.Fatalf("keychain item = %#v", provider.item)
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("snapshot cleanup: entries=%v err=%v", entries, err)
	}
}

func TestImportSupportsOlderSecureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	execStatements(t, database,
		"CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT)",
		"INSERT INTO meta(key, value) VALUES('version', '23')",
		"CREATE TABLE cookies(host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, path TEXT, expires_utc INTEGER, secure INTEGER)",
		"INSERT INTO cookies VALUES('example.com', 'old', 'value', X'', '/', 0, 1)",
	)
	result, err := Import(context.Background(), Options{DatabasePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Cookies) != 1 || !result.Cookies[0].Secure || result.Cookies[0].SameSite != http.SameSiteDefaultMode {
		t.Fatalf("cookies = %#v", result.Cookies)
	}
}

func TestImportReportsUnavailableKeyWithoutDiscardingPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cookies")
	database := createCookieDatabase(t, path, false)
	insertCookie(t, database, ".example.com", "encrypted", "", []byte("v10broken"), "/", 0, 1, 1, 0)
	insertCookie(t, database, "example.com", "plain", "kept", nil, "/", 0, 0, 0, -1)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := Import(context.Background(), Options{
		DatabasePath: path,
		KeyProvider:  &staticKeyProvider{err: errors.New("private backend message")},
	})
	if !errors.Is(err, ErrKeyUnavailable) || strings.Contains(err.Error(), "private backend message") {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Imported != 1 || result.Failed != 1 || result.Cookies[0].Value != "kept" {
		t.Fatalf("result = %#v", result)
	}
}

func TestImportRejectsUnsafePathsAndCancellation(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Import(cancelled, Options{DatabasePath: filepath.Join(t.TempDir(), "missing")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Import() error = %v", err)
	}
	if _, err := Import(context.Background(), Options{DatabasePath: filepath.Join(t.TempDir(), "missing")}); !errors.Is(err, ErrDatabaseNotFound) {
		t.Fatalf("missing Import() error = %v", err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Import(context.Background(), Options{DatabasePath: link}); !errors.Is(err, ErrUnsafeDatabase) {
		t.Fatalf("symlink Import() error = %v", err)
	}
	privateRoot := filepath.Join(root, "private-profile-name")
	if err := os.Mkdir(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(privateRoot, "Cookies")
	if err := os.WriteFile(database, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidTempRoot := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(invalidTempRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), Options{DatabasePath: database, TempRoot: invalidTempRoot}); !errors.Is(err, ErrSnapshot) || strings.Contains(err.Error(), privateRoot) {
		t.Fatalf("snapshot error = %v", err)
	}
}

func TestMacOSKeychainPlatformGate(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("would access the user's keychain")
	}
	if _, err := (MacOSKeychain{}).Password(context.Background(), KeychainItem{}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Password() error = %v", err)
	}
}

func createCookieDatabase(t *testing.T, path string, wal bool) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if wal {
		var mode string
		if err := database.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil || strings.ToLower(mode) != "wal" {
			database.Close()
			t.Fatalf("enable WAL: mode=%q err=%v", mode, err)
		}
		execStatements(t, database, "PRAGMA wal_autocheckpoint=0")
	}
	execStatements(t, database,
		"CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT)",
		"INSERT INTO meta(key, value) VALUES('version', '24')",
		"CREATE TABLE cookies(host_key TEXT, name TEXT, value TEXT, encrypted_value BLOB, path TEXT, expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, samesite INTEGER)",
	)
	return database
}

func insertCookie(t *testing.T, database *sql.DB, host, name, value string, encrypted []byte, path string, expires int64, secure, httpOnly, sameSite int) {
	t.Helper()
	if _, err := database.Exec("INSERT INTO cookies VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)", host, name, value, encrypted, path, expires, secure, httpOnly, sameSite); err != nil {
		t.Fatal(err)
	}
}

func execStatements(t *testing.T, database *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func chromiumMicros(value time.Time) int64 {
	return (value.Unix()+chromeEpochOffsetSeconds)*1_000_000 + int64(value.Nanosecond()/1_000)
}

func cookiesByName(cookies []*http.Cookie) map[string]*http.Cookie {
	result := make(map[string]*http.Cookie, len(cookies))
	for _, cookie := range cookies {
		result[cookie.Name] = cookie
	}
	return result
}
