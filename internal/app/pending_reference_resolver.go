package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/wager"
)

// PendingReferenceRetry pairs a PENDING_REFERENCE transaction with how many
// resolution attempts have already been made against it — attempts lives
// outside wager.Transaction because it's retry-scheduling bookkeeping for
// this worker, not a domain invariant of the transaction itself.
type PendingReferenceRetry struct {
	Transaction *wager.Transaction
	Attempts    int
}

// PendingReferenceResolver periodically retries REFUND/ROLLBACK transactions
// parked in PENDING_REFERENCE because their reference hadn't arrived yet.
// Each attempt reuses WagerSubmitter.process itself — resolving a reference
// is exactly the same decision process a fresh submission goes through, just
// entered again from PENDING_REFERENCE instead of PENDING. If the reference
// still can't be found, tx stays PENDING_REFERENCE, and this resolver alone
// (not WagerSubmitter) decides whether to schedule another attempt or give up.
// PendingReferenceMetrics lets PendingReferenceResolver report resolution
// outcomes (Fase 11) — same optional, no-op-when-nil pattern as
// OutboxMetrics above.
type PendingReferenceMetrics interface {
	ObservePendingReferenceResolution(outcome string)
}

type PendingReferenceResolver struct {
	wagers      WagerRepository
	submitter   *WagerSubmitter
	maxAttempts int
	ttl         time.Duration
	backoffBase time.Duration
	metrics     PendingReferenceMetrics
}

// PendingReferenceResolverOption customizes a PendingReferenceResolver built
// by NewPendingReferenceResolver — same variadic-option reasoning as
// OutboxWorkerOption.
type PendingReferenceResolverOption func(*PendingReferenceResolver)

func WithPendingReferenceMetrics(m PendingReferenceMetrics) PendingReferenceResolverOption {
	return func(r *PendingReferenceResolver) { r.metrics = m }
}

func NewPendingReferenceResolver(
	wagers WagerRepository,
	submitter *WagerSubmitter,
	maxAttempts int,
	ttl time.Duration,
	backoffBase time.Duration,
	opts ...PendingReferenceResolverOption,
) *PendingReferenceResolver {
	r := &PendingReferenceResolver{
		wagers:      wagers,
		submitter:   submitter,
		maxAttempts: maxAttempts,
		ttl:         ttl,
		backoffBase: backoffBase,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ResolveDue attempts to resolve every PENDING_REFERENCE transaction whose
// retry schedule is due as of now, up to limit transactions, and reports how
// many it processed (resolved, re-parked, or expired all count). Meant to be
// invoked on a timer by cmd/croupier (Fase 10) — it does no scheduling of its
// own.
func (r *PendingReferenceResolver) ResolveDue(ctx context.Context, now time.Time, limit int) (int, error) {
	due, err := r.wagers.FindDuePendingReferences(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	for _, item := range due {
		if err := r.resolveOne(ctx, now, item); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

func (r *PendingReferenceResolver) resolveOne(ctx context.Context, now time.Time, item PendingReferenceRetry) error {
	tx := item.Transaction
	// Each resolution attempt is its own operation, not a live HTTP/SQS
	// request — there's no caller-supplied correlationId to reuse here, so
	// one is minted per attempt, same reasoning as internal/sqs.Consumer
	// minting one per delivery.
	correlationID := uuid.NewString()

	if item.Attempts >= r.maxAttempts || now.Sub(tx.CreatedAt()) >= r.ttl {
		_, err := r.submitter.reject(ctx, tx, FailureCodeReferenceNotFound, correlationID)
		if err == nil && r.metrics != nil {
			r.metrics.ObservePendingReferenceResolution("expired")
		}
		return err
	}

	if _, err := r.submitter.process(ctx, tx, correlationID); err != nil {
		return err
	}
	if tx.Status() != wager.TxStatusPendingReference {
		// Resolved one way or another (processed or rejected) — process
		// already persisted the result; nothing left to schedule.
		if r.metrics != nil {
			r.metrics.ObservePendingReferenceResolution("resolved")
		}
		return nil
	}

	if r.metrics != nil {
		r.metrics.ObservePendingReferenceResolution("still_pending")
	}
	next := now.Add(backoffDelay(r.backoffBase, item.Attempts))
	return r.wagers.ScheduleNextPendingReferenceRetry(ctx, tx.ID(), next)
}

// backoffDelay computes base * 2^attempts, capped to stay well clear of
// time.Duration overflow for any attempts count maxAttempts could reasonably
// be set to.
func backoffDelay(base time.Duration, attempts int) time.Duration {
	const maxShift = 20 // base << 20 is already >1000x base; plenty of ceiling
	if attempts < 0 {
		attempts = 0
	}
	if attempts > maxShift {
		attempts = maxShift
	}
	return base << attempts
}
