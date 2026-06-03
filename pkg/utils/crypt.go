package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/bcrypt"
)

// GenerateHash creates a bcrypt hash of the given target string.
func GenerateHash(target string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(target), bcrypt.DefaultCost)
	return string(hash), err
}

// CompareHashAndPassword checks if the provided password matches the hashed password.
func CompareHashAndPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// GenerateToken creates an AES-256-GCM encrypted token containing the userID.
// key must be exactly 32 bytes.
func GenerateToken(userID, key string) (string, error) {
	k, err := parseKey(key)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := aesGCM.Seal(nil, nonce, []byte(userID), nil)
	tokenBytes := append(nonce, ciphertext...)

	return base64.RawURLEncoding.EncodeToString(tokenBytes), nil
}

// DecryptToken decrypts a token and returns the user_id.
// key must be exactly 32 bytes.
func DecryptToken(token, key string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}

	k, err := parseKey(key)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}

	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	if len(data) < aesGCM.NonceSize() {
		return "", errors.New("invalid token length")
	}

	nonce := data[:aesGCM.NonceSize()]
	ciphertext := data[aesGCM.NonceSize():]

	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt token: %w", err)
	}

	return string(plaintext), nil
}

func parseKey(key string) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key must be exactly 32 bytes for AES-256, got %d", len(key))
	}
	return []byte(key), nil
}
