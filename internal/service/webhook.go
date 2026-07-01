package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/middleware"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	apperrors "github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

var validEvents = map[string]bool{
	"job.starting": true,
	"job.started":  true,
	"job.done":     true,
	"job.failed":   true,
}

type WebhookService interface {
	Create(ctx context.Context, reqURL, secret string, events []string) (*repository.WebhookRegistration, error)
	List(ctx context.Context) ([]repository.WebhookRegistration, error)
	Delete(ctx context.Context, id uuid.UUID) error
}

type webhookService struct {
	repo   repository.WebhookRepository
	logger *zap.Logger
}

func NewWebhookService(repo repository.WebhookRepository, logger *zap.Logger) WebhookService {
	return &webhookService{repo: repo, logger: logger}
}

func (s *webhookService) Create(ctx context.Context, reqURL, secret string, events []string) (*repository.WebhookRegistration, error) {
	if _, err := url.ParseRequestURI(reqURL); err != nil {
		return nil, apperrors.NewBadRequestError("invalid url", err)
	}
	if secret == "" {
		return nil, apperrors.NewBadRequestError("secret is required", nil)
	}
	if len(events) == 0 {
		return nil, apperrors.NewBadRequestError("at least one event is required", nil)
	}
	for _, e := range events {
		if !validEvents[e] {
			return nil, apperrors.NewBadRequestError(fmt.Sprintf("invalid event: %s", e), nil)
		}
	}

	userID, ok := middleware.GetTenant(ctx)
	if !ok || userID == "" {
		return nil, apperrors.NewUnauthorizedError("Tenant not found", nil)
	}

	encryptedSecret, err := utils.GenerateToken(secret, config.MustGet().WebhookEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt secret: %w", err)
	}

	reg := repository.WebhookRegistration{
		ID:     uuid.New(),
		UserID: userID,
		URL:    reqURL,
		Secret: encryptedSecret,
		Events: events,
	}

	if err := s.repo.Create(ctx, reg); err != nil {
		return nil, err
	}

	return &reg, nil
}

func (s *webhookService) List(ctx context.Context) ([]repository.WebhookRegistration, error) {
	userID, ok := middleware.GetTenant(ctx)
	if !ok || userID == "" {
		return nil, apperrors.NewUnauthorizedError("Tenant not found", nil)
	}
	return s.repo.ListByUser(ctx, userID)
}

func (s *webhookService) Delete(ctx context.Context, id uuid.UUID) error {
	userID, ok := middleware.GetTenant(ctx)
	if !ok || userID == "" {
		return apperrors.NewUnauthorizedError("Tenant not found", nil)
	}
	if err := s.repo.Delete(ctx, id, userID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperrors.NewNotFoundError("Webhook not found", nil)
		}
		return err
	}
	return nil
}
