package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
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

	// Record request size
	//if r.ContentLength > 0 && metrics.HTTPRequestSize != nil {
	//	attrs := []attribute.KeyValue{
	//		attribute.String("http.method", r.Method),
	//		attribute.String("http.route", r.URL.Path),
	//	}
	//	metrics.HTTPRequestSize.Record(ctx, r.ContentLength, metric.WithAttributes(attrs...))
	//}

	span.SetAttributes(
		attribute.String("http.method", r.Method),
		attribute.String("http.route", chi.RouteContext(r.Context()).RoutePattern()),
	)

	start := time.Now()
	metrics.AssetUploadTotal.Add(ctx, 1)

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
	metrics.AssetUploadDuration.Record(ctx, time.Since(start).Seconds())

	if err != nil {
		h.logger.Error("Failed to create asset", zap.Error(err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to create asset")
		metrics.AssetProcessingFailed.Add(ctx, 1)
		utils.RespondJSON(
			w,
			map[string]string{"status": "error", "message": "Failed to create asset", "error": err.Error()},
			http.StatusInternalServerError,
		)
		return
	}
	if res == nil {
		h.logger.Error("CreateAsset returned nil response without error")
		span.SetStatus(codes.Error, "Nil response from CreateAsset")
		metrics.AssetProcessingSuccess.Add(ctx, 1)
		utils.RespondJSON(
			w,
			map[string]string{"status": "error", "message": "Internal server error: nil response from CreateAsset"},
			http.StatusInternalServerError,
		)
		return
	}

	h.logger.Sugar().Infof("Asset created: %s", res.AssetID)

	span.SetAttributes(attribute.String("asset_id", res.AssetID))
	span.SetStatus(codes.Ok, "Asset created successfully")

	utils.RespondJSON(
		w,
		map[string]interface{}{"status": "success", "data": res},
		http.StatusOK,
	)
}

func (h *AssetHandler) MarkAssetUploaded(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(r.Context(), "AssetHandler.MarkAssetUploaded")
	defer span.End()

	assetID := chi.URLParam(r, "assetID")
	if assetID == "" {
		span.SetStatus(codes.Error, "Asset ID is required")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "asset id is required"}, http.StatusBadRequest)
		return
	}

	span.SetAttributes(attribute.String("asset_id", assetID))

	timeoutCtx, cancelFn := utils.GetTimeoutContext(ctx, 15)
	defer cancelFn()

	parsedID, err := uuid.Parse(assetID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Invalid asset ID format")
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "invalid asset id"}, http.StatusBadRequest)
		return
	}
	err = h.svc.MarkAssetUploaded(timeoutCtx, parsedID)
	if err != nil {
		h.logger.Sugar().Errorf("Failed to mark asset uploaded: %v", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to mark asset uploaded")
		utils.RespondJSON(
			w,
			map[string]string{"status": "error", "message": "Failed to mark asset uploaded", "error": err.Error()},
			http.StatusInternalServerError,
		)
		return
	}

	span.SetStatus(codes.Ok, "Asset marked as uploaded")
	utils.RespondJSON(
		w,
		map[string]string{"status": "success", "message": "Asset marked as uploaded"},
		http.StatusOK,
	)
}
