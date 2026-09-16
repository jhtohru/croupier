//go:build integration

package postgres_test

import (
	"log"
	"os"
	"testing"

	"github.com/jhtohru/croupier/internal/testdb"
)

// TestMain gives this package's tests their own disposable Postgres
// database, freshly migrated, every run — see internal/testdb's doc comment
// for why. TEST_DATABASE_URL is set here rather than read from the
// environment: every test helper in this package already reads it that way,
// so nothing else in this file needs to change.
func TestMain(m *testing.M) {
	dsn, err := testdb.Postgres("croupier_test_postgres")
	if err != nil {
		log.Fatal(err)
	}
	os.Setenv("TEST_DATABASE_URL", dsn)
	os.Exit(m.Run())
}
