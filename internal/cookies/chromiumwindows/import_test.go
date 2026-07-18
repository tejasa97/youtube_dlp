package chromiumwindows

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

type fakeProtector struct {
	wrapped []byte
	legacy  []byte
	err     error
}

func (f fakeProtector) Unprotect(ctx context.Context, input []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	if string(input) == "wrapped-master-key" {
		return append([]byte(nil), f.wrapped...), nil
	}
	if string(input) == "legacy-blob" {
		return append([]byte(nil), f.legacy...), nil
	}
	return nil, errors.New("unexpected protected input")
}

type fakeAppBound struct{ value []byte }

func (f fakeAppBound) DecryptAppBound(ctx context.Context, input []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if string(input) != "app-payload" {
		return nil, errors.New("bad payload")
	}
	return append([]byte(nil), f.value...), nil
}

type cancelAppBound struct{}

func (cancelAppBound) DecryptAppBound(context.Context, []byte) ([]byte, error) {
	return nil, context.Canceled
}

type cookieRow struct {
	host, name, value, path string
	encrypted               []byte
	expires                 int64
	secure, httpOnly, site  int
}

func createCookieDB(t *testing.T, path string, modern bool, version int, rows []cookieRow, wal bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	if wal {
		if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("CREATE TABLE meta(key LONGVARCHAR NOT NULL UNIQUE PRIMARY KEY, value LONGVARCHAR)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO meta VALUES('version', ?)", fmt.Sprint(version)); err != nil {
		t.Fatal(err)
	}
	if modern {
		_, err = db.Exec("CREATE TABLE cookies(host_key TEXT,name TEXT,value TEXT,encrypted_value BLOB,path TEXT,expires_utc INTEGER,is_secure INTEGER,is_httponly INTEGER,samesite INTEGER)")
	} else {
		_, err = db.Exec("CREATE TABLE cookies(host_key TEXT,name TEXT,value TEXT,encrypted_value BLOB,path TEXT,expires_utc INTEGER,secure INTEGER,httponly INTEGER)")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		query := "INSERT INTO cookies VALUES(?,?,?,?,?,?,?,?,?)"
		args := []any{row.host, row.name, row.value, row.encrypted, row.path, row.expires, row.secure, row.httpOnly, row.site}
		if !modern {
			query, args = "INSERT INTO cookies VALUES(?,?,?,?,?,?,?,?)", args[:8]
		}
		if _, err = db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if wal {
		if err := os.Chmod(path+"-wal", 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !wal {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	return db
}

func writeLocalState(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "Local State")
	wrapped := base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), []byte("wrapped-master-key")...))
	if err := os.WriteFile(path, []byte(`{"os_crypt":{"encrypted_key":"`+wrapped+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encryptCookie(t *testing.T, version, host, value string, meta int) []byte {
	t.Helper()
	plain := []byte(value)
	if meta >= 24 {
		hash := sha256.Sum256([]byte(host))
		plain = append(hash[:], plain...)
	}
	block, err := aes.NewCipher(testKey)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := []byte("fixed-nonce!")
	return append(append([]byte(version), nonce...), aead.Seal(nil, nonce, plain, nil)...)
}

func TestImportAllEncryptionFamiliesAndWAL(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "Cookies")
	state := writeLocalState(t, dir)
	host := ".example.test"
	rows := []cookieRow{
		{host: host, name: "plain", value: "clear", path: "/"},
		{host: host, name: "legacy", encrypted: []byte("legacy-blob"), path: "/"},
		{host: host, name: "ten", encrypted: encryptCookie(t, "v10", host, "aes10", 24), path: "/", secure: 1, site: 1},
		{host: host, name: "eleven", encrypted: encryptCookie(t, "v11", host, "aes11", 24), path: "/", httpOnly: 1, site: 2},
		{host: host, name: "twenty", encrypted: append([]byte("v20"), []byte("app-payload")...), path: "/", expires: 13_000_000_000_000_000},
		{host: host, name: "corrupt", encrypted: []byte("v10short"), path: "/"},
	}
	legacy := append(sha256Bytes(host), []byte("old")...)
	open := createCookieDB(t, dbPath, true, 24, rows, true)
	defer open.Close()
	result, err := Import(context.Background(), Options{DatabasePath: dbPath, LocalStatePath: state, Protector: fakeProtector{wrapped: testKey, legacy: legacy}, AppBound: fakeAppBound{value: append(sha256Bytes(host), []byte("bound")...)}})
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("error = %v", err)
	}
	if result.Total != 6 || result.Imported != 5 || result.Failed != 1 || result.V10 != 2 || result.V11 != 1 || result.V20 != 1 || result.Legacy != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	got := map[string]string{}
	for _, cookie := range result.Cookies {
		got[cookie.Name] = cookie.Value
	}
	for name, want := range map[string]string{"plain": "clear", "legacy": "old", "ten": "aes10", "eleven": "aes11", "twenty": "bound"} {
		if got[name] != want {
			t.Errorf("%s=%q want %q", name, got[name], want)
		}
	}
}

func TestImportLegacySchemaAndLimits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Cookies")
	createCookieDB(t, path, false, 23, []cookieRow{{host: "x.test", name: "a", value: "b", path: "/", secure: 1, httpOnly: 1}}, false)
	result, err := Import(context.Background(), Options{DatabasePath: path, MaxCookies: 1})
	if err != nil || result.Imported != 1 || !result.Cookies[0].Secure || !result.Cookies[0].HttpOnly {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	_, err = Import(context.Background(), Options{DatabasePath: path, MaxCookies: -1})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("limit error=%v", err)
	}
}

func TestHostBindingAndAppBoundFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Cookies")
	state := writeLocalState(t, dir)
	createCookieDB(t, path, true, 24, []cookieRow{{host: "wrong.test", name: "a", encrypted: encryptCookie(t, "v10", "right.test", "secret", 24), path: "/"}, {host: "x.test", name: "b", encrypted: []byte("v20data"), path: "/"}}, false)
	result, err := Import(context.Background(), Options{DatabasePath: path, LocalStatePath: state, Protector: fakeProtector{wrapped: testKey}})
	if result.Failed != 2 || !errors.Is(err, ErrDecrypt) || !errors.Is(err, ErrAppBound) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestInvalidLocalStateIsCategorized(t *testing.T) {
	for _, body := range []string{
		`{"os_crypt":{"encrypted_key":"not-base64"}}`,
		`{"os_crypt":{"encrypted_key":"one","encrypted_key":"two"}}`,
		`{"os_crypt":{"encrypted_key":"one"}} trailing`,
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "Cookies")
		state := filepath.Join(dir, "Local State")
		if err := os.WriteFile(state, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		createCookieDB(t, path, true, 23, []cookieRow{{host: "x.test", name: "a", encrypted: encryptCookie(t, "v10", "x.test", "hidden", 23), path: "/"}}, false)
		result, err := Import(context.Background(), Options{DatabasePath: path, LocalStatePath: state, Protector: fakeProtector{wrapped: testKey}})
		if result.Failed != 1 || !errors.Is(err, ErrInvalidLocalState) {
			t.Fatalf("result=%+v error=%v", result, err)
		}
	}
}

func TestCancellationAndSecretSafeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Import(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "Cookies")
	state := writeLocalState(t, dir)
	createCookieDB(t, path, true, 23, []cookieRow{{host: "x.test", name: "a", encrypted: encryptCookie(t, "v10", "x.test", "hidden", 23), path: "/"}}, false)
	_, err := Import(context.Background(), Options{DatabasePath: path, LocalStatePath: state, Protector: fakeProtector{err: errors.New("SUPER-SECRET")}})
	if !errors.Is(err, ErrKeyUnavailable) || strings.Contains(fmt.Sprint(err), "SUPER-SECRET") {
		t.Fatalf("unsafe error=%v", err)
	}
	_, err = Import(context.Background(), Options{DatabasePath: path, LocalStatePath: state, Protector: fakeProtector{err: context.Canceled}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("protector cancellation=%v", err)
	}
	appPath := filepath.Join(dir, "AppCookies")
	createCookieDB(t, appPath, true, 23, []cookieRow{{host: "x.test", name: "a", encrypted: []byte("v20payload"), path: "/"}}, false)
	_, err = Import(context.Background(), Options{DatabasePath: appPath, AppBound: cancelAppBound{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("backend cancellation=%v", err)
	}
}

func TestDiscoveryAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "Default")
	if err := os.MkdirAll(filepath.Join(profile, "Network"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(profile, "Cookies")
	newer := filepath.Join(profile, "Network", "Cookies")
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, oldTime.Add(time.Minute), oldTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	db, state, err := locateInputs(Options{Browser: Chrome, ProfileRoot: root})
	if err != nil || db != newer || state != filepath.Join(root, "Local State") {
		t.Fatalf("db=%s state=%s err=%v", db, state, err)
	}
	for _, profileName := range []string{"../escape", ".", "a/b"} {
		if _, _, err := locateInputs(Options{Browser: Chrome, ProfileRoot: root, Profile: profileName}); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("profile %q error=%v", profileName, err)
		}
	}
	if _, _, err := locateInputs(Options{Browser: "other", ProfileRoot: root}); !errors.Is(err, ErrUnsupportedBrowser) {
		t.Fatalf("error=%v", err)
	}
}

func TestConformanceCorpus(t *testing.T) {
	var corpus struct {
		ReferenceCommit string `json:"reference_commit"`
		Vectors         []struct {
			Meta            int    `json:"meta_version"`
			Host            string `json:"host"`
			MasterKeyBase64 string `json:"master_key_base64"`
			EncryptedBase64 string `json:"encrypted_base64"`
			Expected        string `json:"expected"`
		} `json:"vectors"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "cookies", "chromium-windows", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.ReferenceCommit != "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8" || len(corpus.Vectors) == 0 {
		t.Fatalf("bad provenance: %+v", corpus)
	}
	for _, vector := range corpus.Vectors {
		key, err := base64.StdEncoding.DecodeString(vector.MasterKeyBase64)
		if err != nil {
			t.Fatal(err)
		}
		blob, err := base64.StdEncoding.DecodeString(vector.EncryptedBase64)
		if err != nil {
			t.Fatal(err)
		}
		decryptor := cookieDecryptor{masterKey: key, masterLoaded: true}
		got, family, err := decryptor.decrypt(context.Background(), blob, vector.Host, vector.Meta)
		decryptor.close()
		if err != nil || family != "v10" || got != vector.Expected {
			t.Fatalf("got=%q family=%s err=%v", got, family, err)
		}
	}
}

func sha256Bytes(value string) []byte { hash := sha256.Sum256([]byte(value)); return hash[:] }
