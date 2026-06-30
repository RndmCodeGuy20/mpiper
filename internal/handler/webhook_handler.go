package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/service"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

type WebhookHandler struct {
	svc    service.WebhookService
	logger *zap.Logger
}

func NewWebhookHandler(svc service.WebhookService, logger *zap.Logger) *WebhookHandler {
	return &WebhookHandler{svc: svc, logger: logger}
}

type createWebhookRequest struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret"`
	Events []string `json:"events"`
}

func (h *WebhookHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createWebhookRequest
	if err := utils.ParseJSON(r.Body, &req); err != nil {
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "invalid request"}, http.StatusBadRequest)
		return
	}

	reg, err := h.svc.Create(r.Context(), req.URL, req.Secret, req.Events)
	if err != nil {
		h.logger.Warn("webhook create failed", zap.Error(err))
		utils.RespondJSON(w, map[string]string{"status": "error", "message": err.Error()}, http.StatusBadRequest)
		return
	}

	utils.RespondJSON(w, map[string]interface{}{"status": "success", "data": reg}, http.StatusCreated)
}

func (h *WebhookHandler) List(w http.ResponseWriter, r *http.Request) {
	regs, err := h.svc.List(r.Context())
	if err != nil {
		h.logger.Error("webhook list failed", zap.Error(err))
		utils.RespondJSON(w, map[string]string{"status": "error", "message": err.Error()}, http.StatusInternalServerError)
		return
	}
	utils.RespondJSON(w, map[string]interface{}{"status": "success", "data": regs}, http.StatusOK)
}

func (h *WebhookHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		utils.RespondJSON(w, map[string]string{"status": "error", "message": "invalid id"}, http.StatusBadRequest)
		return
	}

	if err := h.svc.Delete(r.Context(), id); err != nil {
		status := http.StatusInternalServerError
		if err.Error() == "not found" {
			status = http.StatusNotFound
		}
		utils.RespondJSON(w, map[string]string{"status": "error", "message": err.Error()}, status)
		return
	}

	utils.RespondJSON(w, map[string]string{"status": "success", "message": "deleted"}, http.StatusOK)
}
