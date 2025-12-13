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

const (
	encryptionKeyString = "kmZxB1Ai5OMzJSLqXTtOv6b43RqHCg29"
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

// GenerateToken creates an encrypted token containing the userID.
func GenerateToken(userID string) (string, error) {
	block, err := aes.NewCipher([]byte(encryptionKeyString))
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

// DecryptToken decrypts token and returns the user_id
func DecryptToken(token string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher([]byte(encryptionKeyString))
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
