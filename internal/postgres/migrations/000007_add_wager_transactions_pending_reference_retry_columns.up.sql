-- Retry-scheduling bookkeeping for app.PendingReferenceResolver. These are
-- worker/operational metadata, not domain state (wager.Transaction never
-- carries them) — pending_reference_next_retry_at NULL means "never
-- scheduled yet", treated as immediately due by the resolver's query.
ALTER TABLE wager_transactions
    ADD COLUMN pending_reference_attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN pending_reference_next_retry_at TIMESTAMPTZ;

CREATE INDEX wager_transactions_pending_reference_due_idx
    ON wager_transactions (pending_reference_next_retry_at)
    WHERE status = 'PENDING_REFERENCE';
