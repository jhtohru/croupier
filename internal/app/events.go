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
	EventTypeWagerTransactionProcessed        = "WagerTransactionProcessed"
	EventTypeWagerTransactionRejected         = "WagerTransactionRejected"
	EventTypeWagerTransactionPendingReference = "WagerTransactionPendingReference"
	EventTypeWalletBalanceChanged             = "WalletBalanceChanged"
)

// eventEnvelopeVersion is the schema version for every event type below —
// bump the specific event's call sites (not this shared constant) the day
// one of these payloads actually needs a breaking change; there's no
// version history yet, so every event starts at 1.
const eventEnvelopeVersion = 1

type wagerTransactionProcessedData struct {
	TransactionID uuid.UUID   `json:"transactionId"`
	Kind          wager.Kind  `json:"kind"`
	Amount        money.Money `json:"amount"`
}

func newWagerTransactionProcessedEvent(tx *wager.Transaction, correlationID string) (*outbox.Entry, error) {
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
		Version:       eventEnvelopeVersion,
		Payload:       payload,
		OccurredAt:    time.Now(),
		CorrelationID: correlationID,
	})
}

type wagerTransactionRejectedData struct {
	TransactionID uuid.UUID         `json:"transactionId"`
	Kind          wager.Kind        `json:"kind"`
	FailureCode   wager.FailureCode `json:"failureCode"`
}

func newWagerTransactionRejectedEvent(tx *wager.Transaction, correlationID string) (*outbox.Entry, error) {
	payload, err := json.Marshal(wagerTransactionRejectedData{
		TransactionID: tx.ID(),
		Kind:          tx.Kind(),
		FailureCode:   tx.FailureCode(),
	})
	if err != nil {
		return nil, err
	}
	return outbox.NewEntry(outbox.NewEntryInput{
		AggregateType: "WagerTransaction",
		AggregateID:   tx.ID(),
		EventType:     EventTypeWagerTransactionRejected,
		Version:       eventEnvelopeVersion,
		Payload:       payload,
		OccurredAt:    time.Now(),
		CorrelationID: correlationID,
	})
}

type wagerTransactionPendingReferenceData struct {
	TransactionID                  uuid.UUID `json:"transactionId"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId"`
}

func newWagerTransactionPendingReferenceEvent(tx *wager.Transaction, correlationID string) (*outbox.Entry, error) {
	var refExtID string
	if tx.ReferenceExternalTransactionID() != nil {
		refExtID = *tx.ReferenceExternalTransactionID()
	}
	payload, err := json.Marshal(wagerTransactionPendingReferenceData{
		TransactionID:                  tx.ID(),
		ReferenceExternalTransactionID: refExtID,
	})
	if err != nil {
		return nil, err
	}
	return outbox.NewEntry(outbox.NewEntryInput{
		AggregateType: "WagerTransaction",
		AggregateID:   tx.ID(),
		EventType:     EventTypeWagerTransactionPendingReference,
		Version:       eventEnvelopeVersion,
		Payload:       payload,
		OccurredAt:    time.Now(),
		CorrelationID: correlationID,
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

// newWalletBalanceChangedEvent's causationID, when given, is the id of the
// sibling WagerTransactionProcessed event emitted in the very same call
// (process(), in wager_submit.go, and Create(), in wallet_create.go) — a
// precise causal link ("this balance change happened because that specific
// event happened"), not just the broader request-level correlationId every
// event from the same call already shares. Optional per the challenge spec
// (§11: "causationId opcional"); nil when there's no more specific sibling
// event to point to.
func newWalletBalanceChangedEvent(w *wallet.Wallet, entry *wallet.LedgerEntry, correlationID string, causationID *string) (*outbox.Entry, error) {
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
		Version:       eventEnvelopeVersion,
		Payload:       payload,
		OccurredAt:    time.Now(),
		CorrelationID: correlationID,
		CausationID:   causationID,
	})
}
