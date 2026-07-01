package tenant

import (
	"strings"
	"testing"
)

func TestIsValidSlug(t *testing.T) {
	// Build boundary strings of exact length to avoid off-by-one errors
	// when counting by hand.
	boundary64 := strings.Repeat("a", 64)
	boundary65 := strings.Repeat("a", 65)
	boundary128 := strings.Repeat("a", 128)

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// Valid cases
		{"simple lowercase", "acme", true},
		{"with digits", "team42", true},
		{"with underscore", "demo_user", true},
		{"with hyphen", "demo-user", true},
		{"mixed", "acme_corp-2026", true},
		{"single char", "a", true},
		{"single digit", "7", true},
		{"single underscore", "_", true},
		{"single hyphen", "-", true},

		// Boundary: exactly 64 chars (max accepted)
		{"64 chars exact", boundary64, true},

		// Invalid: empty
		{"empty string", "", false},

		// Invalid: 65 chars (one past the limit)
		{"65 chars one over", boundary65, false},

		// Invalid: well past the limit
		{"128 chars", boundary128, false},

		// Invalid: uppercase
		{"uppercase rejected", "Acme", false},

		// Invalid: space
		{"space rejected", "demo user", false},

		// Invalid: slash (path separator, would break storage keys)
		{"slash rejected", "demo/user", false},

		// Invalid: dot
		{"dot rejected", "demo.user", false},

		// Invalid: plus
		{"plus rejected", "demo+user", false},

		// Invalid: at sign
		{"at rejected", "demo@user", false},

		// Invalid: colon
		{"colon rejected", "demo:user", false},

		// Invalid: unicode
		{"unicode rejected", "démö", false},

		// Invalid: newline
		{"newline rejected", "demo\nuser", false},

		// Invalid: null byte
		{"null byte rejected", "demo\x00user", false},

		// The pattern allows leading underscore/hyphen. Verify that is
		// intentionally accepted so callers know to enforce stricter rules
		// at the policy layer if needed.
		{"leading hyphen", "-leading", true},
		{"leading underscore", "_leading", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsValidSlug(tc.input)
			if got != tc.want {
				t.Errorf("IsValidSlug(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
