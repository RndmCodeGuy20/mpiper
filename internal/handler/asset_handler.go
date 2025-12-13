package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"github.com/rndmcodeguy20/mpiper/internal/service"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
)

type AssetHandler struct {
	svc    service.AssetService
	logger *utils.Logger
}

func NewAssetHandler(svc service.AssetService, logger *utils.Logger) *AssetHandler {
	return &AssetHandler{
		svc:    svc,
		logger: logger,
	}
}

func (h *AssetHandler) CreateAsset(w http.ResponseWriter, r *http.Request) {
	{
		var req models.UploadAssetRequest
		err := utils.ParseJSON(r.Body, &req)
		if err != nil {
			utils.RespondJSON(
				w,
				map[string]string{"status": "error", "message": "Invalid request payload"},
				http.StatusBadRequest,
			)
			return
		}

		if req.ContentType == "" {
			utils.RespondJSON(
				w,
				map[string]string{"status": "error", "message": "ContentType is required"},
				http.StatusBadRequest,
			)
			return
		}

		timeoutCtx, cancelFn := utils.GetTimeoutContext(r.Context(), 30)
		defer cancelFn()

		res, err := h.svc.CreateAsset(timeoutCtx, req)

		if err != nil {
			h.logger.Sugar().Errorf("Failed to create asset: %v", err)
			utils.RespondJSON(
				w,
				map[string]string{"status": "error", "message": "Failed to create asset", "error": err.Error()},
				http.StatusInternalServerError,
			)
			return
		}

		utils.RespondJSON(
			w,
			map[string]interface{}{"status": "success", "data": res},
			http.StatusOK,
		)
	}
}

func (h *AssetHandler) MarkAssetUploaded(w http.ResponseWriter, r *http.Request) {
	assetID := chi.URLParam(r, "assetID")
	if assetID == "" {
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "asset id is required"}, http.StatusBadRequest)
		return
	}

	timeoutCtx, cancelFn := utils.GetTimeoutContext(r.Context(), 15)
	defer cancelFn()

	parsedID, err := uuid.Parse(assetID)
	if err != nil {
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "invalid asset id"}, http.StatusBadRequest)
		return
	}
	err = h.svc.MarkAssetUploaded(timeoutCtx, parsedID)
	if err != nil {
		h.logger.Sugar().Errorf("Failed to mark asset uploaded: %v", err)
		utils.RespondJSON(
			w,
			map[string]string{"status": "error", "message": "Failed to mark asset uploaded", "error": err.Error()},
			http.StatusInternalServerError,
		)
		return
	}

	utils.RespondJSON(
		w,
		map[string]string{"status": "success", "message": "Asset marked as uploaded"},
		http.StatusOK,
	)
}
