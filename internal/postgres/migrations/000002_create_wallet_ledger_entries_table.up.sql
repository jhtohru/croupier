CREATE TABLE wallet_ledger_entries (
    id UUID PRIMARY KEY,
    wallet_id UUID NOT NULL REFERENCES wallets (id),
    transaction_id UUID NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    amount BIGINT NOT NULL,
    balance_before BIGINT NOT NULL,
    balance_after BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallet_ledger_entries_amount_positive CHECK (amount > 0),
    CONSTRAINT wallet_ledger_entries_balance_before_non_negative CHECK (balance_before >= 0),
    CONSTRAINT wallet_ledger_entries_balance_after_non_negative CHECK (balance_after >= 0),
    -- Mirrors wallet.NewLedgerEntry's own validation at the schema level:
    -- balanceAfter must equal balanceBefore plus or minus amount, depending
    -- on direction.
    CONSTRAINT wallet_ledger_entries_balance_consistent CHECK (
        (direction = 'CREDIT' AND balance_after = balance_before + amount) OR
        (direction = 'DEBIT' AND balance_after = balance_before - amount)
    ),
    CONSTRAINT wallet_ledger_entries_wallet_transaction_unique UNIQUE (wallet_id, transaction_id)
);

CREATE INDEX wallet_ledger_entries_wallet_id_created_at_idx
    ON wallet_ledger_entries (wallet_id, created_at, id);

-- The ledger is append-only per the domain model (no method on
-- wallet.LedgerEntry ever mutates it after construction) — enforced here too
-- so a bug or a manual query can't silently rewrite financial history.
CREATE FUNCTION wallet_ledger_entries_prevent_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER wallet_ledger_entries_immutable
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION wallet_ledger_entries_prevent_mutation();
