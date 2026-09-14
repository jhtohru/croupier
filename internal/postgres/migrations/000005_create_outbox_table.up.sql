CREATE TABLE outbox (
    id UUID PRIMARY KEY,
    aggregate_type TEXT NOT NULL,
    aggregate_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'PUBLISHED')),
    retry_count INTEGER NOT NULL DEFAULT 0,
    next_send_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

-- Serves the outbox worker's "what's due for (re)send" query; entries that
-- are already PUBLISHED never need to be scanned again.
CREATE INDEX outbox_pending_next_send_at_idx
    ON outbox (next_send_at)
    WHERE status = 'PENDING';
