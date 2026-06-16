package middleware

import "testing"

func TestRandomString(t *testing.T) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	inCharset := func(s string) bool {
		for _, c := range s {
			found := false
			for _, allowed := range charset {
				if c == allowed {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}

	// Length + charset.
	s := randomString(8)
	if len(s) != 8 {
		t.Fatalf("len = %d, want 8", len(s))
	}
	if !inCharset(s) {
		t.Errorf("%q contains chars outside charset", s)
	}

	// Many rapid calls must not collide (the old time-based impl produced
	// identical IDs within a nanosecond window).
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := randomString(8)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q on iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}
