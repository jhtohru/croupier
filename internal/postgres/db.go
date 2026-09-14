// Package postgres implements the app.WalletRepository, app.WagerRepository,
// app.OutboxRepository and app.TxManager interfaces with pgx and explicit
// SQL. Concurrency strategy: pessimistic locking. WalletRepository.FindByID
// issues SELECT ... FOR UPDATE when called inside a TxManager.WithinTx
// block, serializing concurrent operations against the same wallet row
// without ever locking more than that one row — different wallets keep
// processing in parallel. Reads made outside WithinTx (WalletGetter,
// WalletReconciler, WalletLedgerLister) never open a transaction, so they
// never take that lock.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type txKey struct{}

// querier is satisfied by both *pgxpool.Pool and pgx.Tx, letting every
// repository method run against either without caring which.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TxManager runs repository calls within a single Postgres transaction.
type TxManager struct {
	pool *pgxpool.Pool
}

func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op once committed

	ctx = context.WithValue(ctx, txKey{}, tx)
	if err := fn(ctx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func txFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

func dbFor(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := txFromContext(ctx); ok {
		return tx
	}
	return pool
}
