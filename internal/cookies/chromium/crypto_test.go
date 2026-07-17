package chromium

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type corpus struct {
	Version         int    `json:"version"`
	ReferenceCommit string `json:"reference_commit"`
	Vectors         []struct {
		Name            string `json:"name"`
		MetaVersion     int    `json:"meta_version"`
		Host            string `json:"host"`
		Password        string `json:"password"`
		EncryptedBase64 string `json:"encrypted_base64"`
		Expected        string `json:"expected"`
	} `json:"vectors"`
}

type staticKeyProvider struct {
	password []byte
	item     KeychainItem
	err      error
}

func (provider *staticKeyProvider) Password(_ context.Context, item KeychainItem) ([]byte, error) {
	provider.item = item
	if provider.err != nil {
		return nil, provider.err
	}
	return append([]byte(nil), provider.password...), nil
}

func TestDeriveMacKeyMatchesPinnedReference(t *testing.T) {
	key := deriveMacKey([]byte("abc"))
	defer zero(key)
	if got := hex.EncodeToString(key); got != "59e2c0d050f6f4e16cc18c51cb7ccd59" {
		t.Fatalf("key = %s", got)
	}
}

func TestMacDecryptorPinnedCorpus(t *testing.T) {
	fixture := loadCorpus(t)
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			encrypted, err := base64.StdEncoding.DecodeString(vector.EncryptedBase64)
			if err != nil {
				t.Fatal(err)
			}
			provider := &staticKeyProvider{password: []byte(vector.Password)}
			decryptor := &macDecryptor{provider: provider, item: KeychainItem{Account: "Chrome", Service: "Chrome Safe Storage"}}
			defer decryptor.Close()
			value, err := decryptor.Decrypt(context.Background(), vector.Host, encrypted, vector.MetaVersion)
			if err != nil || value != vector.Expected {
				t.Fatalf("Decrypt() = %q, %v", value, err)
			}
			if provider.item.Account != "Chrome" || provider.item.Service != "Chrome Safe Storage" {
				t.Fatalf("keychain item = %#v", provider.item)
			}
		})
	}
}

func TestMacDecryptorRejectsWrongHostPaddingAndKey(t *testing.T) {
	fixture := loadCorpus(t)
	vector := fixture.Vectors[1]
	encrypted, _ := base64.StdEncoding.DecodeString(vector.EncryptedBase64)
	provider := &staticKeyProvider{password: []byte(vector.Password)}
	decryptor := &macDecryptor{provider: provider}
	defer decryptor.Close()
	if _, err := decryptor.Decrypt(context.Background(), ".wrong.example", encrypted, 24); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong-host error = %v", err)
	}
	broken := append([]byte(nil), encrypted...)
	broken[len(broken)-1] ^= 0xff
	if _, err := decryptor.Decrypt(context.Background(), vector.Host, broken, 24); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("padding error = %v", err)
	}
	missing := &macDecryptor{provider: &staticKeyProvider{err: errors.New("secret backend detail")}}
	if _, err := missing.Decrypt(context.Background(), vector.Host, encrypted, 24); !errors.Is(err, ErrKeyUnavailable) || err.Error() != ErrKeyUnavailable.Error() {
		t.Fatalf("key error = %v", err)
	}
}

func FuzzMacDecryptor(f *testing.F) {
	fixture := loadCorpus(f)
	for _, vector := range fixture.Vectors {
		encrypted, err := base64.StdEncoding.DecodeString(vector.EncryptedBase64)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(vector.Host, encrypted, vector.MetaVersion)
	}
	f.Add(".example.com", []byte("legacy-value"), 23)
	f.Fuzz(func(t *testing.T, host string, encrypted []byte, metaVersion int) {
		if len(host) > 4096 || len(encrypted) > 1<<20 {
			t.Skip()
		}
		decryptor := &macDecryptor{provider: &staticKeyProvider{password: []byte("abc")}}
		defer decryptor.Close()
		_, err := decryptor.Decrypt(context.Background(), host, encrypted, metaVersion)
		if err != nil && !errors.Is(err, ErrDecrypt) && !errors.Is(err, ErrKeyUnavailable) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

type testHelper interface {
	Helper()
	Fatal(...any)
	Fatalf(string, ...any)
}

func loadCorpus(t testHelper) corpus {
	t.Helper()
	data, err := os.ReadFile("../../../conformance/cookies/chromium-macos/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture corpus
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.ReferenceCommit == "" || len(fixture.Vectors) < 2 {
		t.Fatalf("invalid corpus: %#v", fixture)
	}
	return fixture
}
