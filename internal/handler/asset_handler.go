package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"github.com/rndmcodeguy20/mpiper/internal/service"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
)

var allowedMIMETypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
	"video/mp4":       true,
	"video/quicktime": true,
}

func maxAssetSize() int64 {
	return config.MustGet().MaxAssetSizeBytes
}

type AssetHandler struct {
	svc    service.AssetService
	logger *zap.Logger
	m      *metrics.Metrics
}

func NewAssetHandler(svc service.AssetService, logger *zap.Logger, m *metrics.Metrics) *AssetHandler {
	return &AssetHandler{svc: svc, logger: logger, m: m}
}

func (h *AssetHandler) CreateAsset(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	tracer := otel.Tracer("mpiper-api")

	ctx, span := tracer.Start(r.Context(), "AssetHandler.CreateAsset")
	defer span.End()

	timeoutCtx, cancelFn := utils.GetTimeoutContext(ctx, 30)
	defer cancelFn()

	span.SetAttributes(
		attribute.String("http.method", r.Method),
		attribute.String("http.route", chi.RouteContext(r.Context()).RoutePattern()),
	)

	start := time.Now()
	defer func() {
		if h.m != nil {
			h.m.AssetUploadDuration.Record(ctx, time.Since(start).Seconds())
		}
	}()

	if h.m != nil {
		h.m.AssetUploadTotal.Add(ctx, 1)
	}

	var req models.UploadAssetRequest
	if err := utils.ParseJSON(r.Body, &req); err != nil {
		h.logger.Error("Failed to parse create asset request", zap.Error(err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "Invalid request payload")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "Invalid request payload"}, http.StatusBadRequest)
		return
	}

	span.SetAttributes(
		attribute.String("content_type", req.ContentType),
		attribute.Int64("content_length", req.Size),
	)

	if req.ContentType == "" {
		span.SetStatus(codes.Error, "ContentType is required")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "ContentType is required"}, http.StatusBadRequest)
		return
	}

	if !allowedMIMETypes[req.ContentType] {
		span.SetStatus(codes.Error, "unsupported content type")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "unsupported content type"}, http.StatusBadRequest)
		return
	}

	if req.Size > maxAssetSize() {
		span.SetStatus(codes.Error, "file too large")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "file exceeds maximum allowed size"}, http.StatusBadRequest)
		return
	}

	if err := timeoutCtx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Request cancelled")
		return
	}

	res, err := h.svc.CreateAsset(timeoutCtx, req)
	if err != nil {
		h.logger.Error("Failed to create asset", zap.Error(err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to create asset")
		if h.m != nil {
			h.m.AssetProcessingFailed.Add(timeoutCtx, 1)
		}
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "Failed to create asset", "error": err.Error()}, http.StatusInternalServerError)
		return
	}
	if res == nil {
		span.SetStatus(codes.Error, "Nil response from CreateAsset")
		if h.m != nil {
			h.m.AssetProcessingFailed.Add(timeoutCtx, 1)
		}
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "Internal server error"}, http.StatusInternalServerError)
		return
	}

	h.logger.Sugar().Infof("Asset created: %s", res.AssetID)
	span.SetAttributes(attribute.String("asset_id", res.AssetID))
	span.SetStatus(codes.Ok, "Asset created successfully")
	utils.RespondJSON(w, map[string]interface{}{"status": "success", "data": res}, http.StatusOK)
}

func (h *AssetHandler) MarkAssetUploaded(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(r.Context(), "AssetHandler.MarkAssetUploaded")
	defer span.End()

	timeoutCtx, cancelFn := utils.GetTimeoutContext(ctx, 15)
	defer cancelFn()

	assetID := chi.URLParam(r, "assetID")
	if assetID == "" {
		span.SetStatus(codes.Error, "Asset ID is required")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "asset id is required"}, http.StatusBadRequest)
		return
	}

	span.SetAttributes(attribute.String("asset_id", assetID))

	parsedID, err := uuid.Parse(assetID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Invalid asset ID format")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "invalid asset id"}, http.StatusBadRequest)
		return
	}

	if err := h.svc.MarkAssetUploaded(timeoutCtx, parsedID); err != nil {
		h.logger.Sugar().Errorf("Failed to mark asset uploaded: %v", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to mark asset uploaded")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "Failed to mark asset uploaded", "error": err.Error()}, http.StatusInternalServerError)
		return
	}

	span.SetStatus(codes.Ok, "Asset marked as uploaded")
	utils.RespondJSON(w, map[string]string{"status": "success", "message": "Asset marked as uploaded"}, http.StatusOK)
}
