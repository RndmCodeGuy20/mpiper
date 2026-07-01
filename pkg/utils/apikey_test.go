package utils

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateAPIKey_RoundTrip(t *testing.T) {
	mat, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}

	if !strings.HasPrefix(mat.Full, "mp_") {
		t.Errorf("full key %q does not start with mp_", mat.Full)
	}

	// Re-hashing the printed full key must match the stored hash (the property
	// the auth middleware relies on at login time).
	if got := HashAPIKey(mat.Full); got != mat.Hash {
		t.Errorf("re-hash mismatch: got %q want %q", got, mat.Hash)
	}

	// The parsed prefix must equal the stored public prefix.
	prefix, err := ParseAPIKey(mat.Full)
	if err != nil {
		t.Fatalf("ParseAPIKey: %v", err)
	}
	if prefix != mat.Prefix {
		t.Errorf("parsed prefix %q != material prefix %q", prefix, mat.Prefix)
	}

	if len(mat.Hash) != 64 {
		t.Errorf("hash length = %d, want 64 (sha256 hex)", len(mat.Hash))
	}
}

func TestGenerateAPIKey_Unique(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		mat, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("GenerateAPIKey: %v", err)
		}
		if _, dup := seen[mat.Full]; dup {
			t.Fatalf("duplicate key generated: %q", mat.Full)
		}
		seen[mat.Full] = struct{}{}
	}
}

func TestParseAPIKey_Malformed(t *testing.T) {
	cases := []string{
		"",
		"not-a-key",
		"mp_only",
		"xx_prefix_secret", // wrong scheme
		"mp__secret",       // empty prefix
		"mp_prefix_",       // empty secret
		"Bearer mp_a_b",
	}
	for _, c := range cases {
		if _, err := ParseAPIKey(c); err == nil {
			t.Errorf("ParseAPIKey(%q) = nil error, want error", c)
		}
	}
}

func TestHashAPIKey_Deterministic(t *testing.T) {
	const k = "mp_abcd1234_deadbeef"
	if HashAPIKey(k) != HashAPIKey(k) {
		t.Error("HashAPIKey is not deterministic")
	}
	if HashAPIKey(k) == HashAPIKey(k+"x") {
		t.Error("distinct keys hashed to the same value")
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	if IsExpired(nil, now) {
		t.Error("nil expiresAt should never be expired")
	}
	if !IsExpired(&past, now) {
		t.Error("past expiresAt should be expired")
	}
	if IsExpired(&future, now) {
		t.Error("future expiresAt should not be expired")
	}
	// Exactly at expiry is treated as expired (not Before).
	if !IsExpired(&now, now) {
		t.Error("expiresAt == now should be expired")
	}
}

func TestIsRevoked(t *testing.T) {
	if IsRevoked(nil) {
		t.Error("nil revokedAt should not be revoked")
	}
	ts := time.Now()
	if !IsRevoked(&ts) {
		t.Error("set revokedAt should be revoked")
	}
}

func TestConstantTimeHashEqual(t *testing.T) {
	if !ConstantTimeHashEqual("abc", "abc") {
		t.Error("equal hashes should compare equal")
	}
	if ConstantTimeHashEqual("abc", "abd") {
		t.Error("different hashes should not compare equal")
	}
	if ConstantTimeHashEqual("abc", "abcd") {
		t.Error("different-length hashes should not compare equal")
	}
}
