package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// fakeOutboxRepo is an in-memory OutboxRepository for white-box relay tests.
type fakeOutboxRepo struct {
	pending     []models.OutboxEvent
	published   []int64
	incremented []int64
	failed      []int64
}

func (f *fakeOutboxRepo) InsertTx(_ context.Context, _ *sql.Tx, _ models.OutboxEvent) error {
	return nil
}
func (f *fakeOutboxRepo) FetchPendingBatch(_ context.Context, _ int) ([]models.OutboxEvent, error) {
	out := f.pending
	f.pending = nil // single tick
	return out, nil
}
func (f *fakeOutboxRepo) MarkPublished(_ context.Context, ids []int64) error {
	f.published = append(f.published, ids...)
	return nil
}
func (f *fakeOutboxRepo) IncrementAttempts(_ context.Context, id int64, _ string) error {
	f.incremented = append(f.incremented, id)
	return nil
}
func (f *fakeOutboxRepo) MarkFailed(_ context.Context, id int64, _ string) error {
	f.failed = append(f.failed, id)
	return nil
}
func (f *fakeOutboxRepo) DeletePublishedBefore(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeOutboxRepo) CountPending(_ context.Context) (int64, error) { return 0, nil }

// capturingQueue records the context handed to Enqueue.
type capturingQueue struct {
	gotCtx     context.Context
	gotPayload map[string]interface{}
}

func (q *capturingQueue) Enqueue(ctx context.Context, payload map[string]interface{}) (string, error) {
	q.gotCtx = ctx
	q.gotPayload = payload
	return "1-0", nil
}

func TestRelay_ReactivatesStoredTraceContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Build a known producer span context and serialize it as a traceparent.
	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(
		trace.ContextWithSpanContext(context.Background(), sc), carrier)
	tp := carrier.Get("traceparent")
	if tp == "" {
		t.Fatal("failed to build traceparent")
	}

	payload, _ := json.Marshal(map[string]interface{}{"asset_id": uuid.New().String()})
	repo := &fakeOutboxRepo{pending: []models.OutboxEvent{
		{ID: 7, Event: "asset_uploaded", Payload: payload, Traceparent: &tp, MaxAttempts: 5},
	}}
	q := &capturingQueue{}

	relay := NewRelay(repo, q, zap.NewNop(), nil, time.Second, 100)
	relay.tick(context.Background())

	if q.gotCtx == nil {
		t.Fatal("Enqueue was not called")
	}
	gotSC := trace.SpanContextFromContext(q.gotCtx)
	if !gotSC.IsValid() {
		t.Fatal("expected a valid span context passed to Enqueue")
	}
	if gotSC.TraceID() != traceID {
		t.Fatalf("trace id not propagated: want %s got %s", traceID, gotSC.TraceID())
	}
	if len(repo.published) != 1 || repo.published[0] != 7 {
		t.Fatalf("expected row 7 marked published, got %v", repo.published)
	}
}

func TestRelay_NoTraceparentStillPublishes(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	payload, _ := json.Marshal(map[string]interface{}{"asset_id": uuid.New().String()})
	repo := &fakeOutboxRepo{pending: []models.OutboxEvent{
		{ID: 9, Event: "asset_uploaded", Payload: payload, MaxAttempts: 5},
	}}
	q := &capturingQueue{}

	relay := NewRelay(repo, q, zap.NewNop(), nil, time.Second, 100)
	relay.tick(context.Background())

	if q.gotCtx == nil {
		t.Fatal("Enqueue was not called")
	}
	if len(repo.published) != 1 || repo.published[0] != 9 {
		t.Fatalf("expected row 9 marked published, got %v", repo.published)
	}
}
