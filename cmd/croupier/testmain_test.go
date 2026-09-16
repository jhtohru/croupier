//go:build integration

package main

import (
	"log"
	"os"
	"testing"

	"github.com/jhtohru/croupier/internal/testdb"
)

// TestMain gives this package's tests their own disposable Postgres
// database, freshly migrated, every run — see internal/testdb's doc comment
// for why.
func TestMain(m *testing.M) {
	dsn, err := testdb.Postgres("croupier_test_cmdcroupier")
	if err != nil {
		log.Fatal(err)
	}
	os.Setenv("TEST_DATABASE_URL", dsn)
	os.Exit(m.Run())
}
