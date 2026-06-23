package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Cipher provides AES-256-GCM authenticated encryption.
type Cipher struct {
	aesGCM cipher.AEAD
}

// NewCipher creates a Cipher from a raw 32-byte key.
// The key is typically read from a Kubernetes Secret mounted at a path.
func NewCipher(rawKey []byte) (*Cipher, error) {
	// Support both raw bytes and hex-encoded strings (e.g. from `openssl rand -hex 32`)
	key := rawKey
	trimmed := strings.TrimSpace(string(rawKey))
	if len(trimmed) == 64 {
		decoded, err := hex.DecodeString(trimmed)
		if err == nil && len(decoded) == 32 {
			key = decoded
		}
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes (got %d)", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return &Cipher{aesGCM: gcm}, nil
}

// Encrypt encrypts plaintext and returns (ciphertext, nonce, error).
// Both must be stored; nonce is required for decryption.
func (c *Cipher) Encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, c.aesGCM.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext = c.aesGCM.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// EncryptString is a convenience wrapper for string plaintexts.
func (c *Cipher) EncryptString(plaintext string) (ciphertext, nonce []byte, err error) {
	return c.Encrypt([]byte(plaintext))
}

// Decrypt decrypts ciphertext using the given nonce.
func (c *Cipher) Decrypt(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != c.aesGCM.NonceSize() {
		return nil, errors.New("invalid nonce size")
	}
	plaintext, err := c.aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w (ciphertext may be tampered)", err)
	}
	return plaintext, nil
}

// DecryptString decrypts and returns a string.
func (c *Cipher) DecryptString(ciphertext, nonce []byte) (string, error) {
	b, err := c.Decrypt(ciphertext, nonce)
	return string(b), err
}

// GeneratePassword generates a cryptographically secure random password.
func GeneratePassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	b := make([]byte, length)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b), nil
}
