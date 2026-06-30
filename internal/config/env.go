package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/rndmcodeguy20/mpiper/pkg/errors"
)

type EnvConfigError struct {
	*errors.BaseError
}

func NewInitializationError(message string, cause error) *EnvConfigError {
	return &EnvConfigError{
		BaseError: &errors.BaseError{
			Message: message,
			Code:    "ENV_CONFIG_INITIALIZATION_ERROR",
			Cause:   cause,
		},
	}
}

type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

type ServerConfig struct {
	Port int
	Host string
}

type RedisConfig struct {
	ConnectionString string
	PoolSize         int
	ConnectTimeout   time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
}

type OtelConfig struct {
	Endpoint          string
	TLSInsecure       bool
	DeploymentEnv     string
	TraceSamplingRate float64
	ServiceName       string
	ServiceVersion    string
}

type GCSConfig struct {
	SAPath string
}

type S3Config struct {
	Bucket          string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	EndpointURL     string // optional — internal/server-side endpoint for MinIO / S3-compatible stores
	// PublicEndpointURL is the client-facing endpoint used to sign presigned
	// URLs and build public URLs (e.g. http://localhost:9000). When empty,
	// EndpointURL is used for both. Set this when internal services reach the
	// store by a private host (e.g. minio:9000) that external clients cannot.
	PublicEndpointURL string
}

type StorageConfig struct {
	Provider string
	Bucket   string
	GCS      GCSConfig
	S3       S3Config
}

type OutboxConfig struct {
	RelayInterval time.Duration
	RelayBatch    int
	MaxAttempts   int
	Retention     time.Duration
}

type WebhookConfig struct {
	PollInterval time.Duration
	BatchSize    int
	Timeout      time.Duration
	MaxAttempts  int
	Retention    time.Duration
	Concurrency  int
}

type EnvConfig struct {
	Environment        string
	Server             ServerConfig
	DB                 DatabaseConfig
	Redis              RedisConfig
	Otel               OtelConfig
	Storage            StorageConfig
	Outbox             OutboxConfig
	Webhook            WebhookConfig
	CORSAllowedOrigins []string
	LogLevel           string
	EncryptionKey      string
	AutoMigrate        bool
	MaxAssetSizeBytes  int64
}

// --- Singleton ---

var (
	instance *EnvConfig
	once     sync.Once
)

// Init stores cfg as the package-level singleton. Must be called once at startup before MustGet.
func Init(cfg EnvConfig) {
	once.Do(func() { instance = &cfg })
}

// MustGet returns the singleton config. Panics if Init has not been called.
func MustGet() *EnvConfig {
	if instance == nil {
		panic("config: MustGet called before Init — call config.Init(cfg) at startup")
	}
	return instance
}

// --- Loading ---

func GetEnvConfig(envFile string) (EnvConfig, error) {
	_ = godotenv.Load(envFile)

	host := envOr("HOST", "0.0.0.0")

	port, err := strconv.Atoi(os.Getenv("PORT"))
	if err != nil {
		port = 5010
	}

	dbPort, err := strconv.Atoi(os.Getenv("DB_PORT"))
	if err != nil {
		dbPort = 5432
	}

	env := os.Getenv("ENV")
	if env == "" {
		return EnvConfig{}, NewInitializationError("ENV is not set", nil)
	}

	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		return EnvConfig{}, NewInitializationError("DB_USER is not set", nil)
	}

	dbPassword := os.Getenv("DB_PASSWORD")
	if dbPassword == "" {
		return EnvConfig{}, NewInitializationError("DB_PASSWORD is not set", nil)
	}

	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		return EnvConfig{}, NewInitializationError("DB_NAME is not set", nil)
	}

	redisConnStr := os.Getenv("REDIS_CONNECTION_STRING")
	if redisConnStr == "" {
		return EnvConfig{}, NewInitializationError("REDIS_CONNECTION_STRING is not set", nil)
	}

	encryptionKey := os.Getenv("ENCRYPTION_KEY")
	if encryptionKey == "" {
		return EnvConfig{}, NewInitializationError("ENCRYPTION_KEY is not set", nil)
	}
	if len(encryptionKey) != 32 {
		return EnvConfig{}, NewInitializationError(
			fmt.Sprintf("ENCRYPTION_KEY must be exactly 32 bytes for AES-256, got %d", len(encryptionKey)), nil,
		)
	}

	traceSamplingRate := 0.1
	if raw := os.Getenv("TRACE_SAMPLING_RATE"); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			traceSamplingRate = parsed
		}
	}

	maxAssetSize := int64(500 * 1024 * 1024)
	if raw := os.Getenv("MAX_ASSET_SIZE_BYTES"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			maxAssetSize = n
		}
	}

	corsOrigins := []string{"http://localhost:5173"}
	if raw := os.Getenv("CORS_ALLOWED_ORIGINS"); raw != "" {
		corsOrigins = strings.Split(raw, ",")
	}

	outboxRelayInterval := time.Second
	if raw := os.Getenv("OUTBOX_RELAY_INTERVAL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			outboxRelayInterval = d
		}
	}
	outboxRelayBatch := 100
	if raw := os.Getenv("OUTBOX_RELAY_BATCH"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			outboxRelayBatch = n
		}
	}
	outboxMaxAttempts := 5
	if raw := os.Getenv("OUTBOX_MAX_ATTEMPTS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			outboxMaxAttempts = n
		}
	}
	outboxRetention := 168 * time.Hour
	if raw := os.Getenv("OUTBOX_RETENTION"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			outboxRetention = d
		}
	}

	webhookPollInterval := 2 * time.Second
	if raw := os.Getenv("WEBHOOK_POLL_INTERVAL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			webhookPollInterval = d
		}
	}
	webhookBatchSize := 50
	if raw := os.Getenv("WEBHOOK_BATCH_SIZE"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			webhookBatchSize = n
		}
	}
	webhookTimeout := 10 * time.Second
	if raw := os.Getenv("WEBHOOK_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			webhookTimeout = d
		}
	}
	webhookMaxAttempts := 5
	if raw := os.Getenv("WEBHOOK_MAX_ATTEMPTS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			webhookMaxAttempts = n
		}
	}
	webhookRetention := 168 * time.Hour
	if raw := os.Getenv("WEBHOOK_RETENTION"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			webhookRetention = d
		}
	}
	webhookConcurrency := 10
	if raw := os.Getenv("WEBHOOK_CONCURRENCY"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			webhookConcurrency = n
		}
	}

	return EnvConfig{
		Environment: env,
		Server: ServerConfig{
			Port: port,
			Host: host,
		},
		DB: DatabaseConfig{
			Host:     envOr("DB_HOST", "localhost"),
			Port:     dbPort,
			User:     dbUser,
			Password: dbPassword,
			Name:     dbName,
			SSLMode:  "disable",
		},
		Redis: RedisConfig{
			ConnectionString: redisConnStr,
		},
		Otel: OtelConfig{
			Endpoint:          envOr("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317"),
			TLSInsecure:       parseTLSInsecure(os.Getenv("OTEL_TLS_INSECURE")),
			DeploymentEnv:     envOr("DEPLOYMENT_ENV", env),
			TraceSamplingRate: traceSamplingRate,
			ServiceName:       envOr("SERVICE_NAME", "mpiper-api"),
			ServiceVersion:    envOr("SERVICE_VERSION", "dev"),
		},
		Storage: StorageConfig{
			Provider: envOr("BUCKET_PROVIDER", "gcs"),
			Bucket:   envOr("BUCKET_NAME", "mpiper"),
			GCS: GCSConfig{
				SAPath: os.Getenv("GCS_SA_PATH"),
			},
			S3: S3Config{
				Bucket:            envOr("S3_BUCKET_NAME", envOr("BUCKET_NAME", "mpiper")),
				Region:            os.Getenv("S3_REGION"),
				AccessKeyID:       os.Getenv("S3_ACCESS_KEY_ID"),
				SecretAccessKey:   os.Getenv("S3_SECRET_ACCESS_KEY"),
				EndpointURL:       os.Getenv("S3_ENDPOINT_URL"),
				PublicEndpointURL: os.Getenv("S3_PUBLIC_ENDPOINT_URL"),
			},
		},
		CORSAllowedOrigins: corsOrigins,
		LogLevel:           envOr("LOG_LEVEL", "INFO"),
		EncryptionKey:      encryptionKey,
		AutoMigrate:        strings.ToLower(os.Getenv("AUTO_MIGRATE")) == "true",
		MaxAssetSizeBytes:  maxAssetSize,
		Outbox: OutboxConfig{
			RelayInterval: outboxRelayInterval,
			RelayBatch:    outboxRelayBatch,
			MaxAttempts:   outboxMaxAttempts,
			Retention:     outboxRetention,
		},
		Webhook: WebhookConfig{
			PollInterval: webhookPollInterval,
			BatchSize:    webhookBatchSize,
			Timeout:      webhookTimeout,
			MaxAttempts:  webhookMaxAttempts,
			Retention:    webhookRetention,
			Concurrency:  webhookConcurrency,
		},
	}, nil
}

// parseTLSInsecure defaults to plaintext (true); TLS is opt-in via OTEL_TLS_INSECURE=false.
// The bundled collector speaks plaintext gRPC, so secure-by-default would silently drop all telemetry.
func parseTLSInsecure(raw string) bool {
	return strings.ToLower(strings.TrimSpace(raw)) != "false"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// --- Environment type helpers ---

type Environment string

const (
	Development Environment = "development"
	Staging     Environment = "staging"
	Production  Environment = "production"
)

func ToEnvironment(env string) Environment {
	switch env {
	case "production":
		return Production
	case "staging":
		return Staging
	case "development":
		return Development
	default:
		return Development
	}
}

func InitializeConfig(env Environment) (EnvConfig, error) {
	switch env {
	case Production:
		return GetEnvConfig(".env")
	case Staging:
		return GetEnvConfig(".env.staging")
	case Development:
		return GetEnvConfig(".env.local")
	default:
		return GetEnvConfig(".env")
	}
}
