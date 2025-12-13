package config

import (
	"os"
	"strconv"
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

type EnvConfig struct {
	Environment string
	Server      ServerConfig
	DB          DatabaseConfig
	Redis       RedisConfig
}

func GetEnvConfig(envFile string) (EnvConfig, error) {
	_ = godotenv.Load(envFile) // Load .env file if it exists

	host := os.Getenv("HOST")
	if host == "" {
		host = "0.0.0.0"
	}
	port, err := strconv.Atoi(os.Getenv("PORT"))
	if err != nil {
		port = 5010 // default port
	}
	dbHost := os.Getenv("DB_HOST")
	dbPort, err := strconv.Atoi(os.Getenv("DB_PORT"))
	if err != nil {
		dbPort = 5432 // default DB port
	}
	dbUser := os.Getenv("DB_USER")
	dbPassword := os.Getenv("DB_PASSWORD")
	dbName := os.Getenv("DB_NAME")
	env := os.Getenv("ENV")
	connectionString := os.Getenv("REDIS_CONNECTION_STRING")

	if env == "" {
		return EnvConfig{}, NewInitializationError("ENV is not set", nil)
	}

	if dbUser == "" {
		return EnvConfig{}, NewInitializationError("DB_USER is not set", nil)
	}

	if dbPassword == "" {
		return EnvConfig{}, NewInitializationError("DB_PASSWORD is not set", nil)
	}

	if dbName == "" {
		return EnvConfig{}, NewInitializationError("DB_NAME is not set", nil)
	}

	if connectionString == "" {
		return EnvConfig{}, NewInitializationError("REDIS_URL is not set", nil)
	}

	return EnvConfig{
		Environment: env,
		Server: ServerConfig{
			Port: port, // default port
			Host: host,
		},
		DB: DatabaseConfig{
			Host:     dbHost,
			Port:     dbPort, // default DB port
			User:     dbUser,
			Password: dbPassword,
			Name:     dbName,
			SSLMode:  "disable",
		},
		Redis: RedisConfig{
			ConnectionString: connectionString,
		},
	}, nil
}

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
