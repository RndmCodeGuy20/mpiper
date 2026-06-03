package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	appErrors "github.com/rndmcodeguy20/mpiper/pkg/errors"
	"go.uber.org/zap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)


type RedisQueueOptions struct {
	QueueName         string
	MaxRetries        int
	RetryInterval     time.Duration
	ConnectionTimeOut time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	PoolSize          int
	AddTimestamp      bool
	AddSource         bool
	MaxStreamLength   int64
	EnableMetrics     bool
}

type Queue interface {
	Enqueue(ctx context.Context, payload map[string]interface{}) (string, error)
}

type RedisQueue struct {
	ctx     context.Context
	redis   *RedisClient
	options RedisQueueOptions
	logger  *zap.Logger
	m       *metrics.Metrics
}

func NewRedisQueue(ctx context.Context, client *RedisClient, options RedisQueueOptions, logger *zap.Logger, m *metrics.Metrics) *RedisQueue {
	if options.QueueName == "" {
		options.QueueName = "media:jobs"
	}
	if options.MaxRetries < 0 {
		options.MaxRetries = 3
	}
	if options.RetryInterval <= 0 {
		options.RetryInterval = 2 * time.Second
	}
	if options.ConnectionTimeOut <= 0 {
		options.ConnectionTimeOut = 2 * time.Second
	}
	if options.ReadTimeout <= 0 {
		options.ReadTimeout = 5 * time.Second
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = 5 * time.Second
	}
	if options.PoolSize <= 0 {
		options.PoolSize = 10
	}
	if options.MaxStreamLength <= 0 {
		options.MaxStreamLength = 10_000
	}

	rq := &RedisQueue{ctx: ctx, redis: client, options: options, logger: logger, m: m}

	if m != nil {
		streamName := options.QueueName
		_ = m.RegisterQueueDepthFunc(func(ctx context.Context) (int64, error) {
			return client.XLen(ctx, streamName)
		})
	}

	return rq
}

func (rq *RedisQueue) Enqueue(ctx context.Context, payload map[string]interface{}) (string, error) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "RedisQueue.Enqueue")
	defer span.End()

	start := time.Now()

	span.SetAttributes(
		attribute.String("queue.name", rq.options.QueueName),
		attribute.String("queue.type", "redis_stream"),
	)

	// Add payload metadata to span if available
	if jobID, ok := payload["job_id"].(int64); ok {
		span.SetAttributes(attribute.Int64("job_id", jobID))
	}
	if assetID, ok := payload["asset_id"].(string); ok {
		span.SetAttributes(attribute.String("asset_id", assetID))
	}
	if event, ok := payload["event"].(string); ok {
		span.SetAttributes(attribute.String("event", event))
	}

	if rq.redis == nil {
		err := appErrors.NewInternalServerError("Redis client is not initialized", fmt.Errorf("nil redis client"))
		span.RecordError(err)
		span.SetStatus(codes.Error, "Redis client not initialized")
		return "", err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to marshal payload")
		return "", appErrors.NewInternalServerError("Failed to marshal payload", err)
	}

	span.SetAttributes(attribute.Int("payload.size_bytes", len(body)))

	streamEntry := map[string]interface{}{
		"body": string(body),
	}

	args := &redis.XAddArgs{
		Stream: rq.options.QueueName,
		Values: streamEntry,
		ID:     "*",
	}

	if rq.options.MaxStreamLength > 0 {
		args.Approx = true
		args.MaxLen = rq.options.MaxStreamLength
		span.SetAttributes(attribute.Int64("queue.max_length", rq.options.MaxStreamLength))
	}

	id, err := rq.doXAddWithRetry(ctx, args)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to enqueue message")

		if rq.m != nil {
			attrs := []attribute.KeyValue{
				attribute.String("queue.name", rq.options.QueueName),
				attribute.String("error.type", "publish_failed"),
			}
			rq.m.QueueMessageFailed.Add(ctx, 1, metric.WithAttributes(attrs...))
		}

		return "", err
	}

	duration := time.Since(start).Seconds()
	attrs := []attribute.KeyValue{
		attribute.String("queue.name", rq.options.QueueName),
	}

	if rq.m != nil {
		rq.m.QueueMessagePublished.Add(ctx, 1, metric.WithAttributes(attrs...))
		rq.m.QueueProcessingLag.Record(ctx, duration, metric.WithAttributes(attrs...))
	}

	span.SetAttributes(attribute.String("message.id", id))
	span.SetStatus(codes.Ok, "Message enqueued successfully")
	return id, nil
}

func (rq *RedisQueue) doXAddWithRetry(ctx context.Context, args *redis.XAddArgs) (string, error) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "RedisQueue.doXAddWithRetry")
	defer span.End()

	span.SetAttributes(
		attribute.Int("max_retries", rq.options.MaxRetries),
		attribute.String("stream", args.Stream),
	)

	var lastErr error

	for attempt := 0; attempt <= rq.options.MaxRetries; attempt++ {
		span.AddEvent("Attempting Redis XADD", trace.WithAttributes(attribute.Int("attempt", attempt+1)))

		attemptCtx, cancel := context.WithTimeout(ctx, rq.options.ConnectionTimeOut)
		id, err := rq.redis.client.XAdd(attemptCtx, args).Result()
		cancel()

		if err == nil {
			span.SetAttributes(
				attribute.String("message.id", id),
				attribute.Int("attempts_used", attempt+1),
			)
			span.SetStatus(codes.Ok, "Message added to stream")
			return id, nil
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			span.RecordError(err)
			span.SetStatus(codes.Error, "Context cancelled or deadline exceeded")
			return "", err
		}

		lastErr = err
		span.AddEvent("Retry attempt failed",
			trace.WithAttributes(
				attribute.Int("attempt", attempt+1),
				attribute.String("error", err.Error()),
			),
		)

		if attempt < rq.options.MaxRetries {
			sleepDuration := time.Duration(attempt+1) * rq.options.RetryInterval
			span.AddEvent("Waiting before retry", trace.WithAttributes(attribute.String("duration", sleepDuration.String())))
			time.Sleep(sleepDuration)
		}
	}

	span.RecordError(lastErr)
	span.SetStatus(codes.Error, "All retry attempts exhausted")
	return "", appErrors.NewInternalServerError("Failed to add entry to Redis stream after retries", lastErr)
}

func (rq *RedisQueue) Close() error {
	return rq.redis.client.Close()
}
