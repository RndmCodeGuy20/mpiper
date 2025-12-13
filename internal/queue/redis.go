package queue

import (
	"github.com/redis/go-redis/v9"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
)

type RedisClient struct {
	cfg    *config.RedisConfig
	client *redis.Client
}

func MustGetRedisClient(cfg *config.RedisConfig, logger *utils.Logger) (*RedisClient, error) {
	var redisAddr string
	var redisPassword string
	var redisDB int
	if cfg.ConnectionString != "" {
		options, err := redis.ParseURL(cfg.ConnectionString)
		if err != nil {
			return nil, errors.NewConfigError("invalid redis URL", err)
		}

		logger.Info("Connecting to Redis using URL")

		redisAddr = options.Addr
		redisPassword = options.Password
		redisDB = options.DB
	}

	opts := &redis.Options{
		Addr:         redisAddr,
		Password:     redisPassword,
		DB:           redisDB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.ConnectTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		MinIdleConns: 2,
	}

	rdb := redis.NewClient(opts)

	return &RedisClient{
		cfg:    cfg,
		client: rdb,
	}, nil
}
