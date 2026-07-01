package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

// fakeAuthenticator is an in-memory APIKeyAuthenticator keyed by hash.
type fakeAuthenticator struct {
	byHash map[string]*repository.APIKey
	err    error
}

func (f *fakeAuthenticator) GetByHash(_ context.Context, keyHash string) (*repository.APIKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	k, ok := f.byHash[keyHash]
	if !ok {
		return nil, repository.ErrAPIKeyNotFound
	}
	return k, nil
}

// mintKey generates a valid API key and registers it in the fake with the
// given tenant/expiry/revocation, returning the plaintext key.
func mintKey(f *fakeAuthenticator, tenant string, expiresAt, revokedAt *time.Time) string {
	mat, err := utils.GenerateAPIKey()
	if err != nil {
		panic(err)
	}
	if f.byHash == nil {
		f.byHash = map[string]*repository.APIKey{}
	}
	f.byHash[mat.Hash] = &repository.APIKey{
		ID:        uuid.New(),
		TenantID:  tenant,
		KeyHash:   mat.Hash,
		Prefix:    mat.Prefix,
		ScopesRaw: []byte(`["assets:write"]`),
		ExpiresAt: expiresAt,
		RevokedAt: revokedAt,
	}
	return mat.Full
}

func TestAuthMiddleware_RejectsUnauthenticated(t *testing.T) {
	f := &fakeAuthenticator{}
	tests := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"non-bearer scheme", "Basic abc123"},
		{"bearer without token", "Bearer "},
		{"malformed key", "Bearer not-a-valid-key"},
		{"unknown key", "Bearer " + func() string { m, _ := utils.GenerateAPIKey(); return m.Full }()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			gate := AuthMiddleware(zap.NewNop(), f)(next)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/x/complete", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			gate.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if called {
				t.Error("next handler ran for unauthenticated request — gate leaked")
			}
		})
	}
}

func TestAuthMiddleware_AllowsValidKeyAndPopulatesTenant(t *testing.T) {
	const wantTenant = "tenant-42"
	f := &fakeAuthenticator{}
	key := mintKey(f, wantTenant, nil, nil)

	var gotTenant string
	var gotOK bool
	var gotScopes []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant, gotOK = GetTenant(r.Context())
		gotScopes = GetScopes(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	gate := AuthMiddleware(zap.NewNop(), f)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/x/complete", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !gotOK {
		t.Fatal("GetTenant returned ok=false — tenant not injected into context")
	}
	if gotTenant != wantTenant {
		t.Errorf("tenant = %q, want %q", gotTenant, wantTenant)
	}
	if len(gotScopes) != 1 || gotScopes[0] != "assets:write" {
		t.Errorf("scopes = %v, want [assets:write]", gotScopes)
	}
}

func TestAuthMiddleware_RejectsExpiredKey(t *testing.T) {
	f := &fakeAuthenticator{}
	past := time.Now().Add(-time.Hour)
	key := mintKey(f, "tenant-1", &past, nil)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	gate := AuthMiddleware(zap.NewNop(), f)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/x/complete", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Error("next handler ran for expired key")
	}
}

func TestAuthMiddleware_RejectsRevokedKey(t *testing.T) {
	f := &fakeAuthenticator{}
	revoked := time.Now().Add(-time.Minute)
	key := mintKey(f, "tenant-1", nil, &revoked)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	gate := AuthMiddleware(zap.NewNop(), f)(next)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/x/complete", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Error("next handler ran for revoked key")
	}
}
