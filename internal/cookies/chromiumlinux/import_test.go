package chromiumlinux

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteURIUsesCanonicalNativeFilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile with space", "Cookies")
	uri := sqliteURI(path)
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "file" || parsed.Host != "" || parsed.Path != sqliteURLPath(path) {
		t.Fatalf("sqliteURI(%q) = %q (host=%q path=%q)", path, uri, parsed.Host, parsed.Path)
	}
	if parsed.Query().Get("mode") != "rw" || len(parsed.Query()["_pragma"]) != 2 {
		t.Fatalf("SQLite URI query = %v", parsed.Query())
	}
}

type provider struct {
	password []byte
	err      error
	called   int
}

type borrowedProvider struct{ password []byte }

func (p *borrowedProvider) Password(context.Context, string) ([]byte, error) { return p.password, nil }

type cancelProvider struct{ cancel context.CancelFunc }

func (p cancelProvider) Password(ctx context.Context, _ string) ([]byte, error) {
	p.cancel()
	return nil, ctx.Err()
}

func (p *provider) Password(ctx context.Context, service string) ([]byte, error) {
	p.called++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]byte(nil), p.password...), p.err
}

func encrypt(t *testing.T, version string, password, plaintext []byte, host string, meta int) []byte {
	t.Helper()
	if meta >= 24 {
		digest := sha256.Sum256([]byte(host))
		plaintext = append(digest[:], plaintext...)
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	plaintext = append(plaintext, bytesRepeat(byte(padding), padding)...)
	block, err := aes.NewCipher(DeriveKey(password))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 3+len(plaintext))
	copy(out, version)
	cipher.NewCBCEncrypter(block, linuxIV).CryptBlocks(out[3:], plaintext)
	return out
}
func bytesRepeat(value byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = value
	}
	return out
}

func database(t *testing.T, meta int, rows [][]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Cookies")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE meta(key TEXT,value TEXT)",
		"CREATE TABLE cookies(host_key TEXT,name TEXT,value TEXT,encrypted_value BLOB,path TEXT,expires_utc INTEGER,is_secure INTEGER,is_httponly INTEGER,samesite INTEGER)",
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO meta VALUES('version',?)", meta); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err = db.Exec("INSERT INTO cookies VALUES(?,?,?,?,?,?,?,?,?)", row...); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPinnedLinuxV10Corpus(t *testing.T) {
	d := decryptor{}
	value, err := d.decrypt(context.Background(), "example.com", []byte{'v', '1', '0', 0xcc, 0x57, 0x25, 0xcd, 0xe6, 0xe6, 0x9f, 0x4d, 0x22, 0x20, 0xa7, 0xb0, 0xca, 0xe4, 0x07, 0xd6})
	if err != nil || value != "USD" {
		t.Fatalf("%q %v", value, err)
	}
}
func TestPinnedLinuxV11Corpus(t *testing.T) {
	p := &provider{password: []byte{}}
	d := decryptor{provider: p}
	value, err := d.decrypt(context.Background(), "example.com", []byte{'v', '1', '1', 0x23, 0x81, 0x10, 0x3e, 0x60, 0x77, 0x8f, 0x29, 0xc0, 0xb2, 0xc1, 0x0d, 0xf4, 0x1a, 0x6c, 0xdd, 0x93, 0xfd, 0xf8, 0xf8, 0x4e, 0xf2, 0xa9, 0x83, 0xf1, 0xe9, 0x6f, 0x0e, 0x6c, 0x56, 0x51, 0x64})
	if err != nil || value != "tz=Europe.London" {
		t.Fatalf("%q %v", value, err)
	}
}

func TestImportPlainV10V11AndSchema(t *testing.T) {
	host := ".example.com"
	p := &provider{password: []byte("keyring")}
	rows := [][]any{{host, "plain", "value", []byte{}, "/", int64(0), 0, 0, -1}, {host, "v10", "", encrypt(t, "v10", []byte("peanuts"), []byte("ten"), host, 24), "/", int64(13_000_000_000_000_000), 1, 1, 1}, {host, "v11", "", encrypt(t, "v11", []byte("keyring"), []byte("eleven"), host, 24), "/", int64(0), 0, 0, 0}}
	result, err := Import(context.Background(), Options{DatabasePath: database(t, 24, rows), PasswordProvider: p})
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 3 || result.Encrypted != 2 || p.called != 1 || !result.Cookies[1].Secure || !result.Cookies[1].HttpOnly {
		t.Fatalf("%+v provider=%d", result, p.called)
	}
}

func TestImportCredentialFailureIsPartialAndRedacted(t *testing.T) {
	secret := "credential-secret"
	host := "example.com"
	p := &provider{err: errors.New(secret)}
	rows := [][]any{{host, "plain", "ok", []byte{}, "/", int64(0), 0, 0, -1}, {host, "protected", "", encrypt(t, "v11", []byte("keyring"), []byte("hidden"), host, 23), "/", int64(0), 0, 0, -1}}
	result, err := Import(context.Background(), Options{DatabasePath: database(t, 23, rows), PasswordProvider: p})
	if !errors.Is(err, ErrKeyUnavailable) || result.Imported != 1 || result.Failed != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "hidden") {
		t.Fatal("secret leaked")
	}
}

func TestImportCancellationUnsafeAndMalformed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Import(ctx, Options{DatabasePath: database(t, 23, nil)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	_, err = Import(context.Background(), Options{DatabasePath: t.TempDir()})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("got %v", err)
	}
	path := filepath.Join(t.TempDir(), "Cookies")
	os.WriteFile(path, []byte("not sqlite secret"), 0o600)
	_, err = Import(context.Background(), Options{DatabasePath: path})
	if !errors.Is(err, ErrInvalidDatabase) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("got %v", err)
	}
}

func TestImportOlderSchemaFromLockedWAL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Cookies")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE meta(key TEXT,value TEXT)",
		"INSERT INTO meta VALUES('version','23')",
		"CREATE TABLE cookies(host_key TEXT,name TEXT,value TEXT,encrypted_value BLOB,path TEXT,expires_utc INTEGER,secure INTEGER)",
		"INSERT INTO cookies VALUES('example.com','session','value',X'','/',0,1)",
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Import(context.Background(), Options{ProfileDir: dir})
	if err != nil || result.Imported != 1 || !result.Cookies[0].Secure {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestMeta24HostBindingRejectsWrongDomain(t *testing.T) {
	encrypted := encrypt(t, "v10", []byte("peanuts"), []byte("secret-value"), "right.example", 24)
	d := decryptor{metaVersion: 24}
	value, err := d.decrypt(context.Background(), "wrong.example", encrypted)
	if !errors.Is(err, ErrDecrypt) || value != "" || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("value=%q err=%v", value, err)
	}
}

func TestImportZeroesProviderPassword(t *testing.T) {
	host := "example.com"
	password := []byte("caller-owned-keyring-secret")
	provider := &borrowedProvider{password: password}
	rows := [][]any{{host, "protected", "", encrypt(t, "v11", password, []byte("value"), host, 23), "/", int64(0), 0, 0, -1}}
	result, err := Import(context.Background(), Options{DatabasePath: database(t, 23, rows), PasswordProvider: provider})
	if err != nil || result.Imported != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, value := range password {
		if value != 0 {
			t.Fatal("provider password buffer was not zeroed")
		}
	}
}

func TestCredentialLookupCancellation(t *testing.T) {
	host := "example.com"
	rows := [][]any{{host, "protected", "", encrypt(t, "v11", []byte("key"), []byte("value"), host, 23), "/", int64(0), 0, 0, -1}}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Import(ctx, Options{DatabasePath: database(t, 23, rows), PasswordProvider: cancelProvider{cancel: cancel}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func FuzzDecrypt(f *testing.F) {
	f.Add([]byte("v10bad"), "example.com", 23)
	f.Fuzz(func(t *testing.T, data []byte, host string, meta int) {
		if len(data) > 1<<20 || len(host) > 4096 {
			t.Skip()
		}
		d := decryptor{metaVersion: meta}
		_, _ = d.decrypt(context.Background(), host, data)
	})
}
