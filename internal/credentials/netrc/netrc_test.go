package netrc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fixture(t testing.TB) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "auth", "netrc", "valid.netrc"))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func parsedFixture(t testing.TB) *Store {
	t.Helper()
	store, err := Parse(context.Background(), bytes.NewReader(fixture(t)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestParseLookupDuplicateDefaultQuotedEscapedAndEmpty(t *testing.T) {
	store := parsedFixture(t)
	if store.Count() != 4 {
		t.Fatalf("count=%d", store.Count())
	}
	tests := []struct {
		host string
		want Credential
	}{
		{"example.com", Credential{Login: "last user", Account: "billing-account", Password: "synthetic secret # 2"}},
		{"EXAMPLE.COM:443", Credential{Login: "last user", Account: "billing-account", Password: "synthetic secret # 2"}},
		{"api.example.com:8443", Credential{Login: "port-user", Password: "escaped secret"}},
		{"api.example.com:9443", Credential{Login: "fallback-user", Password: "fallback-secret"}},
		{"bücher.example", Credential{Login: "idna-user", Password: "unicode-secret"}},
		{"xn--bcher-kva.example", Credential{Login: "idna-user", Password: "unicode-secret"}},
		{"empty.example", Credential{}},
		{"missing.example", Credential{Login: "fallback-user", Password: "fallback-secret"}},
	}
	for _, test := range tests {
		credential, ok, err := store.Lookup(context.Background(), test.host)
		if err != nil || !ok || credential != test.want {
			t.Fatalf("host=%q credential=%s ok=%t error=%v", test.host, credential, ok, err)
		}
	}
}

func TestServiceAliasCaseSensitivityAndNoDefault(t *testing.T) {
	store, err := Parse(context.Background(), strings.NewReader("machine normal_use login user password pass\n"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if credential, ok, err := store.Lookup(context.Background(), "normal_use"); err != nil || !ok || credential.Login != "user" {
		t.Fatalf("alias credential=%s ok=%t error=%v", credential, ok, err)
	}
	if _, ok, err := store.Lookup(context.Background(), "NORMAL_USE"); err != nil || ok {
		t.Fatalf("case-sensitive alias ok=%t error=%v", ok, err)
	}
}

func TestMacdefBodyIsIgnoredAndNeverParsed(t *testing.T) {
	input := "macdef dangerous\n" +
		"machine fake login leaked password secret\n" +
		"unterminated ' quote and # opaque\n\n" +
		"machine real login user password pass\n"
	store, err := Parse(context.Background(), strings.NewReader(input), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if store.Count() != 1 {
		t.Fatalf("count=%d", store.Count())
	}
	if _, ok, _ := store.Lookup(context.Background(), "fake"); ok {
		t.Fatal("macro content became a credential")
	}
}

func TestMalformedInputErrorsAreCategorizedAndSecretSafe(t *testing.T) {
	secret := "DO-NOT-ECHO-SYNTHETIC-SECRET"
	tests := []string{
		"machine host login user",
		"machine host login user password " + secret + " unexpected field",
		"machine host login 'unterminated password " + secret,
		"machine _invalid login user password " + secret,
		"machine host login user password trailing\\",
		"macdef",
		"unknown " + secret,
		"machine host login user password " + secret + "\x00",
		"machine host login user password \xff",
	}
	for index, input := range tests {
		_, err := Parse(context.Background(), strings.NewReader(input), Limits{})
		if !errors.Is(err, ErrSyntax) {
			t.Fatalf("case=%d error=%v", index, err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("case=%d leaked secret in %q", index, err)
		}
	}
	credential := Credential{Login: secret, Account: secret, Password: secret}
	for _, formatted := range []string{fmt.Sprint(credential), fmt.Sprintf("%#v", credential)} {
		if strings.Contains(formatted, secret) || !strings.Contains(formatted, "redacted") {
			t.Fatalf("credential formatting=%q", formatted)
		}
	}
}

func TestResourceBounds(t *testing.T) {
	base := "machine host login user password pass\n"
	tests := []struct {
		name   string
		input  string
		limits Limits
	}{
		{"bytes", base, Limits{MaxBytes: 8}},
		{"entries", base + "machine other login user password pass\n", Limits{MaxEntries: 1}},
		{"token", "machine hostname login user password pass\n", Limits{MaxTokenBytes: 4}},
		{"macros", "macdef one\na\n\nmacdef two\nb\n\n" + base, Limits{MaxMacros: 1}},
		{"macro bytes", "macdef one\nlong macro body\n\n" + base, Limits{MaxMacroBytes: 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(context.Background(), strings.NewReader(test.input), test.limits); !errors.Is(err, ErrLimit) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for _, limits := range []Limits{{MaxBytes: -1}, {MaxEntries: hardMaxEntries + 1}, {MaxTokenBytes: hardMaxTokenBytes + 1}, {MaxMacros: hardMaxMacros + 1}, {MaxMacroBytes: hardMaxMacroBytes + 1}} {
		if _, err := Parse(context.Background(), strings.NewReader(base), limits); !errors.Is(err, ErrLimit) {
			t.Fatalf("limits=%+v error=%v", limits, err)
		}
	}
}

func TestCancellationDuringParseAndLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Parse(ctx, strings.NewReader("machine host login user password pass"), Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel parse error=%v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader := &cancelReader{payload: fixture(t), cancel: cancel}
	if _, err := Parse(ctx, reader, Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-read parse error=%v", err)
	}
	if _, _, err := parsedFixture(t).Lookup(ctx, "example.com"); !errors.Is(err, context.Canceled) {
		t.Fatalf("lookup error=%v", err)
	}
}

func TestReaderFailuresAreCategorizedAndRedacted(t *testing.T) {
	secret := "READER-ERROR-SECRET"
	if _, err := Parse(context.Background(), errorReader{err: errors.New(secret)}, Limits{}); !errors.Is(err, ErrIO) || strings.Contains(err.Error(), secret) {
		t.Fatalf("reader error=%v", err)
	}
	if _, err := Parse(context.Background(), zeroReader{}, Limits{}); !errors.Is(err, ErrSyntax) {
		t.Fatalf("zero-progress error=%v", err)
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

type cancelReader struct {
	payload []byte
	cancel  context.CancelFunc
	done    bool
}

func (reader *cancelReader) Read(target []byte) (int, error) {
	if reader.done {
		return 0, io.EOF
	}
	reader.done = true
	written := copy(target, reader.payload[:min(16, len(reader.payload))])
	reader.cancel()
	return written, nil
}

func TestCanonicalHostPolicyAndInvalidAuthorities(t *testing.T) {
	store, err := Parse(context.Background(), strings.NewReader(
		"machine [2001:db8::1]:443 login ip password pass\n"+
			"machine Example.COM. login dns password pass\n"), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"[2001:0db8::1]:443", "example.com", "EXAMPLE.COM."} {
		if _, ok, err := store.Lookup(context.Background(), host); err != nil || !ok {
			t.Fatalf("host=%q ok=%t error=%v", host, ok, err)
		}
	}
	for _, host := range []string{"", " https://example.com", "https://example.com", "user@example.com", "example.com/path", "example.com:0", "example.com:080", "[2001:db8::1]"} {
		if _, _, err := store.Lookup(context.Background(), host); !errors.Is(err, ErrInvalidHost) {
			t.Fatalf("host=%q error=%v", host, err)
		}
	}
}

func TestConcurrentLookupIsReadOnly(t *testing.T) {
	store := parsedFixture(t)
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				credential, ok, err := store.Lookup(context.Background(), "example.com:443")
				if err != nil || !ok || credential.Login != "last user" {
					t.Errorf("credential=%s ok=%t error=%v", credential, ok, err)
					return
				}
			}
		}()
	}
	wait.Wait()
}

func FuzzParse(f *testing.F) {
	f.Add(fixture(f))
	f.Add([]byte("machine host login user password pass"))
	f.Add([]byte("macdef x\nopaque\n\n"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 256<<10 {
			t.Skip()
		}
		store, err := Parse(context.Background(), bytes.NewReader(input), Limits{MaxBytes: 256 << 10, MaxEntries: 128, MaxTokenBytes: 4096, MaxMacros: 16, MaxMacroBytes: 16 << 10})
		if err == nil {
			_, _, _ = store.Lookup(context.Background(), "example.com:443")
		}
	})
}

func FuzzLookupHost(f *testing.F) {
	store := parsedFixture(f)
	f.Add("example.com:443")
	f.Add("bücher.example")
	f.Add("https://invalid.example")
	f.Fuzz(func(t *testing.T, host string) {
		if len(host) > 16<<10 {
			t.Skip()
		}
		_, _, _ = store.Lookup(context.Background(), host)
	})
}
