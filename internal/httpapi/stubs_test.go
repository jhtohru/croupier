package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

// Stubs implement the small consumer-defined interfaces in server.go — no
// fake repository, no real Postgres, just canned inputs/outputs to test each
// handler's HTTP-shaped behavior (status codes, JSON shape, error mapping)
// in isolation from the use-case logic, which internal/app already tests on
// its own.

type stubWalletCreator struct {
	wallet *wallet.Wallet
	err    error
}

func (s stubWalletCreator) Create(ctx context.Context, input app.CreateWalletInput) (*wallet.Wallet, error) {
	return s.wallet, s.err
}

type stubWalletGetter struct {
	wallet *wallet.Wallet
	err    error
}

func (s stubWalletGetter) Get(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error) {
	return s.wallet, s.err
}

type stubWalletReconciler struct {
	result *app.ReconciliationResult
	err    error
}

func (s stubWalletReconciler) Reconcile(ctx context.Context, walletID uuid.UUID) (*app.ReconciliationResult, error) {
	return s.result, s.err
}

type stubWalletLedgerLister struct {
	entries []*wallet.LedgerEntry
	err     error
	// gotInput captures the last call's input, so a test can assert on how
	// the handler translated query params into it.
	gotInput app.ListWalletLedgerInput
}

func (s *stubWalletLedgerLister) List(ctx context.Context, input app.ListWalletLedgerInput) ([]*wallet.LedgerEntry, error) {
	s.gotInput = input
	return s.entries, s.err
}

type stubWagerSubmitter struct {
	result *app.SubmitWagerTransactionResult
	err    error
	// gotInput captures the last call's input.
	gotInput app.SubmitWagerTransactionInput
}

func (s *stubWagerSubmitter) Submit(ctx context.Context, input app.SubmitWagerTransactionInput) (*app.SubmitWagerTransactionResult, error) {
	s.gotInput = input
	return s.result, s.err
}

type stubWagerTransactionGetter struct {
	tx  *wager.Transaction
	err error
}

func (s stubWagerTransactionGetter) Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error) {
	return s.tx, s.err
}

func (s stubWagerTransactionGetter) GetByProvider(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error) {
	return s.tx, s.err
}
