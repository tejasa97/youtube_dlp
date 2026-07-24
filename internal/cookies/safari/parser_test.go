package safari

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type fixtureCookie struct {
	domain, name, path, value string
	secure                    bool
	expiry, creation          float64
}

func TestParsePinnedSafariCookieCorpus(t *testing.T) {
	expiry := time.Date(2021, 6, 18, 21, 39, 19, 0, time.UTC)
	data := fixtureDatabase(
		fixturePage(fixtureCookie{
			domain: "localhost", name: "foo", path: "/", value: "test%20%3Bcookie",
			expiry: float64(expiry.Unix() - macEpochUnix), creation: 1,
		}),
	)
	// The layout, strings, flags, and expected timestamp are derived from
	// yt-dlp test/test_cookies.py::test_safari_cookie_parsing at the pinned
	// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 reference commit.
	result, err := Parse(context.Background(), data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Imported != 1 || result.Failed != 0 || len(result.Cookies) != 1 {
		t.Fatalf("result = %#v", result)
	}
	cookie := result.Cookies[0]
	if cookie.Domain != "localhost" || cookie.Name != "foo" || cookie.Path != "/" ||
		cookie.Value != "test%20%3Bcookie" || cookie.Secure || !cookie.Expires.Equal(expiry) {
		t.Fatalf("cookie = %#v", cookie)
	}
}

func TestParseMultiplePagesSecureOrderAndFooter(t *testing.T) {
	data := fixtureDatabase(
		fixturePage(
			fixtureCookie{domain: ".example.com", name: "first", path: "/", value: "one", secure: true, expiry: 1, creation: 1},
			fixtureCookie{domain: "example.org", name: "second", path: "/x", value: "two", expiry: 2, creation: 2},
		),
		fixturePage(),
		fixturePage(fixtureCookie{domain: "localhost", name: "third", path: "/", value: "three", expiry: 3, creation: 3}),
	)
	data = append(data, []byte("bounded opaque footer")...)
	result, err := Parse(context.Background(), data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.Imported != 3 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	for index, name := range []string{"first", "second", "third"} {
		if result.Cookies[index].Name != name {
			t.Fatalf("cookie %d = %#v", index, result.Cookies[index])
		}
	}
	if !result.Cookies[0].Secure {
		t.Fatal("secure flag was not preserved")
	}
}

func TestParseRejectsStructuralCorruption(t *testing.T) {
	valid := fixtureDatabase(fixturePage(
		fixtureCookie{domain: "localhost", name: "name", path: "/", value: "value", expiry: 1, creation: 1},
	))
	tests := map[string]func([]byte){
		"database signature": func(data []byte) { copy(data[:4], "nope") },
		"page signature":     func(data []byte) { copy(data[12:16], "nope") },
		"truncated body":     func(data []byte) { binary.BigEndian.PutUint32(data[8:12], uint32(len(data))) },
		"offset in header":   func(data []byte) { binary.LittleEndian.PutUint32(data[20:24], 8) },
		"record size":        func(data []byte) { binary.LittleEndian.PutUint32(data[28:32], math.MaxUint32) },
		"string order": func(data []byte) {
			record := data[28:]
			binary.LittleEndian.PutUint32(record[20:24], binary.LittleEndian.Uint32(record[16:20]))
		},
		"unterminated string": func(data []byte) {
			record := data[28:]
			value := binary.LittleEndian.Uint32(record[28:32])
			record[len(record)-1] = 'x'
			binary.LittleEndian.PutUint32(record[28:32], value)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), valid...)
			mutate(data)
			if _, err := Parse(context.Background(), data, Options{}); !errors.Is(err, ErrInvalidDatabase) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseSkipsFramedInvalidCookieAndEnforcesLimits(t *testing.T) {
	bad := fixtureCookie{domain: "localhost", name: "bad name", path: "/", value: "x", expiry: 1, creation: 1}
	good := fixtureCookie{domain: "localhost", name: "good", path: "/", value: "y", expiry: 1, creation: 1}
	data := fixtureDatabase(fixturePage(bad, good))
	result, err := Parse(context.Background(), data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Imported != 1 || result.Failed != 1 || result.Cookies[0].Name != "good" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := Parse(context.Background(), data, Options{MaxCookies: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("cookie limit error = %v", err)
	}
	if _, err := Parse(context.Background(), data, Options{MaxBytes: int64(len(data) - 1)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("byte limit error = %v", err)
	}
}

func TestParseRejectsInvalidTimestampAndHonorsCancellation(t *testing.T) {
	data := fixtureDatabase(fixturePage(
		fixtureCookie{domain: "localhost", name: "bad", path: "/", value: "x", expiry: math.NaN(), creation: 1},
	))
	result, err := Parse(context.Background(), data, Options{})
	if err != nil || result.Failed != 1 || result.Imported != 0 {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Parse(ctx, data, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestLocateAndReadRegular(t *testing.T) {
	home := t.TempDir()
	secondary := filepath.Join(home, defaultRelativePaths[1])
	if err := os.MkdirAll(filepath.Dir(secondary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondary, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := locate(Options{HomeDir: home})
	if err != nil || path != secondary {
		t.Fatalf("path=%q error=%v", path, err)
	}
	if _, err := locate(Options{DatabasePath: "relative"}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("relative path error = %v", err)
	}
	link := filepath.Join(home, "link")
	if err := os.Symlink(secondary, link); err != nil {
		t.Fatal(err)
	}
	if _, err := locate(Options{DatabasePath: link}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readRegular(ctx, secondary, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancellation error = %v", err)
	}
}

func TestImportExplicitSyntheticDatabaseOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real Safari filesystem dispatch is macOS-only")
	}
	path := filepath.Join(t.TempDir(), "Cookies.binarycookies")
	data := fixtureDatabase(fixturePage(
		fixtureCookie{domain: "localhost", name: "fixture", path: "/", value: "secret", expiry: 1, creation: 1},
	))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Import(context.Background(), Options{DatabasePath: path})
	if err != nil || result.Imported != 1 || result.Cookies[0].Value != "secret" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestFilesystemErrorsDoNotExposePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-user-path")
	_, err := readRegular(context.Background(), path, 0)
	if err == nil || bytes.Contains([]byte(err.Error()), []byte("private-user-path")) {
		t.Fatalf("error = %v", err)
	}
}

func fixtureDatabase(pages ...[]byte) []byte {
	var output bytes.Buffer
	output.WriteString(databaseMagic)
	_ = binary.Write(&output, binary.BigEndian, uint32(len(pages)))
	for _, page := range pages {
		_ = binary.Write(&output, binary.BigEndian, uint32(len(page)))
	}
	for _, page := range pages {
		output.Write(page)
	}
	return output.Bytes()
}

func fixturePage(cookies ...fixtureCookie) []byte {
	records := make([][]byte, len(cookies))
	for index, cookie := range cookies {
		records[index] = fixtureRecord(cookie)
	}
	headerBytes := 8 + 4*len(records)
	if len(records) > 0 {
		headerBytes += 4 // opaque page header field present in the pinned corpus
	}
	output := make([]byte, headerBytes)
	copy(output[:4], pageMagic)
	binary.LittleEndian.PutUint32(output[4:8], uint32(len(records)))
	offset := headerBytes
	for index, record := range records {
		binary.LittleEndian.PutUint32(output[8+index*4:], uint32(offset))
		output = append(output, record...)
		offset += len(record)
	}
	return output
}

func fixtureRecord(cookie fixtureCookie) []byte {
	fields := []string{cookie.domain, cookie.name, cookie.path, cookie.value}
	size := recordHeaderBytes
	for _, field := range fields {
		size += len(field) + 1
	}
	record := make([]byte, size)
	binary.LittleEndian.PutUint32(record[:4], uint32(size))
	if cookie.secure {
		binary.LittleEndian.PutUint32(record[8:12], 1)
	}
	offset := recordHeaderBytes
	for index, field := range fields {
		binary.LittleEndian.PutUint32(record[16+index*4:], uint32(offset))
		copy(record[offset:], field)
		offset += len(field) + 1
	}
	binary.LittleEndian.PutUint64(record[40:48], math.Float64bits(cookie.expiry))
	binary.LittleEndian.PutUint64(record[48:56], math.Float64bits(cookie.creation))
	return record
}

func assertValidCookie(t *testing.T, cookie *http.Cookie) {
	t.Helper()
	if cookie == nil || cookie.Valid() != nil {
		t.Fatalf("invalid cookie: %#v", cookie)
	}
}
