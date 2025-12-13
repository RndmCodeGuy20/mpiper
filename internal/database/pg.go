package database

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
)

// NewPostgresDB creates a new PostgresSQL database connection
func NewPostgresDB(databaseConfig config.DatabaseConfig) (*sqlx.DB, error) {
	pgLogger := utils.NewLogger()
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		databaseConfig.Host, databaseConfig.Port, databaseConfig.User, databaseConfig.Password, databaseConfig.Name, databaseConfig.SSLMode,
	)

	pgLogger.Sugar().Infof("Connecting to database: %s", databaseConfig.Name)

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	pgLogger.Info("Connected to database successfully")

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(300) // 5 minutes

	// Set the connection timeout
	db.SetConnMaxIdleTime(30) // 30 seconds

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}
