package httpapi

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

// The interfaces below are consumer-defined (httpapi only declares the one
// method it actually calls on each use case), not app's own exported types —
// that's what lets handler tests use tiny stubs instead of real repositories
// or internal/app's fakes. Each *app.XxxYyy concrete type already satisfies
// its corresponding interface here with no changes needed on that side.

type walletCreator interface {
	Create(ctx context.Context, input app.CreateWalletInput) (*wallet.Wallet, error)
}

type walletGetter interface {
	Get(ctx context.Context, walletID uuid.UUID) (*wallet.Wallet, error)
}

type walletReconciler interface {
	Reconcile(ctx context.Context, walletID uuid.UUID) (*app.ReconciliationResult, error)
}

type walletLedgerLister interface {
	List(ctx context.Context, input app.ListWalletLedgerInput) ([]*wallet.LedgerEntry, error)
}

type wagerSubmitter interface {
	Submit(ctx context.Context, input app.SubmitWagerTransactionInput) (*app.SubmitWagerTransactionResult, error)
}

type wagerTransactionGetter interface {
	Get(ctx context.Context, id uuid.UUID) (*wager.Transaction, error)
	GetByProvider(ctx context.Context, providerID, externalTransactionID string) (*wager.Transaction, error)
}

// Deps are the use cases and checks the HTTP layer calls into. Nothing here
// depends on internal/postgres — cmd/croupier (Fase 10) is the only place
// that wires a concrete *app.XxxYyy or a real Postgres ping into these.
type Deps struct {
	WalletCreator          walletCreator
	WalletGetter           walletGetter
	WalletReconciler       walletReconciler
	WalletLedgerLister     walletLedgerLister
	WagerSubmitter         wagerSubmitter
	WagerTransactionGetter wagerTransactionGetter
	// Ready is called by GET /health/ready. Only Postgres is checked for
	// now — Fase 8 will extend whatever cmd/croupier passes in here to also
	// check SQS, with no change needed in this package.
	Ready func(ctx context.Context) error
	// Auth verifies bearer tokens (internal/auth.Verifier satisfies this).
	// Every route except /health/* requires one — see requireAuth and
	// requireInternalRole below.
	Auth tokenVerifier
}

type Server struct {
	mux *http.ServeMux
}

// NewServer builds the routes listed in TODO.md's Fase 7, gated per
// TODO.md's Fase 9: wallet operations require the internal-service role
// (requireInternalRole — not exposed to providers at all, regardless of how
// valid their own token is); wagering routes require any authenticated
// caller (requireAuth), with providerId coming from the token's own claims,
// never from client-supplied path/body — see wagering.go. Only /health/*
// stays open, since orchestration health checks can't present a token.
func NewServer(deps Deps) *Server {
	h := &handler{deps: deps}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /wallets", requireInternalRole(deps.Auth, h.createWallet))
	mux.HandleFunc("GET /wallets/{walletId}", requireInternalRole(deps.Auth, h.getWallet))
	mux.HandleFunc("GET /wallets/{walletId}/ledger", requireInternalRole(deps.Auth, h.listWalletLedger))
	mux.HandleFunc("POST /wallets/{walletId}/reconciliation", requireInternalRole(deps.Auth, h.reconcileWallet))

	mux.HandleFunc("POST /wagering/transactions", requireAuth(deps.Auth, h.submitWagerTransaction))
	mux.HandleFunc("GET /wagering/transactions/{transactionId}", requireInternalRole(deps.Auth, h.getWagerTransaction))
	mux.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", requireAuth(deps.Auth, h.getWagerTransactionByProvider))

	mux.HandleFunc("GET /health/live", h.healthLive)
	mux.HandleFunc("GET /health/ready", h.healthReady)

	return &Server{mux: mux}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type handler struct {
	deps Deps
}
