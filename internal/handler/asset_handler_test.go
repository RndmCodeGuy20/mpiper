package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	apperrors "github.com/rndmcodeguy20/mpiper/pkg/errors"
	"go.uber.org/zap"
)

// fakeAssetService implements service.AssetService for handler tests.
type fakeAssetService struct {
	markErr error
}

func (f *fakeAssetService) CreateAsset(_ context.Context, _ models.UploadAssetRequest) (*models.UploadAssetResponse, error) {
	return &models.UploadAssetResponse{}, nil
}

func (f *fakeAssetService) MarkAssetUploaded(_ context.Context, _ uuid.UUID) error {
	return f.markErr
}

// serveComplete mounts the handler on a chi router so {assetID} is parsed.
func serveComplete(h *AssetHandler, assetID string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/api/v1/assets/{assetID}/complete", h.MarkAssetUploaded)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+assetID+"/complete", nil)
	r.ServeHTTP(rec, req)
	return rec
}

func TestMarkAssetUploaded_CrossTenantReturns404(t *testing.T) {
	h := NewAssetHandler(&fakeAssetService{markErr: apperrors.NewNotFoundError("Asset not found", nil)}, zap.NewNop(), nil)
	rec := serveComplete(h, uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestMarkAssetUploaded_Success200(t *testing.T) {
	h := NewAssetHandler(&fakeAssetService{markErr: nil}, zap.NewNop(), nil)
	rec := serveComplete(h, uuid.New().String())
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestMarkAssetUploaded_InvalidUUID400(t *testing.T) {
	h := NewAssetHandler(&fakeAssetService{}, zap.NewNop(), nil)
	rec := serveComplete(h, "not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
