-- FindLedgerEntryByTransactionID filters by transaction_id alone; the
-- existing wallet_ledger_entries_wallet_transaction_unique index is led by
-- wallet_id, so it can't serve that lookup and every call falls back to a
-- sequential scan.
CREATE INDEX wallet_ledger_entries_transaction_id_idx
    ON wallet_ledger_entries (transaction_id);
