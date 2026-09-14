DROP INDEX wager_transactions_pending_reference_due_idx;

ALTER TABLE wager_transactions
    DROP COLUMN pending_reference_attempts,
    DROP COLUMN pending_reference_next_retry_at;
