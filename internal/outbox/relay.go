package outbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/queue"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Relay polls the event_outbox table for pending rows and publishes them to Redis.
type Relay struct {
	repo     repository.OutboxRepository
	queue    queue.Queue
	logger   *zap.Logger
	m        *metrics.Metrics
	tracer   trace.Tracer
	interval time.Duration
	batch    int
}

func NewRelay(repo repository.OutboxRepository, q queue.Queue, logger *zap.Logger, m *metrics.Metrics, interval time.Duration, batch int) *Relay {
	return &Relay{repo: repo, queue: q, logger: logger, m: m, tracer: otel.Tracer("mpiper-api"), interval: interval, batch: batch}
}

// Start runs the relay loop until ctx is cancelled. It finishes the in-flight batch before returning.
func (r *Relay) Start(ctx context.Context) {
	r.logger.Info("outbox relay started", zap.Duration("interval", r.interval), zap.Int("batch", r.batch))
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("outbox relay stopped")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Relay) tick(ctx context.Context) {
	rows, err := r.repo.FetchPendingBatch(ctx, r.batch)
	if err != nil {
		r.logger.Error("outbox relay: fetch pending batch failed", zap.Error(err))
		return
	}
	if len(rows) == 0 {
		return
	}

	// Record relay lag from the oldest pending row.
	if r.m != nil {
		lag := time.Since(rows[0].CreatedAt).Seconds()
		r.m.OutboxRelayLagSeconds.Record(ctx, lag)
	}

	var publishedIDs []int64

	for _, row := range rows {
		var payload map[string]interface{}
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			r.logger.Error("outbox relay: unmarshal payload failed", zap.Int64("id", row.ID), zap.Error(err))
			_ = r.repo.MarkFailed(ctx, row.ID, err.Error())
			if r.m != nil {
				r.m.OutboxPublishFailures.Add(ctx, 1)
			}
			continue
		}

		// Re-activate the producer's trace context (captured when the row was
		// written) so the publish + enqueue spans rejoin the original request
		// trace instead of starting a disconnected root. tick() runs on a
		// background ticker context, so without this the trace would break here.
		publishCtx := ctx
		if row.Traceparent != nil && *row.Traceparent != "" {
			carrier := propagation.MapCarrier{"traceparent": *row.Traceparent}
			publishCtx = otel.GetTextMapPropagator().Extract(ctx, carrier)
		}
		publishCtx, span := r.tracer.Start(publishCtx, "outbox.publish")
		span.SetAttributes(
			attribute.Int64("outbox.row_id", row.ID),
			attribute.String("event", row.Event),
		)

		if _, err := r.queue.Enqueue(publishCtx, payload); err != nil {
			r.logger.Warn("outbox relay: enqueue failed", zap.Int64("id", row.ID), zap.Error(err))
			span.RecordError(err)
			span.SetStatus(codes.Error, "enqueue failed")
			span.End()
			_ = r.repo.IncrementAttempts(ctx, row.ID, err.Error())
			if row.Attempts+1 >= row.MaxAttempts {
				_ = r.repo.MarkFailed(ctx, row.ID, err.Error())
			}
			if r.m != nil {
				r.m.OutboxPublishFailures.Add(ctx, 1)
			}
			continue
		}
		span.End()

		publishedIDs = append(publishedIDs, row.ID)
	}

	if len(publishedIDs) > 0 {
		if err := r.repo.MarkPublished(ctx, publishedIDs); err != nil {
			r.logger.Error("outbox relay: mark published failed", zap.Error(err))
		}
		if r.m != nil {
			r.m.OutboxPublishedTotal.Add(ctx, int64(len(publishedIDs)))
		}
	}
}

// StartCleanup periodically deletes published outbox rows older than retention.
func (r *Relay) StartCleanup(ctx context.Context, retention time.Duration) {
	interval := retention / 24
	if interval < time.Minute {
		interval = time.Minute
	}
	r.logger.Info("outbox cleanup started", zap.Duration("retention", retention), zap.Duration("interval", interval))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("outbox cleanup stopped")
			return
		case <-ticker.C:
			deleted, err := r.repo.DeletePublishedBefore(ctx, time.Now().Add(-retention))
			if err != nil {
				r.logger.Error("outbox cleanup: delete failed", zap.Error(err))
				continue
			}
			if deleted > 0 {
				r.logger.Info("outbox cleanup: deleted old rows", zap.Int64("count", deleted))
			}
		}
	}
}
