// Package testdb gives each integration-tagged test package its own
// Postgres database, dropped and recreated fresh (then migrated to the
// current schema) every time that package's tests run.
//
// This exists because of a real bug found the hard way (see TODO.md, Fase
// 12): every integration test in this project used to point at the same
// "croupier" database the docker-compose `app` service itself runs
// against. A live app instance's own OutboxWorker, polling that table on
// its own schedule, raced a test that assumed exclusive access to it,
// intermittently failing a concurrency assertion that had nothing wrong
// with it — the test's premise (nobody else is touching this table) was
// false, not the code it was testing. A separate, disposable database per
// test package makes that premise true by construction instead of by
// remembering to `docker compose stop app` first.
//
// Only internal/postgres itself needs to know how to reach a Postgres
// *server* (POSTGRES_HOST/PORT/USER/PASSWORD, same variables cmd/croupier's
// own Config reads — see .env.example) — never a full DSN, and never the
// same database name the app uses, so this package literally cannot resolve
// to the app's own database no matter what's set in the environment.
package testdb

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/jhtohru/croupier/internal/postgres"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func serverDSN(database string) string {
	host := getenv("POSTGRES_HOST", "localhost")
	port := getenv("POSTGRES_PORT", "5432")
	user := getenv("POSTGRES_USER", "croupier")
	password := getenv("POSTGRES_PASSWORD", "croupier")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, host, port, database)
}

// Postgres drops (if present) and recreates dbName on the Postgres server
// reachable via POSTGRES_HOST/PORT/USER/PASSWORD, applies every migration to
// it, and returns its connection string. Meant to be called once, from a
// TestMain, before that package's tests run — dbName should be unique to
// the calling package (e.g. "croupier_test_httpapi") so packages whose
// tests run as concurrent `go test` binaries (the default across different
// packages) never contend for the same database.
func Postgres(dbName string) (string, error) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, serverDSN("postgres"))
	if err != nil {
		return "", fmt.Errorf("testdb: connecting to the postgres maintenance database: %w", err)
	}
	defer admin.Close(ctx)

	ident := pgx.Identifier{dbName}.Sanitize()

	// A database can't be dropped while other connections are open against
	// it — a previous run's test binary that didn't shut down cleanly, most
	// likely.
	if _, err := admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName); err != nil {
		return "", fmt.Errorf("testdb: terminating existing connections to %s: %w", dbName, err)
	}
	// DROP/CREATE DATABASE can't run inside a transaction block; a plain
	// Exec on a freshly Connect'ed *pgx.Conn runs in Postgres's own
	// autocommit mode, not wrapped in one, so this is fine as-is.
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+ident); err != nil {
		return "", fmt.Errorf("testdb: dropping %s: %w", dbName, err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		return "", fmt.Errorf("testdb: creating %s: %w", dbName, err)
	}

	dsn := serverDSN(dbName)
	if err := postgres.ApplyMigrations(dsn); err != nil {
		return "", fmt.Errorf("testdb: applying migrations to %s: %w", dbName, err)
	}
	return dsn, nil
}
