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
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jhtohru/croupier/internal/app"
)

type txKey struct{}

// querier is satisfied by both *pgxpool.Pool and pgx.Tx, letting every
// repository method run against either without caring which.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// wrapTransientErr classifies err via pgconn.SafeToRetry — true means the
// failure happened before any data reached the server (a dial/connection
// failure, not anything about the query itself) — and wraps it in
// app.ErrUnavailable when so, letting internal/httpapi's writeError map it
// to 503 instead of a generic 500 (challenge spec §9's "indisponibilidade
// transitória" must be distinguishable by contract). Every other error
// (including pgx.ErrNoRows — a successful round trip, not a connectivity
// failure) passes through unchanged.
func wrapTransientErr(err error) error {
	if err == nil {
		return nil
	}
	if pgconn.SafeToRetry(err) {
		return fmt.Errorf("%w: %w", app.ErrUnavailable, err)
	}
	return err
}

// wrappingQuerier decorates a querier so every error it returns has already
// passed through wrapTransientErr — the one place this happens, so no
// individual repository method needs to remember to call it.
type wrappingQuerier struct {
	inner querier
}

func (q wrappingQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := q.inner.Exec(ctx, sql, args...)
	return tag, wrapTransientErr(err)
}

func (q wrappingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := q.inner.Query(ctx, sql, args...)
	return rows, wrapTransientErr(err)
}

func (q wrappingQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return wrappingRow{inner: q.inner.QueryRow(ctx, sql, args...)}
}

// wrappingRow exists because QueryRow itself never returns an error —
// pgx.Row defers that to Scan, so that's where wrapTransientErr has to hook
// in for the QueryRow path.
type wrappingRow struct {
	inner pgx.Row
}

func (r wrappingRow) Scan(dest ...any) error {
	return wrapTransientErr(r.inner.Scan(dest...))
}

// TxManager runs repository calls within a single Postgres transaction.
type TxManager struct {
	pool *pgxpool.Pool
}

func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// WithinTx is reentrant: if ctx already carries a transaction (a caller
// composing multiple TxManager-using operations into one atomic unit, e.g.
// internal/sqs.Consumer wrapping an inbox save around app.WagerSubmitter's
// own WithinTx call), it joins that transaction instead of opening a second,
// unrelated one on a different pool connection — only the outermost call
// begins/commits/rolls back.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := txFromContext(ctx); ok {
		return fn(ctx)
	}

	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return wrapTransientErr(err)
	}
	defer tx.Rollback(ctx) // no-op once committed

	ctx = context.WithValue(ctx, txKey{}, tx)
	if err := fn(ctx); err != nil {
		return err
	}
	return wrapTransientErr(tx.Commit(ctx))
}

func txFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

func dbFor(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := txFromContext(ctx); ok {
		return wrappingQuerier{inner: tx}
	}
	return wrappingQuerier{inner: pool}
}
