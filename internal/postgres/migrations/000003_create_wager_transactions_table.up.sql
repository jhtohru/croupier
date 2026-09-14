CREATE TABLE wager_transactions (
    id UUID PRIMARY KEY,
    status TEXT NOT NULL
        CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    kind TEXT NOT NULL
        CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    -- Nullable: OPENING transactions (created only by wallet opening, never
    -- submitted by a provider) have none of these — see
    -- wager.NewOpeningTransaction. A plain UNIQUE constraint treats each
    -- NULL as distinct, so multiple OPENING rows don't collide on it.
    provider_id TEXT,
    external_transaction_id TEXT,
    round_id TEXT,
    game_id TEXT,
    player_id UUID NOT NULL,
    wallet_id UUID NOT NULL REFERENCES wallets (id),
    currency CHAR(3) NOT NULL,
    amount BIGINT NOT NULL,
    reference_external_transaction_id TEXT,
    reference_transaction_id UUID REFERENCES wager_transactions (id),
    failure_code TEXT,
    payload_hash BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT wager_transactions_provider_external_unique UNIQUE (provider_id, external_transaction_id),
    -- Mirrors NewTransaction's ErrInvalidInput: every kind except OPENING
    -- requires all four of these to be present.
    CONSTRAINT wager_transactions_provider_fields_required CHECK (
        kind = 'OPENING' OR (
            provider_id IS NOT NULL AND provider_id <> '' AND
            external_transaction_id IS NOT NULL AND external_transaction_id <> '' AND
            round_id IS NOT NULL AND round_id <> '' AND
            game_id IS NOT NULL AND game_id <> ''
        )
    )
);

CREATE INDEX wager_transactions_reference_lookup_idx
    ON wager_transactions (reference_transaction_id, kind, status)
    WHERE reference_transaction_id IS NOT NULL;
