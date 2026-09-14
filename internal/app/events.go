package app

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/outbox"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

// Event type names and payload shapes below are our own interpretation where
// the challenge spec doesn't fully define them (WagerTransactionProcessed's
// payload in particular) — WalletBalanceChanged's fields match the spec
// literally (walletId, transactionId, direction, money, balanceBefore,
// balanceAfter, walletVersion).
const (
	EventTypeWagerTransactionProcessed = "WagerTransactionProcessed"
	EventTypeWalletBalanceChanged      = "WalletBalanceChanged"
)

type wagerTransactionProcessedData struct {
	TransactionID uuid.UUID   `json:"transactionId"`
	Kind          wager.Kind  `json:"kind"`
	Amount        money.Money `json:"amount"`
}

func newWagerTransactionProcessedEvent(tx *wager.Transaction) (*outbox.Entry, error) {
	payload, err := json.Marshal(wagerTransactionProcessedData{
		TransactionID: tx.ID(),
		Kind:          tx.Kind(),
		Amount:        tx.Amount(),
	})
	if err != nil {
		return nil, err
	}
	return outbox.NewEntry(outbox.NewEntryInput{
		AggregateType: "WagerTransaction",
		AggregateID:   tx.ID(),
		EventType:     EventTypeWagerTransactionProcessed,
		Payload:       payload,
		OccurredAt:    time.Now(),
	})
}

type walletBalanceChangedData struct {
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     wallet.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
}

func newWalletBalanceChangedEvent(w *wallet.Wallet, entry *wallet.LedgerEntry) (*outbox.Entry, error) {
	payload, err := json.Marshal(walletBalanceChangedData{
		WalletID:      w.ID(),
		TransactionID: entry.TransactionID(),
		Direction:     entry.Direction(),
		Money:         entry.Amount(),
		BalanceBefore: entry.BalanceBefore(),
		BalanceAfter:  entry.BalanceAfter(),
		WalletVersion: w.Version(),
	})
	if err != nil {
		return nil, err
	}
	return outbox.NewEntry(outbox.NewEntryInput{
		AggregateType: "Wallet",
		AggregateID:   w.ID(),
		EventType:     EventTypeWalletBalanceChanged,
		Payload:       payload,
		OccurredAt:    time.Now(),
	})
}
