package main

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jhtohru/croupier/internal/postgres"
)

// runMigrations applies every pending migration (embedded at compile time —
// see internal/postgres/migrations.go) before anything else starts: nothing
// ever serves traffic against a schema that isn't fully up to date. Uses its
// own plain database/sql connection, entirely separate from the pgxpool.Pool
// the rest of the app uses — migration tooling and the application's own
// query path have no reason to share a connection pool.
func runMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migrate: opening database: %w", err)
	}
	defer db.Close()

	driver, err := migratepg.WithInstance(db, &migratepg.Config{})
	if err != nil {
		return fmt.Errorf("migrate: creating database driver: %w", err)
	}
	sourceDriver, err := iofs.New(postgres.MigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrate: creating source driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrate: initializing migrator: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: applying migrations: %w", err)
	}
	return nil
}
