package middleware

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	apperrors "github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// AssetCounter reports how many assets a tenant owns (for quota enforcement).
type AssetCounter interface {
	CountByOwner(ctx context.Context, tenantID string) (int64, error)
}

// recordThrottle increments the throttle metric with a low-cardinality reason.
func recordThrottle(ctx context.Context, m *metrics.Metrics, reason string) {
	if m == nil || m.TenantThrottleTotal == nil {
		return
	}
	m.TenantThrottleTotal.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("reason", reason)))
}

// TenantRateLimitMiddleware applies a per-tenant token-bucket rate limit.
// Each tenant gets `rps` sustained requests/second with a burst of `burst`.
// Over-limit requests get 429 + Retry-After. Idle tenant limiters are evicted.
func TenantRateLimitMiddleware(l *zap.Logger, m *metrics.Metrics, rps float64, burst int) func(http.Handler) http.Handler {
	type entry struct {
		lim      *rate.Limiter
		lastSeen time.Time
	}
	var (
		mu      sync.Mutex
		tenants = make(map[string]*entry)
	)

	// Evict tenants not seen in the last 10 minutes to bound memory.
	go func() {
		for range time.Tick(time.Minute) {
			mu.Lock()
			for t, e := range tenants {
				if time.Since(e.lastSeen) > 10*time.Minute {
					delete(tenants, t)
				}
			}
			mu.Unlock()
		}
	}()

	getLimiter := func(tenant string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		e, ok := tenants[tenant]
		if !ok {
			e = &entry{lim: rate.NewLimiter(rate.Limit(rps), burst)}
			tenants[tenant] = e
		}
		e.lastSeen = time.Now()
		return e.lim
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant, ok := GetTenant(r.Context())
			if !ok || tenant == "" {
				// No tenant to key on (auth should have set it) — don't block.
				next.ServeHTTP(w, r)
				return
			}
			if !getLimiter(tenant).Allow() {
				recordThrottle(r.Context(), m, "rate_limit")
				l.Warn("tenant rate limit exceeded", zap.String("tenant", tenant))
				// Suggest a retry delay derived from the sustained rate.
				retryAfter := 1
				if rps > 0 {
					if ra := int(1.0 / rps); ra > retryAfter {
						retryAfter = ra
					}
				}
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				utils.WriteErrorResponse(w, apperrors.NewTooManyRequestsError("Rate limit exceeded", nil))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TenantQuotaMiddleware enforces a per-tenant asset-count quota. When quota is
// 0 the middleware is a no-op. A tenant at or above its quota gets 403.
func TenantQuotaMiddleware(l *zap.Logger, m *metrics.Metrics, counter AssetCounter, quota int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if quota <= 0 {
			return next // unlimited — skip the DB count entirely
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant, ok := GetTenant(r.Context())
			if !ok || tenant == "" {
				next.ServeHTTP(w, r)
				return
			}
			count, err := counter.CountByOwner(r.Context(), tenant)
			if err != nil {
				l.Error("quota count failed", zap.String("tenant", tenant), zap.Error(err))
				utils.WriteErrorResponse(w, apperrors.NewInternalServerError("Quota check failed", err))
				return
			}
			if count >= quota {
				recordThrottle(r.Context(), m, "quota")
				l.Warn("tenant asset quota exceeded", zap.String("tenant", tenant), zap.Int64("count", count), zap.Int64("quota", quota))
				utils.WriteErrorResponse(w, apperrors.NewForbiddenError(
					fmt.Sprintf("Asset quota exceeded (%d/%d)", count, quota), nil))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
