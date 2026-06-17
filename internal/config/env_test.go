package config

import "testing"

func TestParseTLSInsecure(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"unset defaults to plaintext", "", true},
		{"explicit false enables TLS", "false", false},
		{"uppercase FALSE enables TLS", "FALSE", false},
		{"padded false enables TLS", "  false  ", false},
		{"explicit true is plaintext", "true", true},
		{"garbage value is plaintext", "yes", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseTLSInsecure(tc.raw); got != tc.want {
				t.Errorf("parseTLSInsecure(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
