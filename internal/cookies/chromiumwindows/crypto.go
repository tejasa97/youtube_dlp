package chromiumwindows

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

const (
	maximumEncryptedCookieBytes = 16 << 20
	chromiumGCMNonceBytes       = 12
	chromiumGCMTagBytes         = 16
)

type cookieDecryptor struct {
	protector      DataProtector
	appBound       AppBoundDecryptor
	localStatePath string
	maxStateBytes  int64
	masterKey      []byte
	masterLoaded   bool
	masterErr      error
}

func (decryptor *cookieDecryptor) close() {
	clear(decryptor.masterKey)
	decryptor.masterKey = nil
}

func (decryptor *cookieDecryptor) decrypt(ctx context.Context, encrypted []byte, host string, metaVersion int) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if len(encrypted) == 0 || len(encrypted) > maximumEncryptedCookieBytes {
		return "", "", ErrLimit
	}
	version := "legacy"
	var plaintext []byte
	var err error
	if len(encrypted) >= 3 {
		version = string(encrypted[:3])
	}
	switch version {
	case "v10", "v11":
		if len(encrypted) <= 3+chromiumGCMNonceBytes+chromiumGCMTagBytes {
			return "", version, ErrDecrypt
		}
		key, keyErr := decryptor.key(ctx)
		if keyErr != nil {
			return "", version, keyErr
		}
		plaintext, err = decryptAESGCM(encrypted[3:], key)
	case "v20":
		if len(encrypted) <= 3 || decryptor.appBound == nil {
			return "", version, ErrAppBound
		}
		plaintext, err = decryptor.appBound.DecryptAppBound(ctx, append([]byte(nil), encrypted[3:]...))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", version, err
			}
			err = ErrAppBound
		}
	default:
		version = "legacy"
		if decryptor.protector == nil {
			return "", version, ErrKeyUnavailable
		}
		plaintext, err = decryptor.protector.Unprotect(ctx, append([]byte(nil), encrypted...))
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", version, err
		}
		if errors.Is(err, ErrAppBound) || errors.Is(err, ErrKeyUnavailable) || errors.Is(err, ErrInvalidLocalState) || errors.Is(err, ErrUnsafePath) || errors.Is(err, ErrLimit) {
			return "", version, err
		}
		return "", version, ErrDecrypt
	}
	defer clear(plaintext)
	if metaVersion >= 24 {
		if len(plaintext) < sha256.Size {
			return "", version, ErrDecrypt
		}
		expected := sha256.Sum256([]byte(host))
		if subtle.ConstantTimeCompare(plaintext[:sha256.Size], expected[:]) != 1 {
			return "", version, ErrDecrypt
		}
		plaintext = plaintext[sha256.Size:]
	}
	if !utf8.Valid(plaintext) {
		return "", version, ErrDecrypt
	}
	return string(plaintext), version, nil
}

func (decryptor *cookieDecryptor) key(ctx context.Context) ([]byte, error) {
	if decryptor.masterLoaded {
		if decryptor.masterErr != nil {
			return nil, decryptor.masterErr
		}
		return decryptor.masterKey, nil
	}
	decryptor.masterLoaded = true
	if decryptor.protector == nil || decryptor.localStatePath == "" {
		decryptor.masterErr = ErrKeyUnavailable
		return nil, decryptor.masterErr
	}
	encoded, err := readSecureBounded(ctx, decryptor.localStatePath, decryptor.maxStateBytes)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		switch {
		case errors.Is(err, ErrUnsafePath), errors.Is(err, ErrLimit):
			decryptor.masterErr = err
		case errors.Is(err, ErrNotFound):
			decryptor.masterErr = ErrKeyUnavailable
		default:
			decryptor.masterErr = ErrInvalidLocalState
		}
		return nil, decryptor.masterErr
	}
	defer clear(encoded)
	var state struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if err := pluginapi.ValidateJSONFrame(encoded); err != nil {
		decryptor.masterErr = ErrInvalidLocalState
		return nil, decryptor.masterErr
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&state); err != nil || state.OSCrypt.EncryptedKey == "" || len(state.OSCrypt.EncryptedKey) > 64<<10 {
		decryptor.masterErr = ErrInvalidLocalState
		return nil, decryptor.masterErr
	}
	wrapped, err := base64.StdEncoding.DecodeString(state.OSCrypt.EncryptedKey)
	if err != nil || !bytes.HasPrefix(wrapped, []byte("DPAPI")) || len(wrapped) <= len("DPAPI") {
		decryptor.masterErr = ErrInvalidLocalState
		return nil, decryptor.masterErr
	}
	defer clear(wrapped)
	key, err := decryptor.protector.Unprotect(ctx, append([]byte(nil), wrapped[len("DPAPI"):]...))
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		clear(key)
		return nil, err
	}
	if err != nil || len(key) != 32 {
		clear(key)
		decryptor.masterErr = ErrKeyUnavailable
		return nil, decryptor.masterErr
	}
	decryptor.masterKey = append([]byte(nil), key...)
	clear(key)
	return decryptor.masterKey, nil
}

func decryptAESGCM(payload, key []byte) ([]byte, error) {
	if len(key) != 32 || len(payload) <= chromiumGCMNonceBytes+chromiumGCMTagBytes {
		return nil, ErrDecrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrDecrypt
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrDecrypt
	}
	nonce := payload[:chromiumGCMNonceBytes]
	ciphertextAndTag := payload[chromiumGCMNonceBytes:]
	plaintext, err := aead.Open(nil, nonce, ciphertextAndTag, nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

func readSecureBounded(ctx context.Context, path string, maximum int64) ([]byte, error) {
	file, info, err := openSecureSource(path, maximum)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > maximum {
		return nil, ErrLimit
	}
	buffer := make([]byte, info.Size())
	read := 0
	for read < len(buffer) {
		if err := ctx.Err(); err != nil {
			clear(buffer)
			return nil, err
		}
		count, err := file.Read(buffer[read:])
		read += count
		if err != nil {
			clear(buffer)
			return nil, ErrInvalidLocalState
		}
		if count == 0 {
			clear(buffer)
			return nil, ErrInvalidLocalState
		}
	}
	after, err := file.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		clear(buffer)
		return nil, ErrInvalidLocalState
	}
	return buffer, nil
}
