package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func serveWithTenant(mw func(http.Handler) http.Handler, tenant string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	gate := mw(handler)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/presign", nil)
	if tenant != "" {
		req = req.WithContext(WithTenant(req.Context(), tenant))
	}
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, req)
	return rec
}

func okHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
}

func TestTenantRateLimit_ThrottlesPerTenant(t *testing.T) {
	// rps=1, burst=1: the first request passes, an immediate second is throttled.
	mw := TenantRateLimitMiddleware(zap.NewNop(), nil, 1, 1)

	if rec := serveWithTenant(mw, "tenant-a", okHandler()); rec.Code != http.StatusOK {
		t.Fatalf("first request for tenant-a = %d, want 200", rec.Code)
	}
	rec := serveWithTenant(mw, "tenant-a", okHandler())
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("second request for tenant-a = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 response should carry a Retry-After header")
	}

	// A different tenant has its own bucket and is unaffected.
	if rec := serveWithTenant(mw, "tenant-b", okHandler()); rec.Code != http.StatusOK {
		t.Errorf("first request for tenant-b = %d, want 200 (per-tenant isolation)", rec.Code)
	}
}

func TestTenantRateLimit_NoTenantPassesThrough(t *testing.T) {
	mw := TenantRateLimitMiddleware(zap.NewNop(), nil, 1, 1)
	// No tenant in context -> not blocked (auth would normally have set it).
	for i := 0; i < 3; i++ {
		if rec := serveWithTenant(mw, "", okHandler()); rec.Code != http.StatusOK {
			t.Fatalf("request %d without tenant = %d, want 200", i, rec.Code)
		}
	}
}

// fakeCounter implements AssetCounter.
type fakeCounter struct {
	count int64
	err   error
}

func (f *fakeCounter) CountByOwner(_ context.Context, _ string) (int64, error) {
	return f.count, f.err
}

func TestTenantQuota_BlocksOverQuota(t *testing.T) {
	mw := TenantQuotaMiddleware(zap.NewNop(), nil, &fakeCounter{count: 5}, 5)
	rec := serveWithTenant(mw, "tenant-a", okHandler())
	if rec.Code != http.StatusForbidden {
		t.Errorf("at-quota request = %d, want 403", rec.Code)
	}
}

func TestTenantQuota_AllowsUnderQuota(t *testing.T) {
	mw := TenantQuotaMiddleware(zap.NewNop(), nil, &fakeCounter{count: 2}, 5)
	rec := serveWithTenant(mw, "tenant-a", okHandler())
	if rec.Code != http.StatusOK {
		t.Errorf("under-quota request = %d, want 200", rec.Code)
	}
}

func TestTenantQuota_ZeroMeansUnlimited(t *testing.T) {
	// quota=0 -> middleware is a no-op and never calls the counter.
	counter := &fakeCounter{err: errors.New("should not be called")}
	mw := TenantQuotaMiddleware(zap.NewNop(), nil, counter, 0)
	rec := serveWithTenant(mw, "tenant-a", okHandler())
	if rec.Code != http.StatusOK {
		t.Errorf("unlimited quota request = %d, want 200", rec.Code)
	}
}

func TestTenantQuota_CounterErrorReturns500(t *testing.T) {
	mw := TenantQuotaMiddleware(zap.NewNop(), nil, &fakeCounter{err: errors.New("db down")}, 5)
	rec := serveWithTenant(mw, "tenant-a", okHandler())
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("counter error = %d, want 500", rec.Code)
	}
}
