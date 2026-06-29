package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestBackoff_ExponentialWithCap(t *testing.T) {
	tests := []struct {
		attempt    int
		wantMin    time.Duration
		wantMax    time.Duration
	}{
		{1, 1 * time.Second, 4 * time.Second},    // 2s base ±25%
		{2, 2 * time.Second, 6 * time.Second},    // 4s base ±25%
		{3, 4 * time.Second, 12 * time.Second},   // 8s base ±25%
		{4, 8 * time.Second, 24 * time.Second},   // 16s base ±25%
		{10, 3 * time.Minute, 5*time.Minute + 1}, // capped at 5min (hard cap after jitter)
	}

	for _, tt := range tests {
		// Run multiple times for randomness coverage
		for range 50 {
			got := backoff(tt.attempt)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("backoff(%d) = %v, want [%v, %v]", tt.attempt, got, tt.wantMin, tt.wantMax)
			}
		}
	}
}

func TestBackoff_NeverExceedsFiveMinutes(t *testing.T) {
	for attempt := 1; attempt <= 20; attempt++ {
		for range 100 {
			got := backoff(attempt)
			if got > 5*time.Minute+time.Minute { // generous tolerance for jitter
				t.Fatalf("backoff(%d) = %v exceeds 5min cap", attempt, got)
			}
		}
	}
}

func TestComputeHMAC(t *testing.T) {
	secret := "my-webhook-secret"
	payload := []byte(`{"event":"job.done","asset_id":"abc-123"}`)

	got := computeHMAC(secret, payload)

	// Verify independently
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))

	if got != want {
		t.Errorf("computeHMAC = %s, want %s", got, want)
	}
}

func TestComputeHMAC_DifferentSecrets(t *testing.T) {
	payload := []byte(`{"event":"job.done"}`)
	sig1 := computeHMAC("secret-a", payload)
	sig2 := computeHMAC("secret-b", payload)

	if sig1 == sig2 {
		t.Error("different secrets should produce different signatures")
	}
}
