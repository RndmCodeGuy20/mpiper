package database

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	applogger "github.com/rndmcodeguy20/mpiper/pkg/logger"
)

// NewPostgresDB creates a new PostgreSQL database connection.
func NewPostgresDB(databaseConfig config.DatabaseConfig) (*sqlx.DB, error) {
	cfg := config.MustGet()
	l := applogger.New(applogger.Config{
		ServiceName: cfg.Otel.ServiceName,
		Environment: cfg.Environment,
		Level:       applogger.ParseLevel(cfg.LogLevel),
	})

	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		databaseConfig.Host, databaseConfig.Port, databaseConfig.User,
		databaseConfig.Password, databaseConfig.Name, databaseConfig.SSLMode,
	)

	l.Sugar().Infof("Connecting to database: %s", databaseConfig.Name)

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	l.Info("Connected to database successfully")

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(300)
	db.SetConnMaxIdleTime(30)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}
