package postgres

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// ApplyMigrations applies every pending migration (embedded at compile time,
// see migrations.go) to the database at dsn. Used both by cmd/croupier at
// startup — nothing ever serves traffic against a schema that isn't fully up
// to date — and by internal/testdb to bring a freshly created test database
// up to the current schema before a test package runs. Uses its own plain
// database/sql connection, entirely separate from any pgxpool.Pool the
// caller has open — migration tooling and the application's own query path
// have no reason to share a connection pool.
func ApplyMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("postgres: opening database for migrations: %w", err)
	}
	defer db.Close()

	driver, err := migratepg.WithInstance(db, &migratepg.Config{})
	if err != nil {
		return fmt.Errorf("postgres: creating migration database driver: %w", err)
	}
	sourceDriver, err := iofs.New(MigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("postgres: creating migration source driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", sourceDriver, "postgres", driver)
	if err != nil {
		return fmt.Errorf("postgres: initializing migrator: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres: applying migrations: %w", err)
	}
	return nil
}
