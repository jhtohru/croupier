package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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

// httpMetrics is what this package needs from a metrics backend (Fase 11) —
// consumer-defined, same pattern as every dependency above;
// internal/metrics.Registry satisfies this structurally. A nil httpMetrics
// on Server (the zero value of Deps.Metrics) is a safe no-op.
type httpMetrics interface {
	ObserveRequest(method, pattern string, status int, duration time.Duration)
	ObserveWagerSubmission(kind, outcome string)
	ObserveReconciliation(consistent bool, differenceMinorUnits int64)
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
	// Ready is called by GET /health/ready. This package doesn't know or
	// care what it checks — cmd/croupier's readyChecker (Fase 8) is what
	// checks both Postgres and SQS, passed in here as this one func value,
	// so nothing in internal/httpapi needed to change when SQS readiness
	// was added.
	Ready func(ctx context.Context) error
	// Auth verifies bearer tokens (internal/auth.Verifier satisfies this).
	// Every route except /health/* requires one — see requireAuth and
	// requireInternalRole below.
	Auth tokenVerifier
	// Metrics is optional (Fase 11) — a nil Metrics disables request/outcome
	// recording entirely, no route/handler behavior changes either way.
	Metrics httpMetrics
	// MetricsHandler, when set, is served at GET /metrics (unauthenticated,
	// same reasoning as /health/*: scrapers can't present a bearer token).
	// internal/metrics.Registry.Handler() supplies this from cmd/croupier.
	MetricsHandler http.Handler
}

type Server struct {
	mux     *http.ServeMux
	metrics httpMetrics
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

	if deps.MetricsHandler != nil {
		mux.Handle("GET /metrics", deps.MetricsHandler)
	}

	return &Server{mux: mux, metrics: deps.Metrics}
}

type correlationIDKey struct{}

func correlationIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey{}).(string)
	return id
}

// statusRecorder captures the status code a handler actually wrote, so
// ServeHTTP can log/record it after the fact — http.ResponseWriter itself
// has no getter for it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// ServeHTTP assigns a correlationId to every request (reusing one supplied
// via X-Correlation-Id, e.g. from an upstream gateway, so a trace started
// there stays intact instead of getting a second, disconnected id here) and
// logs/records exactly one summary line per request — the mechanism behind
// Fase 11's "logs JSON com correlationId" for the HTTP side. Individual
// handlers still log their own errors with more specific fields
// (walletId/transactionId, see writeError) — this is the outer layer that
// makes every one of those lines findable by the same correlationId a
// caller can also see echoed back in the response header.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	correlationID := r.Header.Get("X-Correlation-Id")
	if correlationID == "" {
		correlationID = uuid.NewString()
	}
	w.Header().Set("X-Correlation-Id", correlationID)
	ctx := context.WithValue(r.Context(), correlationIDKey{}, correlationID)
	r = r.WithContext(ctx)

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	s.mux.ServeHTTP(rec, r)
	duration := time.Since(start)

	_, pattern := s.mux.Handler(r)
	slog.InfoContext(ctx, "http request",
		"method", r.Method, "pattern", pattern, "status", rec.status,
		"durationMs", duration.Milliseconds(), "correlationId", correlationID)
	if s.metrics != nil {
		s.metrics.ObserveRequest(r.Method, pattern, rec.status, duration)
	}
}

type handler struct {
	deps Deps
}
