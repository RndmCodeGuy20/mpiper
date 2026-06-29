package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

type contextKey string

const userIDKey contextKey = "user_id"

// AuthMiddleware validates the token, extracts the user ID, and injects it into the context.
func AuthMiddleware(l *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				l.Warn("Authorization header is empty")
				utils.WriteErrorResponse(w, errors.NewUnauthorizedError("Missing Authorization header", nil))
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				l.Warn("Invalid Authorization header format")
				utils.WriteErrorResponse(w, errors.NewUnauthorizedError("Invalid Authorization format", nil))
				return
			}

			token := parts[1]
			if token == "" {
				l.Warn("Token is empty")
				utils.WriteErrorResponse(w, errors.NewUnauthorizedError("Empty token", nil))
				return
			}

			userID, err := utils.DecryptToken(token, config.MustGet().EncryptionKey)
			if err != nil {
				l.Warn("Invalid or expired token", zap.Error(err))
				utils.WriteErrorResponse(w, errors.NewUnauthorizedError("Invalid token", err))
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetUserID extracts the user ID from context safely.
func GetUserID(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDKey).(string)
	return userID, ok
}

// UserIDKey returns the context key used for storing user_id. Exported for testing.
func UserIDKey() contextKey { return userIDKey }
