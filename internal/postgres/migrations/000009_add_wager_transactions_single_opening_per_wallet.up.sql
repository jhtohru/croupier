-- Challenge spec §6.3.23: "O schema deve impedir crédito inicial
-- duplicado." Until now, nothing at the schema level stopped a second
-- OPENING row for the same wallet_id — the guarantee was purely emergent
-- from WalletCreator.Create only ever inserting one OPENING, in the same
-- transaction as the wallet itself. A direct insert (bug, or manual access)
-- wouldn't have been blocked by anything in wager_transactions itself.
CREATE UNIQUE INDEX wager_transactions_single_opening_per_wallet_idx
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';
