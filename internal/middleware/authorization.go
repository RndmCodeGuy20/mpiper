package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/repository"
	apperrors "github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

type contextKey string

const (
	tenantKey contextKey = "tenant_id"
	scopesKey contextKey = "scopes"
)

// APIKeyAuthenticator is the subset of the API key repository the auth
// middleware depends on. Defined here so tests can inject a fake without a DB.
type APIKeyAuthenticator interface {
	GetByHash(ctx context.Context, keyHash string) (*repository.APIKey, error)
}

// AuthMiddleware authenticates requests via a scoped API key presented as a
// Bearer credential. The presented key is hashed and looked up; missing,
// malformed, unknown, expired, or revoked keys are rejected with 401. On
// success the key's tenant id and scopes are injected into the request context.
func AuthMiddleware(l *zap.Logger, keys APIKeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				l.Warn("Authorization header is empty")
				utils.WriteErrorResponse(w, apperrors.NewUnauthorizedError("Missing Authorization header", nil))
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				l.Warn("Invalid Authorization header format")
				utils.WriteErrorResponse(w, apperrors.NewUnauthorizedError("Invalid Authorization format", nil))
				return
			}

			presented := parts[1]
			if presented == "" {
				l.Warn("API key is empty")
				utils.WriteErrorResponse(w, apperrors.NewUnauthorizedError("Empty API key", nil))
				return
			}

			// Validate the wire format before touching the DB. Avoids a lookup
			// for obviously-bad input and keeps error responses uniform.
			if _, err := utils.ParseAPIKey(presented); err != nil {
				l.Warn("Malformed API key")
				utils.WriteErrorResponse(w, apperrors.NewUnauthorizedError("Invalid API key", nil))
				return
			}

			key, err := keys.GetByHash(r.Context(), utils.HashAPIKey(presented))
			if err != nil {
				if errors.Is(err, repository.ErrAPIKeyNotFound) {
					l.Warn("Unknown API key")
					utils.WriteErrorResponse(w, apperrors.NewUnauthorizedError("Invalid API key", nil))
					return
				}
				l.Error("API key lookup failed", zap.Error(err))
				utils.WriteErrorResponse(w, apperrors.NewInternalServerError("Authentication failed", err))
				return
			}

			now := time.Now()
			if utils.IsRevoked(key.RevokedAt) {
				l.Warn("Revoked API key presented", zap.String("prefix", key.Prefix))
				utils.WriteErrorResponse(w, apperrors.NewUnauthorizedError("API key revoked", nil))
				return
			}
			if utils.IsExpired(key.ExpiresAt, now) {
				l.Warn("Expired API key presented", zap.String("prefix", key.Prefix))
				utils.WriteErrorResponse(w, apperrors.NewUnauthorizedError("API key expired", nil))
				return
			}

			ctx := context.WithValue(r.Context(), tenantKey, key.TenantID)
			ctx = context.WithValue(ctx, scopesKey, key.Scopes())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetTenant extracts the authenticated tenant id from context safely.
func GetTenant(ctx context.Context) (string, bool) {
	tenant, ok := ctx.Value(tenantKey).(string)
	return tenant, ok
}

// GetScopes extracts the authenticated key's scopes from context.
func GetScopes(ctx context.Context) []string {
	scopes, _ := ctx.Value(scopesKey).([]string)
	return scopes
}

// WithTenant returns a context carrying the given tenant id. Exported for
// tests and internal callers that need to set the tenant without going through
// the HTTP middleware.
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey, tenant)
}
