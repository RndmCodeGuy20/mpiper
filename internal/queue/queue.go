package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	appErrors "github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
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
	logger  *utils.Logger
}

func NewRedisQueue(ctx context.Context, client *RedisClient, options RedisQueueOptions, logger *utils.Logger) *RedisQueue {
	// validate options
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
		options.MaxStreamLength = 10_000 // default max stream length
	}

	return &RedisQueue{
		ctx:     ctx,
		redis:   client,
		options: options,
		logger:  logger,
	}
}

func (rq *RedisQueue) Enqueue(ctx context.Context, payload map[string]interface{}) (string, error) {
	if rq.redis == nil {
		return "", appErrors.NewInternalServerError("Redis client is not initialized", fmt.Errorf("nil redis client"))
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", appErrors.NewInternalServerError("Failed to marshal payload", err)
	}

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
	}

	return rq.doXAddWithRetry(ctx, args)
}

func (rq *RedisQueue) doXAddWithRetry(ctx context.Context, args *redis.XAddArgs) (string, error) {
	var lastErr error

	for attempt := 0; attempt <= rq.options.MaxRetries; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, rq.options.ConnectionTimeOut)
		id, err := rq.redis.client.XAdd(attemptCtx, args).Result()
		cancel()

		if err == nil {
			return id, nil
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}

		lastErr = err

		time.Sleep(time.Duration(attempt+1) * rq.options.RetryInterval)
	}

	return "", appErrors.NewInternalServerError("Failed to add entry to Redis stream after retries", lastErr)
}

func (rq *RedisQueue) Close() error {
	return rq.redis.client.Close()
}
