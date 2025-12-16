package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"github.com/rndmcodeguy20/mpiper/internal/service"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
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
	tracer := otel.Tracer("mpiper-api")

	ctx, span := tracer.Start(r.Context(), "AssetHandler.CreateAsset")
	defer span.End()

	var req models.UploadAssetRequest
	err := utils.ParseJSON(r.Body, &req)
	if err != nil {
		h.logger.Error("Failed to parse create asset request", zap.Error(err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "Invalid request payload")
		utils.RespondJSON(
			w,
			map[string]string{"status": "error", "message": "Invalid request payload"},
			http.StatusBadRequest,
		)
		return
	}

	span.SetAttributes(
		attribute.String("content_type", req.ContentType),
		attribute.Int64("content_length", req.Size),
	)

	if req.ContentType == "" {
		h.logger.Error("ContentType is required")
		span.SetStatus(codes.Error, "ContentType is required")
		utils.RespondJSON(
			w,
			map[string]string{"status": "error", "message": "ContentType is required"},
			http.StatusBadRequest,
		)
		return
	}

	timeoutCtx, cancelFn := utils.GetTimeoutContext(ctx, 30)
	defer cancelFn()

	res, err := h.svc.CreateAsset(timeoutCtx, req)
	if err != nil {
		h.logger.Error("Failed to create asset", zap.Error(err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to create asset")
		utils.RespondJSON(
			w,
			map[string]string{"status": "error", "message": "Failed to create asset", "error": err.Error()},
			http.StatusInternalServerError,
		)
		return
	}

	span.SetAttributes(attribute.String("asset_id", res.AssetID))
	span.SetStatus(codes.Ok, "Asset created successfully")

	utils.RespondJSON(
		w,
		map[string]interface{}{"status": "success", "data": res},
		http.StatusOK,
	)
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
