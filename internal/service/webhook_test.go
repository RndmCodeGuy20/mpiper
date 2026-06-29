package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/middleware"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"go.uber.org/zap"
)

// mockWebhookRepo implements repository.WebhookRepository for testing.
type mockWebhookRepo struct {
	created []repository.WebhookRegistration
}

func (m *mockWebhookRepo) Create(_ context.Context, reg repository.WebhookRegistration) error {
	m.created = append(m.created, reg)
	return nil
}
func (m *mockWebhookRepo) ListByUser(_ context.Context, _ string) ([]repository.WebhookRegistration, error) {
	return nil, nil
}
func (m *mockWebhookRepo) Delete(_ context.Context, _ uuid.UUID, _ string) error { return nil }

func ctxWithUser(userID string) context.Context {
	return context.WithValue(context.Background(), middleware.UserIDKey(), userID)
}

func init() {
	// Initialize config singleton for tests (32-byte encryption key).
	config.Init(config.EnvConfig{
		EncryptionKey: "01234567890123456789012345678901",
	})
}

func TestWebhookService_Create_ValidInput(t *testing.T) {
	repo := &mockWebhookRepo{}
	svc := NewWebhookService(repo, zap.NewNop())

	ctx := ctxWithUser("user-123")
	reg, err := svc.Create(ctx, "https://example.com/hook", "my-secret", []string{"job.done", "job.failed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg.UserID != "user-123" {
		t.Errorf("expected user-123, got %s", reg.UserID)
	}
	if len(repo.created) != 1 {
		t.Fatalf("expected 1 registration, got %d", len(repo.created))
	}
	// Secret should be encrypted (not the raw value)
	if repo.created[0].Secret == "my-secret" {
		t.Error("secret should be encrypted, not stored as plaintext")
	}
	if repo.created[0].Secret == "" {
		t.Error("encrypted secret should not be empty")
	}
}

func TestWebhookService_Create_InvalidURL(t *testing.T) {
	repo := &mockWebhookRepo{}
	svc := NewWebhookService(repo, zap.NewNop())

	ctx := ctxWithUser("user-123")
	_, err := svc.Create(ctx, "not-a-url", "secret", []string{"job.done"})
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestWebhookService_Create_InvalidEvent(t *testing.T) {
	repo := &mockWebhookRepo{}
	svc := NewWebhookService(repo, zap.NewNop())

	ctx := ctxWithUser("user-123")
	_, err := svc.Create(ctx, "https://example.com/hook", "secret", []string{"invalid.event"})
	if err == nil {
		t.Fatal("expected error for invalid event")
	}
}

func TestWebhookService_Create_EmptyEvents(t *testing.T) {
	repo := &mockWebhookRepo{}
	svc := NewWebhookService(repo, zap.NewNop())

	ctx := ctxWithUser("user-123")
	_, err := svc.Create(ctx, "https://example.com/hook", "secret", []string{})
	if err == nil {
		t.Fatal("expected error for empty events")
	}
}

func TestWebhookService_Create_EmptySecret(t *testing.T) {
	repo := &mockWebhookRepo{}
	svc := NewWebhookService(repo, zap.NewNop())

	ctx := ctxWithUser("user-123")
	_, err := svc.Create(ctx, "https://example.com/hook", "", []string{"job.done"})
	if err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestWebhookService_Create_NoUserInContext(t *testing.T) {
	repo := &mockWebhookRepo{}
	svc := NewWebhookService(repo, zap.NewNop())

	_, err := svc.Create(context.Background(), "https://example.com/hook", "secret", []string{"job.done"})
	if err == nil {
		t.Fatal("expected error for missing user in context")
	}
}
