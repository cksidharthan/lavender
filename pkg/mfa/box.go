package mfa

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const keyBytes = 32

// KeyFileName is the name of the file in the data directory that holds the key, when none is given.
const KeyFileName = "secret.key"

// Key is the secret of the application. It seals the secrets of the users that must be read back, and signs the
// short lived tokens of the sign in.
type Key []byte

// LoadKey returns the key. It is the base64 of 32 bytes in given, which comes from the configuration. Without it the key is
// in a file in dir, which is made, with a random key, the first time.
func LoadKey(given, dir string) (Key, error) {
	if given = strings.TrimSpace(given); given != "" {
		key, err := base64.StdEncoding.DecodeString(given)
		if err != nil || len(key) != keyBytes {
			return nil, fmt.Errorf("the secret key must be %d bytes in base64, `openssl rand -base64 %d` makes one", keyBytes, keyBytes)
		}

		return key, nil
	}

	path := filepath.Join(dir, KeyFileName)

	data, err := os.ReadFile(path)
	if err == nil {
		key, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(key) != keyBytes {
			return nil, fmt.Errorf("the key in %s is not valid", path)
		}

		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("error reading the key: %w", err)
	}

	key := make([]byte, keyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("error making a key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("error making the directory of the key: %w", err)
	}
	// exclusive, so two instances that start together do not each keep a key of their own
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadKey("", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("error writing the key: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		return nil, fmt.Errorf("error writing the key: %w", err)
	}

	return key, nil
}

// seal encrypts and authenticates the text, the result is text too.
func (k Key) seal(plain string) (string, error) {
	gcm, err := k.gcm()
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("error making a nonce: %w", err)
	}

	return base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// open reads what seal made. It fails when the text was changed, or sealed with another key.
func (k Key) open(sealed string) (string, error) {
	gcm, err := k.gcm()
	if err != nil {
		return "", err
	}

	data, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil || len(data) < gcm.NonceSize() {
		return "", errors.New("the sealed secret is not valid")
	}

	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("the sealed secret can not be opened, the key is not the one it was sealed with")
	}

	return string(plain), nil
}

func (k Key) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, fmt.Errorf("error using the key: %w", err)
	}

	return cipher.NewGCM(block)
}
