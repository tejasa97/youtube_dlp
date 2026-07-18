package chromiumlinux

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"

	"golang.org/x/crypto/pbkdf2"
)

var linuxIV = bytes.Repeat([]byte{' '}, aes.BlockSize)

type decryptor struct {
	metaVersion    int
	provider       PasswordProvider
	service        string
	v11Key         []byte
	v11Loaded      bool
	v11Unavailable bool
}

func DeriveKey(password []byte) []byte {
	return pbkdf2.Key(password, []byte("saltysalt"), 1, 16, sha1.New)
}

func (d *decryptor) Close() { zero(d.v11Key) }

func (d *decryptor) decrypt(ctx context.Context, host string, encrypted []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(encrypted) < 3 {
		return "", ErrDecrypt
	}
	version := string(encrypted[:3])
	ciphertext := encrypted[3:]
	var keys [][]byte
	switch version {
	case "v10":
		keys = [][]byte{DeriveKey([]byte("peanuts")), DeriveKey(nil)}
	case "v11":
		key, err := d.loadV11(ctx)
		if err != nil {
			return "", err
		}
		keys = [][]byte{key, DeriveKey(nil)}
	default:
		return "", ErrDecrypt
	}
	defer func() {
		for index, key := range keys {
			if version != "v11" || index != 0 {
				zero(key)
			}
		}
	}()
	for _, key := range keys {
		plaintext, err := decryptCBC(key, ciphertext)
		if err != nil {
			continue
		}
		fullPlaintext := plaintext[:cap(plaintext)]
		if d.metaVersion >= 24 {
			digest := sha256.Sum256([]byte(host))
			if len(plaintext) < len(digest) || !bytes.Equal(plaintext[:len(digest)], digest[:]) {
				zero(fullPlaintext)
				continue
			}
			plaintext = plaintext[len(digest):]
		}
		if !validValue(plaintext) {
			zero(fullPlaintext)
			continue
		}
		value := string(plaintext)
		zero(fullPlaintext)
		return value, nil
	}
	return "", ErrDecrypt
}

func (d *decryptor) loadV11(ctx context.Context) ([]byte, error) {
	if d.v11Loaded {
		if d.v11Unavailable {
			return nil, ErrKeyUnavailable
		}
		return d.v11Key, nil
	}
	d.v11Loaded = true
	if d.provider == nil {
		d.v11Unavailable = true
		return nil, ErrKeyUnavailable
	}
	password, err := d.provider.Password(ctx, d.service)
	if err != nil {
		zero(password)
		d.v11Unavailable = true
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, ErrKeyUnavailable
	}
	d.v11Key = DeriveKey(password)
	zero(password)
	return d.v11Key, nil
}

func decryptCBC(key, ciphertext []byte) ([]byte, error) {
	if len(key) != 16 || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrDecrypt
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrDecrypt
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, linuxIV).CryptBlocks(plaintext, ciphertext)
	padding := int(plaintext[len(plaintext)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(plaintext) {
		zero(plaintext)
		return nil, ErrDecrypt
	}
	for _, value := range plaintext[len(plaintext)-padding:] {
		if int(value) != padding {
			zero(plaintext)
			return nil, ErrDecrypt
		}
	}
	return plaintext[:len(plaintext)-padding], nil
}

func validValue(value []byte) bool { return !bytes.ContainsAny(value, "\x00\r\n") }
func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
