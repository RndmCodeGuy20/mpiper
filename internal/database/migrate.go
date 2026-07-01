package database

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// destructiveMigrations lists schema migration versions that drop or alter
// existing user data. They are gated behind an explicit opt-in flag so a
// fresh database can bootstrap safely while production databases with
// existing data are protected from accidental data loss.
var destructiveMigrations = []uint{7, 8}

// RunMigrations applies all pending up-migrations. Idempotent — safe to call
// on every startup when AUTO_MIGRATE=true. Destructive migrations (versions
// 7 and 8) require allowDestructive=true; otherwise the function returns an
// error and refuses to apply anything.
func RunMigrations(db *sql.DB, allowDestructive bool) error {
	pending, err := destructiveMigrationsPending(db)
	if err != nil {
		return fmt.Errorf("check destructive migrations: %w", err)
	}
	if len(pending) > 0 && !allowDestructive {
		return fmt.Errorf(
			"destructive migrations %v are pending. Set MIGRATION_ALLOW_DESTRUCTIVE=true to apply them",
			pending,
		)
	}

	src, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}

// destructiveMigrationsPending returns the subset of destructiveMigrations
// that have not yet been recorded in schema_migrations. If the
// schema_migrations table does not exist yet (fresh database before migrate
// has been initialised), every destructive version is reported pending.
func destructiveMigrationsPending(db *sql.DB) ([]uint, error) {
	applied, err := loadAppliedMigrations(db)
	if err != nil {
		return nil, err
	}
	var pending []uint
	for _, v := range destructiveMigrations {
		if !applied[v] {
			pending = append(pending, v)
		}
	}
	return pending, nil
}

// loadAppliedMigrations returns the set of migration versions already
// recorded in schema_migrations. The table does not exist on a fresh
// database before migrate has been initialised; in that case we return an
// empty set so the caller treats all migrations as pending.
func loadAppliedMigrations(db *sql.DB) (map[uint]bool, error) {
	applied := make(map[uint]bool)
	rows, err := db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		if isUndefinedTableErr(err) {
			return applied, nil
		}
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[uint(v)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

// isUndefinedTableErr reports whether err is a PostgreSQL "undefined_table"
// (SQLSTATE 42P01), which is what postgres raises when schema_migrations
// does not yet exist on a fresh database.
func isUndefinedTableErr(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "42P01"
	}
	return false
}
