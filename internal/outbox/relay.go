package outbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/queue"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"go.uber.org/zap"
)

// Relay polls the event_outbox table for pending rows and publishes them to Redis.
type Relay struct {
	repo     repository.OutboxRepository
	queue    queue.Queue
	logger   *zap.Logger
	m        *metrics.Metrics
	interval time.Duration
	batch    int
}

func NewRelay(repo repository.OutboxRepository, q queue.Queue, logger *zap.Logger, m *metrics.Metrics, interval time.Duration, batch int) *Relay {
	return &Relay{repo: repo, queue: q, logger: logger, m: m, interval: interval, batch: batch}
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

		if _, err := r.queue.Enqueue(ctx, payload); err != nil {
			r.logger.Warn("outbox relay: enqueue failed", zap.Int64("id", row.ID), zap.Error(err))
			_ = r.repo.IncrementAttempts(ctx, row.ID, err.Error())
			if row.Attempts+1 >= row.MaxAttempts {
				_ = r.repo.MarkFailed(ctx, row.ID, err.Error())
			}
			if r.m != nil {
				r.m.OutboxPublishFailures.Add(ctx, 1)
			}
			continue
		}

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
