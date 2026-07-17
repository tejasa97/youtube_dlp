package chromium

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/pbkdf2"
)

var (
	macSalt = []byte("saltysalt")
	macIV   = bytes.Repeat([]byte{' '}, aes.BlockSize)
)

type macDecryptor struct {
	provider KeyProvider
	item     KeychainItem
	fetched  bool
	key      []byte
	err      error
}

func (decryptor *macDecryptor) Close() {
	zero(decryptor.key)
	decryptor.key = nil
}

func (decryptor *macDecryptor) Decrypt(ctx context.Context, host string, encrypted []byte, metaVersion int) (string, error) {
	if !bytes.HasPrefix(encrypted, []byte("v10")) {
		if !utf8.Valid(encrypted) {
			return "", ErrDecrypt
		}
		return string(encrypted), nil
	}
	if err := decryptor.ensureKey(ctx); err != nil {
		return "", err
	}
	ciphertext := encrypted[3:]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", ErrDecrypt
	}
	block, err := aes.NewCipher(decryptor.key)
	if err != nil {
		return "", ErrDecrypt
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, macIV).CryptBlocks(plaintext, ciphertext)
	plaintext, err = unpadPKCS7(plaintext, aes.BlockSize)
	if err != nil {
		zero(plaintext)
		return "", ErrDecrypt
	}
	if metaVersion >= 24 {
		if len(plaintext) < sha256.Size {
			zero(plaintext)
			return "", ErrDecrypt
		}
		digest := sha256.Sum256([]byte(host))
		if !bytes.Equal(plaintext[:sha256.Size], digest[:]) {
			zero(plaintext)
			return "", ErrDecrypt
		}
		plaintext = plaintext[sha256.Size:]
	}
	if !utf8.Valid(plaintext) {
		zero(plaintext)
		return "", ErrDecrypt
	}
	value := string(plaintext)
	zero(plaintext)
	return value, nil
}

func (decryptor *macDecryptor) ensureKey(ctx context.Context) error {
	if decryptor.fetched {
		return decryptor.err
	}
	decryptor.fetched = true
	if decryptor.provider == nil {
		decryptor.err = ErrKeyUnavailable
		return decryptor.err
	}
	password, err := decryptor.provider.Password(ctx, decryptor.item)
	if err != nil || len(password) == 0 {
		zero(password)
		decryptor.err = ErrKeyUnavailable
		return decryptor.err
	}
	decryptor.key = deriveMacKey(password)
	zero(password)
	return nil
}

func deriveMacKey(password []byte) []byte {
	return pbkdf2.Key(password, macSalt, 1003, 16, sha1.New)
}

func unpadPKCS7(value []byte, blockSize int) ([]byte, error) {
	if len(value) == 0 || len(value)%blockSize != 0 {
		return nil, errors.New("invalid padded data")
	}
	padding := int(value[len(value)-1])
	if padding == 0 || padding > blockSize || padding > len(value) {
		return nil, errors.New("invalid padding")
	}
	for _, item := range value[len(value)-padding:] {
		if int(item) != padding {
			return nil, errors.New("invalid padding")
		}
	}
	return value[:len(value)-padding], nil
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
