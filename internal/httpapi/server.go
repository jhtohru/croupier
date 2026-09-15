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
}

type Server struct {
	mux *http.ServeMux
}

// NewServer builds the routes listed in TODO.md's Fase 7. Auth middleware
// (Fase 9) isn't wired in yet: providerId on the wagering routes is taken
// directly from client-supplied path/body, not from an authenticated
// identity — see the "Autenticação e Autorização" limitation noted in
// ARCHITECTURE.md. That's the one seam Fase 9 needs to close; nothing else
// here should need to change when it does.
func NewServer(deps Deps) *Server {
	h := &handler{deps: deps}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /wallets", h.createWallet)
	mux.HandleFunc("GET /wallets/{walletId}", h.getWallet)
	mux.HandleFunc("GET /wallets/{walletId}/ledger", h.listWalletLedger)
	mux.HandleFunc("POST /wallets/{walletId}/reconciliation", h.reconcileWallet)

	mux.HandleFunc("POST /wagering/transactions", h.submitWagerTransaction)
	mux.HandleFunc("GET /wagering/transactions/{transactionId}", h.getWagerTransaction)
	mux.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", h.getWagerTransactionByProvider)

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
