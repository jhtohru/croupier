package app

import (
	"context"
	"errors"
	"time"

	"github.com/jhtohru/croupier/internal/outbox"
)

// OutboxPublisher ships one outbox entry to wherever it's supposed to go
// (internal/sqs's implementation sends it as an SQS message, Fase 8) — the
// only thing OutboxWorker needs from messaging infrastructure.
type OutboxPublisher interface {
	Publish(ctx context.Context, entry *outbox.Entry) error
}

// OutboxWorker publishes PENDING outbox entries after their originating
// transaction has already committed (never before — that's the whole point
// of the outbox pattern: an event only exists to publish once the write
// it describes is durable).
type OutboxWorker struct {
	outbox      OutboxRepository
	publisher   OutboxPublisher
	txManager   TxManager
	backoffBase time.Duration
}

func NewOutboxWorker(outboxRepo OutboxRepository, publisher OutboxPublisher, txManager TxManager, backoffBase time.Duration) *OutboxWorker {
	return &OutboxWorker{outbox: outboxRepo, publisher: publisher, txManager: txManager, backoffBase: backoffBase}
}

// RunOnce claims and publishes at most one due entry. It reports whether it
// found one to process — the caller (cmd/croupier, Fase 10) drives the
// polling cadence by calling this on a timer, same pattern as
// PendingReferenceResolver.ResolveDue.
//
// Claiming and publishing happen inside one TxManager.WithinTx: the row lock
// from OutboxRepository.FindDueForUpdate (SELECT ... FOR UPDATE SKIP LOCKED)
// is what gives "múltiplos publishers" safety — two workers racing for the
// same entry never both get it — and it's also what gives "recovery de
// trabalho abandonado" for free: if this process dies mid-publish, the
// transaction never commits, Postgres releases the lock, and the next
// worker to poll picks the same entry back up. The entry's own id (its
// eventId) never changes across attempts, so a republish after a crash
// still carries the same id a downstream consumer would already recognize.
func (w *OutboxWorker) RunOnce(ctx context.Context) (bool, error) {
	var processed bool
	err := w.txManager.WithinTx(ctx, func(ctx context.Context) error {
		entry, err := w.outbox.FindDueForUpdate(ctx)
		if err != nil {
			if errors.Is(err, ErrOutboxEntryNotFound) {
				return nil
			}
			return err
		}
		processed = true

		if pubErr := w.publisher.Publish(ctx, entry); pubErr != nil {
			next := time.Now().Add(backoffDelay(w.backoffBase, entry.RetryCount()))
			if err := entry.ScheduleRetry(next); err != nil {
				return err
			}
			return w.outbox.SaveAll(ctx, entry)
		}

		entry.MarkPublished()
		return w.outbox.SaveAll(ctx, entry)
	})
	return processed, err
}
