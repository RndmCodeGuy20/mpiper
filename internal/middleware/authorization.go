package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

const userIDKey contextKey = "user_id"

// AuthMiddleware validates the token, extracts the user ID, and injects it into the context.
func AuthMiddleware(logger *utils.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				logger.Warn("Authorization header is empty")
				utils.WriteErrorResponse(w, errors.NewUnauthorizedError("Missing Authorization header", nil))
				return
			}

			// Expect "Bearer <token>"
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				logger.Warn("Invalid Authorization header format")
				utils.WriteErrorResponse(w, errors.NewUnauthorizedError("Invalid Authorization format", nil))
				return
			}

			token := parts[1]
			if token == "" {
				logger.Warn("Token is empty")
				utils.WriteErrorResponse(w, errors.NewUnauthorizedError("Empty token", nil))
				return
			}

			// Decrypt token to get user ID
			userID, err := utils.DecryptToken(token)
			if err != nil {
				logger.Warn("Invalid or expired token", zap.Error(err))
				utils.WriteErrorResponse(w, errors.NewUnauthorizedError("Invalid token", err))
				return
			}

			// Attach userID to context
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
