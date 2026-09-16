//go:build integration

package sqs

import (
	"log"
	"os"
	"testing"

	"github.com/jhtohru/croupier/internal/testdb"
)

// TestMain gives this package's tests their own disposable Postgres
// database, freshly migrated, every run — see internal/testdb's doc comment
// for why. This only covers the Postgres side: the SQS side of isolation is
// createTestFIFOQueue's job (see integration_test.go), a disposable LocalStack
// queue per test rather than per package, since queues are cheap to create
// per-test and a shared Postgres database is not.
func TestMain(m *testing.M) {
	dsn, err := testdb.Postgres("croupier_test_sqs")
	if err != nil {
		log.Fatal(err)
	}
	os.Setenv("TEST_DATABASE_URL", dsn)
	os.Exit(m.Run())
}
