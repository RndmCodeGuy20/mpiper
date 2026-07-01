package utils

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// API key wire format: mp_<prefix>_<secret>
//
//   - "mp"     a fixed scheme tag so keys are recognizable in logs/UIs.
//   - prefix   a short, PUBLIC identifier (hex). Stored in cleartext and used to
//              narrow the hash lookup; safe to display in key listings.
//   - secret   high-entropy random (hex). Never stored; only its hash is kept.
//
// Both prefix and secret are hex-encoded so the '_' delimiter never collides
// with the encoding alphabet (unlike base64url, which uses '_').
const (
	apiKeyScheme    = "mp"
	apiKeyPrefixLen = 4  // bytes -> 8 hex chars
	apiKeySecretLen = 24 // bytes -> 48 hex chars (192 bits of entropy)
)

var (
	// ErrMalformedAPIKey is returned when a presented key does not match the
	// mp_<prefix>_<secret> shape.
	ErrMalformedAPIKey = errors.New("malformed api key")
)

// APIKeyMaterial is the result of minting a new API key. Full is shown to the
// caller exactly once; only Hash (and the public Prefix) are persisted.
type APIKeyMaterial struct {
	Full   string // mp_<prefix>_<secret> — show once, never store
	Prefix string // public lookup hint
	Hash   string // SHA-256 hex of Full — store this
}

// GenerateAPIKey mints a new random API key.
func GenerateAPIKey() (APIKeyMaterial, error) {
	prefixBytes := make([]byte, apiKeyPrefixLen)
	if _, err := rand.Read(prefixBytes); err != nil {
		return APIKeyMaterial{}, fmt.Errorf("generate api key prefix: %w", err)
	}
	secretBytes := make([]byte, apiKeySecretLen)
	if _, err := rand.Read(secretBytes); err != nil {
		return APIKeyMaterial{}, fmt.Errorf("generate api key secret: %w", err)
	}

	prefix := hex.EncodeToString(prefixBytes)
	secret := hex.EncodeToString(secretBytes)
	full := fmt.Sprintf("%s_%s_%s", apiKeyScheme, prefix, secret)

	return APIKeyMaterial{
		Full:   full,
		Prefix: prefix,
		Hash:   HashAPIKey(full),
	}, nil
}

// HashAPIKey returns the lowercase hex SHA-256 of a full API key. API keys are
// high-entropy, so a fast hash with an indexed equality lookup is appropriate
// (and unlike bcrypt, allows the DB to index key_hash).
func HashAPIKey(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}

// ParseAPIKey validates the wire format and returns the public prefix. The
// secret is intentionally not returned — callers authenticate by hashing the
// full key, not by inspecting the secret.
func ParseAPIKey(full string) (prefix string, err error) {
	parts := strings.SplitN(full, "_", 3)
	if len(parts) != 3 || parts[0] != apiKeyScheme || parts[1] == "" || parts[2] == "" {
		return "", ErrMalformedAPIKey
	}
	return parts[1], nil
}

// ConstantTimeHashEqual compares two key hashes without leaking timing
// information. Lookups are by indexed equality, but this guards any in-process
// comparison path.
func ConstantTimeHashEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// IsExpired reports whether expiresAt is set and in the past relative to now.
// A nil expiresAt means the key never expires.
func IsExpired(expiresAt *time.Time, now time.Time) bool {
	return expiresAt != nil && !now.Before(*expiresAt)
}

// IsRevoked reports whether revokedAt is set (i.e. the key has been revoked).
func IsRevoked(revokedAt *time.Time) bool {
	return revokedAt != nil
}
