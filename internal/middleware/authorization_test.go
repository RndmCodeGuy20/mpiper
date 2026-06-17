package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

// 32-byte AES-256 key for the test singleton.
const testEncryptionKey = "0123456789abcdef0123456789abcdef"

func TestMain(m *testing.M) {
	config.Init(config.EnvConfig{EncryptionKey: testEncryptionKey})
	m.Run()
}

// newGate wraps a handler that records whether it ran with AuthMiddleware.
func newGate(t *testing.T) (http.Handler, *bool) {
	t.Helper()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	return AuthMiddleware(zap.NewNop())(next), &called
}

func TestAuthMiddleware_RejectsUnauthenticated(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"non-bearer scheme", "Basic abc123"},
		{"bearer without token", "Bearer "},
		{"malformed token", "Bearer not-a-valid-token"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gate, called := newGate(t)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/x/complete", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()

			gate.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if *called {
				t.Error("next handler ran for unauthenticated request — gate leaked")
			}
		})
	}
}

func TestAuthMiddleware_AllowsValidTokenAndPopulatesUserID(t *testing.T) {
	const wantUserID = "user-42"
	token, err := utils.GenerateToken(wantUserID, testEncryptionKey)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	var gotUserID string
	var gotOK bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID, gotOK = GetUserID(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	gate := AuthMiddleware(zap.NewNop())(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/x/complete", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !gotOK {
		t.Fatal("GetUserID returned ok=false — userID not injected into context")
	}
	if gotUserID != wantUserID {
		t.Errorf("userID = %q, want %q", gotUserID, wantUserID)
	}
}
