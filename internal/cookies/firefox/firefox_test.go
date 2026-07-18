package firefox

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeDatabase(t *testing.T, schema int, wal bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookies.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if wal {
		if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER,isHttpOnly INTEGER,sameSite INTEGER,originAttributes TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version = " + fmtInt(schema)); err != nil {
		t.Fatal(err)
	}
	expiry := int64(1_893_456_000)
	if schema >= 16 {
		expiry *= 1000
	}
	if _, err = db.Exec("INSERT INTO moz_cookies VALUES(?,?,?,?,?,?,?,?,?)", ".example.com", "sid", "secret", "/", expiry, 1, 1, 1, "^userContextId=7"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO moz_cookies VALUES(?,?,?,?,?,?,?,?,?)", "plain.example", "session", "value", "/", 0, 0, 0, 0, ""); err != nil {
		t.Fatal(err)
	}
	if wal {
		if _, err = db.Exec("PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func fmtInt(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var out [20]byte
	index := len(out)
	for value > 0 {
		index--
		out[index] = digits[value%10]
		value /= 10
	}
	return string(out[index:])
}

func TestImportSchema16ExpiryFlagsAndContainer(t *testing.T) {
	path := makeDatabase(t, 16, false)
	containers := `{"identities":[{"name":"Work","userContextId":7,"l10nID":"userContext7.label"}]}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "containers.json"), []byte(containers), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Import(context.Background(), Options{DatabasePath: path, Container: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.ContainerID == nil || *result.ContainerID != 7 {
		t.Fatalf("%+v", result)
	}
	cookie := result.Cookies[0]
	if cookie.Expires.Unix() != 1_893_456_000 || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != 2 {
		t.Fatalf("%+v", cookie)
	}
}

func TestImportOlderSchemaVariant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER); INSERT INTO moz_cookies VALUES('example.com','name','value','/',0,0); PRAGMA user_version=15")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	result, err := Import(context.Background(), Options{DatabasePath: path})
	if err != nil || result.Imported != 1 || result.Cookies[0].HttpOnly {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestImportCopiesDatabaseAndWAL(t *testing.T) {
	result, err := Import(context.Background(), Options{DatabasePath: makeDatabase(t, 17, true)})
	if err != nil || result.Imported != 2 {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestImportCopiesLockedLiveWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,path TEXT,expiry INTEGER,isSecure INTEGER)",
		"PRAGMA user_version=15",
		"INSERT INTO moz_cookies VALUES('example.com','live','value','/',0,0)",
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Import(context.Background(), Options{DatabasePath: path})
	if err != nil || result.Imported != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLocateNewestSyntheticProfile(t *testing.T) {
	root := t.TempDir()
	older := filepath.Join(root, "old", "cookies.sqlite")
	newer := filepath.Join(root, "new", "cookies.sqlite")
	for _, path := range []string{older, newer} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, oldTime.Add(time.Hour), oldTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	selected, err := locate(Options{ProfileDir: root})
	if err != nil || selected != newer {
		t.Fatalf("selected=%q err=%v", selected, err)
	}
}

func TestContainerMissingIsCategorized(t *testing.T) {
	_, err := Import(context.Background(), Options{DatabasePath: makeDatabase(t, 17, false), Container: "missing"})
	if !errors.Is(err, ErrContainerMissing) {
		t.Fatalf("got %v", err)
	}
}

func TestImportCancellationAndUnsafePath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Import(ctx, Options{DatabasePath: makeDatabase(t, 17, false)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	dir := t.TempDir()
	_, err = Import(context.Background(), Options{DatabasePath: dir})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("got %v", err)
	}
}

func TestErrorsRedactCookieSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies.sqlite")
	if err := os.WriteFile(path, []byte("secret-cookie-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Import(context.Background(), Options{DatabasePath: path})
	if !errors.Is(err, ErrInvalidDatabase) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret-cookie-value") {
		t.Fatal("secret leaked")
	}
}

func FuzzValidCookie(f *testing.F) {
	f.Add("example.com", "name", "value", "/")
	f.Fuzz(func(t *testing.T, h, n, v, p string) {
		if len(h)+len(n)+len(v)+len(p) > 1<<20 {
			t.Skip()
		}
		_ = validCookie(h, n, v, p)
	})
}
