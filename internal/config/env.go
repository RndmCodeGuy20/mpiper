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

type StorageConfig struct {
	Provider string
	GCS      GCSConfig
}

type EnvConfig struct {
	Environment        string
	Server             ServerConfig
	DB                 DatabaseConfig
	Redis              RedisConfig
	Otel               OtelConfig
	Storage            StorageConfig
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
			TLSInsecure:       strings.ToLower(os.Getenv("OTEL_TLS_INSECURE")) == "true",
			DeploymentEnv:     envOr("DEPLOYMENT_ENV", env),
			TraceSamplingRate: traceSamplingRate,
			ServiceName:       envOr("SERVICE_NAME", "mpiper-api"),
			ServiceVersion:    envOr("SERVICE_VERSION", "dev"),
		},
		Storage: StorageConfig{
			Provider: envOr("BUCKET_PROVIDER", "gcs"),
			GCS: GCSConfig{
				SAPath: os.Getenv("GCS_SA_PATH"),
			},
		},
		CORSAllowedOrigins: corsOrigins,
		LogLevel:           envOr("LOG_LEVEL", "INFO"),
		EncryptionKey:      encryptionKey,
		AutoMigrate:        strings.ToLower(os.Getenv("AUTO_MIGRATE")) == "true",
		MaxAssetSizeBytes:  maxAssetSize,
	}, nil
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
