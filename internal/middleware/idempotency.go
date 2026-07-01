package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/repository"
	apperrors "github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

// IdempotencyKeyHeader is the request header clients send to make a mutating
// request safely retryable.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyStore is the subset of the idempotency repository the middleware
// needs. Defined here so tests can inject a fake.
type IdempotencyStore interface {
	Acquire(ctx context.Context, tenant, key, fingerprint string, ttl time.Duration) (repository.AcquireOutcome, *repository.IdempotencyRecord, error)
	Complete(ctx context.Context, tenant, key string, status int, body []byte) error
	Release(ctx context.Context, tenant, key string) error
}

// captureWriter records the status code and body while still writing through to
// the underlying ResponseWriter, so the response can be persisted for replay.
type captureWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (c *captureWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.buf.Write(b)
	return c.ResponseWriter.Write(b)
}

// IdempotencyMiddleware provides Stripe-style idempotency for mutating requests.
// When an Idempotency-Key header is present (and a tenant is authenticated):
//   - the first request executes the handler and its response is stored;
//   - a replay with the same key + same request returns the stored response;
//   - the same key with a different request body returns 422;
//   - a concurrent duplicate still in flight returns 409.
//
// Requests without the header pass straight through unchanged. The middleware
// must run AFTER AuthMiddleware so the tenant is in context.
func IdempotencyMiddleware(l *zap.Logger, store IdempotencyStore, ttl time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			tenant, ok := GetTenant(r.Context())
			if !ok || tenant == "" {
				// No tenant (auth should have set it) — nothing to scope by.
				next.ServeHTTP(w, r)
				return
			}

			// Buffer the body so we can fingerprint it AND let the handler read
			// it again.
			var body []byte
			if r.Body != nil {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					utils.WriteErrorResponse(w, apperrors.NewBadRequestError("Failed to read request body", err))
					return
				}
				body = b
				_ = r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			fingerprint := fingerprintRequest(r.Method, r.URL.Path, body)

			outcome, rec, err := store.Acquire(r.Context(), tenant, key, fingerprint, ttl)
			if err != nil {
				l.Error("idempotency acquire failed", zap.Error(err))
				utils.WriteErrorResponse(w, apperrors.NewInternalServerError("Idempotency check failed", err))
				return
			}

			switch outcome {
			case repository.AcquireReplay:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotent-Replayed", "true")
				status := rec.ResponseStatus
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				_, _ = w.Write(rec.ResponseBody)
				return
			case repository.AcquireMismatch:
				utils.WriteErrorResponse(w, apperrors.NewUnprocessableEntityError(
					"Idempotency-Key reused for a different request", nil))
				return
			case repository.AcquireInFlight:
				utils.WriteErrorResponse(w, apperrors.NewConflictError(
					"A request with this Idempotency-Key is already in progress", nil))
				return
			}

			// AcquireAcquired — run the handler, capturing the response.
			cw := &captureWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(cw, r)

			// Cache only non-server-error responses. On 5xx, release the key so
			// the client may retry the operation.
			if cw.status >= 500 {
				if err := store.Release(r.Context(), tenant, key); err != nil {
					l.Warn("idempotency release failed", zap.Error(err))
				}
				return
			}
			if err := store.Complete(r.Context(), tenant, key, cw.status, cw.buf.Bytes()); err != nil {
				// The client already received a valid response; just log.
				l.Warn("idempotency complete failed", zap.Error(err))
			}
		})
	}
}

func fingerprintRequest(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
