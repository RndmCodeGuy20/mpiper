package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"go.uber.org/zap"
)

type fakeIdemStore struct {
	outcome   repository.AcquireOutcome
	rec       *repository.IdempotencyRecord
	acquired  int
	complete  int
	released  int
	gotStatus int
	gotBody   []byte
}

func (f *fakeIdemStore) Acquire(_ context.Context, _, _, _ string, _ time.Duration) (repository.AcquireOutcome, *repository.IdempotencyRecord, error) {
	f.acquired++
	return f.outcome, f.rec, nil
}
func (f *fakeIdemStore) Complete(_ context.Context, _, _ string, status int, body []byte) error {
	f.complete++
	f.gotStatus = status
	f.gotBody = body
	return nil
}
func (f *fakeIdemStore) Release(_ context.Context, _, _ string) error {
	f.released++
	return nil
}

func runIdem(store *fakeIdemStore, key string, tenant string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	mw := IdempotencyMiddleware(zap.NewNop(), store, time.Hour)(handler)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage/presign", strings.NewReader(`{"x":1}`))
	if key != "" {
		req.Header.Set(IdempotencyKeyHeader, key)
	}
	if tenant != "" {
		req = req.WithContext(WithTenant(req.Context(), tenant))
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	return rec
}

func TestIdempotency_NoHeader_PassesThrough(t *testing.T) {
	store := &fakeIdemStore{outcome: repository.AcquireAcquired}
	ran := false
	rec := runIdem(store, "", "tenant-1", func(w http.ResponseWriter, r *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})
	if !ran {
		t.Error("handler should run when no Idempotency-Key present")
	}
	if store.acquired != 0 {
		t.Error("store should not be consulted without a key")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestIdempotency_Acquired_RunsHandlerAndStores(t *testing.T) {
	store := &fakeIdemStore{outcome: repository.AcquireAcquired}
	rec := runIdem(store, "key-1", "tenant-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"assetId":"abc"}`))
	})
	if store.complete != 1 {
		t.Errorf("Complete calls = %d, want 1", store.complete)
	}
	if store.gotStatus != http.StatusOK {
		t.Errorf("stored status = %d, want 200", store.gotStatus)
	}
	if string(store.gotBody) != `{"assetId":"abc"}` {
		t.Errorf("stored body = %q", string(store.gotBody))
	}
	if rec.Body.String() != `{"assetId":"abc"}` {
		t.Errorf("response body = %q", rec.Body.String())
	}
}

func TestIdempotency_Replay_ReturnsStoredResponse(t *testing.T) {
	store := &fakeIdemStore{
		outcome: repository.AcquireReplay,
		rec:     &repository.IdempotencyRecord{ResponseStatus: http.StatusOK, ResponseBody: []byte(`{"assetId":"abc"}`)},
	}
	ran := false
	rec := runIdem(store, "key-1", "tenant-1", func(w http.ResponseWriter, r *http.Request) {
		ran = true
	})
	if ran {
		t.Error("handler must NOT run on replay")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != `{"assetId":"abc"}` {
		t.Errorf("replayed body = %q", rec.Body.String())
	}
	if rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("replay should set Idempotent-Replayed header")
	}
}

func TestIdempotency_Mismatch_Returns422(t *testing.T) {
	store := &fakeIdemStore{outcome: repository.AcquireMismatch}
	ran := false
	rec := runIdem(store, "key-1", "tenant-1", func(w http.ResponseWriter, r *http.Request) { ran = true })
	if ran {
		t.Error("handler must NOT run on fingerprint mismatch")
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestIdempotency_InFlight_Returns409(t *testing.T) {
	store := &fakeIdemStore{outcome: repository.AcquireInFlight}
	rec := runIdem(store, "key-1", "tenant-1", func(w http.ResponseWriter, r *http.Request) {})
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestIdempotency_ServerError_ReleasesKey(t *testing.T) {
	store := &fakeIdemStore{outcome: repository.AcquireAcquired}
	runIdem(store, "key-1", "tenant-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if store.released != 1 {
		t.Errorf("Release calls = %d, want 1 (5xx must not be cached)", store.released)
	}
	if store.complete != 0 {
		t.Errorf("Complete calls = %d, want 0 for 5xx", store.complete)
	}
}
