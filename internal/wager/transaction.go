package wager

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jhtohru/croupier/internal/money"
)

var (
	ErrInvalidInput         = errors.New("invalid input")
	ErrInvalidKind          = errors.New("invalid kind")
	ErrInvalidReference     = errors.New("invalid reference")
	ErrInvalidTransition    = errors.New("invalid transition")
	ErrLossAmountMustBeZero = errors.New("loss amount must be zero")
	ErrMissingReference     = errors.New("missing reference")
	ErrNonPositiveAmount    = errors.New("non-positive amount")
	ErrReferenceMismatch    = errors.New("reference mismatch")
	ErrTerminalTransaction  = errors.New("terminal transaction")
	ErrUnexpectedReference  = errors.New("unexpected reference")
)

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

func ParseKind(s string) (Kind, error) {
	if s != string(KindOpening) &&
		s != string(KindBet) &&
		s != string(KindWin) &&
		s != string(KindLoss) &&
		s != string(KindRefund) &&
		s != string(KindRollback) {
		return "", ErrInvalidKind
	}
	return Kind(s), nil
}

type TxStatus string

const (
	TxStatusPending          TxStatus = "PENDING"
	TxStatusPendingReference TxStatus = "PENDING_REFERENCE"
	TxStatusProcessed        TxStatus = "PROCESSED"
	TxStatusRejected         TxStatus = "REJECTED"
	TxStatusFailed           TxStatus = "FAILED"
)

func (ts TxStatus) IsTerminal() bool {
	return ts == TxStatusProcessed ||
		ts == TxStatusRejected ||
		ts == TxStatusFailed
}

type FailureCode string

type Transaction struct {
	id                             uuid.UUID
	status                         TxStatus
	externalTransactionID          string
	providerID                     string
	playerID                       uuid.UUID
	walletID                       uuid.UUID
	roundID                        string
	gameID                         string
	kind                           Kind
	amount                         money.Money
	referenceExternalTransactionID *string
	referenceTransactionID         *uuid.UUID
	failureCode                    FailureCode
	createdAt                      time.Time
	updatedAt                      time.Time
}

func (tx Transaction) IdempotencyKey() string {
	return tx.providerID + ":" + tx.externalTransactionID
}

type NewTransactionInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Amount                         money.Money
	ReferenceExternalTransactionID *string
}

func NewTransaction(input NewTransactionInput) (*Transaction, error) {
	if input.ProviderID == "" ||
		input.ExternalTransactionID == "" ||
		input.PlayerID == uuid.Nil ||
		input.WalletID == uuid.Nil ||
		input.RoundID == "" ||
		input.GameID == "" {
		return nil, ErrInvalidInput
	}
	switch input.Kind {
	case KindBet:
		if !input.Amount.IsPositive() {
			return nil, ErrNonPositiveAmount
		}
		if input.ReferenceExternalTransactionID != nil {
			return nil, ErrUnexpectedReference
		}
	case KindWin:
		if !input.Amount.IsPositive() {
			return nil, ErrNonPositiveAmount
		}
	case KindLoss:
		if !input.Amount.IsZero() {
			return nil, ErrLossAmountMustBeZero
		}
	case KindRefund, KindRollback:
		if !input.Amount.IsPositive() {
			return nil, ErrNonPositiveAmount
		}
		if input.ReferenceExternalTransactionID == nil {
			return nil, ErrMissingReference
		}
	default:
		return nil, ErrInvalidKind
	}
	now := time.Now()
	return &Transaction{
		id:                             uuid.New(),
		status:                         TxStatusPending,
		providerID:                     input.ProviderID,
		externalTransactionID:          input.ExternalTransactionID,
		playerID:                       input.PlayerID,
		walletID:                       input.WalletID,
		roundID:                        input.RoundID,
		gameID:                         input.GameID,
		kind:                           input.Kind,
		amount:                         input.Amount,
		referenceExternalTransactionID: input.ReferenceExternalTransactionID,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

// NewOpeningTransaction constructs the OPENING transaction created internally
// when a wallet is opened with a positive initial balance. Unlike the other
// kinds, OPENING has no provider, external transaction, round or game — it
// isn't submitted by a provider, so NewTransaction structurally refuses
// KindOpening and this is the only way to construct one. It is created
// already PROCESSED, since it doesn't go through the pending decision flow
// that provider-submitted transactions do.
type NewOpeningInput struct {
	PlayerID uuid.UUID
	WalletID uuid.UUID
	Amount   money.Money
}

func NewOpeningTransaction(input NewOpeningInput) (*Transaction, error) {
	if input.PlayerID == uuid.Nil || input.WalletID == uuid.Nil {
		return nil, ErrInvalidInput
	}
	if !input.Amount.IsPositive() {
		return nil, ErrNonPositiveAmount
	}
	now := time.Now()
	return &Transaction{
		id:        uuid.New(),
		status:    TxStatusProcessed,
		kind:      KindOpening,
		playerID:  input.PlayerID,
		walletID:  input.WalletID,
		amount:    input.Amount,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// TransactionFromPersistenceInput mirrors Transaction's full field set,
// trusted data coming from storage — no validation here, same reasoning as
// wallet.FromPersistence.
type TransactionFromPersistenceInput struct {
	ID                             uuid.UUID
	Status                         TxStatus
	Kind                           Kind
	ProviderID                     string
	ExternalTransactionID          string
	RoundID                        string
	GameID                         string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	Amount                         money.Money
	ReferenceExternalTransactionID *string
	ReferenceTransactionID         *uuid.UUID
	FailureCode                    FailureCode
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

func TransactionFromPersistence(input TransactionFromPersistenceInput) *Transaction {
	return &Transaction{
		id:                             input.ID,
		status:                         input.Status,
		kind:                           input.Kind,
		providerID:                     input.ProviderID,
		externalTransactionID:          input.ExternalTransactionID,
		roundID:                        input.RoundID,
		gameID:                         input.GameID,
		playerID:                       input.PlayerID,
		walletID:                       input.WalletID,
		amount:                         input.Amount,
		referenceExternalTransactionID: input.ReferenceExternalTransactionID,
		referenceTransactionID:         input.ReferenceTransactionID,
		failureCode:                    input.FailureCode,
		createdAt:                      input.CreatedAt,
		updatedAt:                      input.UpdatedAt,
	}
}

func (tx *Transaction) ResolveReference(id uuid.UUID) error {
	if tx.status != TxStatusPending && tx.status != TxStatusPendingReference {
		return ErrInvalidTransition
	}
	if tx.referenceTransactionID != nil && *tx.referenceTransactionID != id {
		return ErrReferenceMismatch
	}
	tx.referenceTransactionID = &id
	tx.updatedAt = time.Now()
	return nil
}

func (tx Transaction) ValidateReference(ref Transaction) error {
	if tx.referenceExternalTransactionID == nil {
		return ErrMissingReference
	}
	if !referenceableKind(tx.kind, ref.kind) ||
		ref.status != TxStatusProcessed ||
		ref.providerID != tx.providerID ||
		ref.externalTransactionID != *tx.referenceExternalTransactionID ||
		ref.playerID != tx.playerID ||
		ref.walletID != tx.walletID ||
		ref.roundID != tx.roundID {
		return ErrInvalidReference
	}
	if ok, err := ref.amount.Equal(tx.amount); !ok || err != nil {
		return ErrInvalidReference
	}
	return nil
}

// referenceableKind reports whether a transaction of kind may reference a
// transaction of refKind. REFUND only ever reverses a processed BET; ROLLBACK
// reverses BET, WIN or REFUND (never OPENING, LOSS, or another ROLLBACK).
func referenceableKind(kind, refKind Kind) bool {
	switch kind {
	case KindRefund:
		return refKind == KindBet
	case KindRollback:
		return refKind == KindBet || refKind == KindWin || refKind == KindRefund
	default:
		return false
	}
}

func (tx Transaction) PayloadHash() [32]byte {
	var hashInput = struct {
		PlayerID                       uuid.UUID   `json:"playerId"`
		WalletID                       uuid.UUID   `json:"walletId"`
		RoundID                        string      `json:"roundId"`
		GameID                         string      `json:"gameId"`
		Kind                           Kind        `json:"kind"`
		Amount                         money.Money `json:"amount"`
		ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId,omitempty"`
	}{
		PlayerID:                       tx.playerID,
		WalletID:                       tx.walletID,
		RoundID:                        tx.roundID,
		GameID:                         tx.gameID,
		Kind:                           tx.kind,
		Amount:                         tx.amount,
		ReferenceExternalTransactionID: tx.referenceExternalTransactionID,
	}
	b, err := json.Marshal(hashInput)
	if err != nil {
		panic(err)
	}
	return sha256.Sum256(b)
}

func (tx *Transaction) MarkProcessed() error {
	if tx.status.IsTerminal() {
		return ErrTerminalTransaction
	}
	tx.status = TxStatusProcessed
	tx.updatedAt = time.Now()
	return nil
}

func (tx *Transaction) MarkRejected(failureCode FailureCode) error {
	if tx.status.IsTerminal() {
		return ErrTerminalTransaction
	}
	tx.status = TxStatusRejected
	tx.failureCode = failureCode
	tx.updatedAt = time.Now()
	return nil
}

// MarkPendingReference parks tx waiting for its reference to appear. Callable
// from PENDING (first time) or, idempotently, from PENDING_REFERENCE itself —
// the retry worker calls this again on every attempt that still can't find
// the reference, and re-affirming the same state must not be an error.
func (tx *Transaction) MarkPendingReference() error {
	if tx.status.IsTerminal() {
		return ErrTerminalTransaction
	}
	if tx.status != TxStatusPending && tx.status != TxStatusPendingReference {
		return ErrInvalidTransition
	}
	tx.status = TxStatusPendingReference
	tx.updatedAt = time.Now()
	return nil
}

func (tx *Transaction) MarkFailed(failureCode FailureCode) error {
	if tx.status.IsTerminal() {
		return ErrTerminalTransaction
	}
	tx.status = TxStatusFailed
	tx.failureCode = failureCode
	tx.updatedAt = time.Now()
	return nil
}

func (tx Transaction) ID() uuid.UUID {
	return tx.id
}

func (tx Transaction) Status() TxStatus {
	return tx.status
}

func (tx Transaction) ExternalTransactionID() string {
	return tx.externalTransactionID
}

func (tx Transaction) ProviderID() string {
	return tx.providerID
}

func (tx Transaction) PlayerID() uuid.UUID {
	return tx.playerID
}

func (tx Transaction) WalletID() uuid.UUID {
	return tx.walletID
}

func (tx Transaction) RoundID() string {
	return tx.roundID
}

func (tx Transaction) GameID() string {
	return tx.gameID
}

func (tx Transaction) Kind() Kind {
	return tx.kind
}

func (tx Transaction) Amount() money.Money {
	return tx.amount
}

func (tx Transaction) ReferenceExternalTransactionID() *string {
	return tx.referenceExternalTransactionID
}

func (tx Transaction) ReferenceTransactionID() *uuid.UUID {
	return tx.referenceTransactionID
}

func (tx Transaction) FailureCode() FailureCode {
	return tx.failureCode
}

func (tx Transaction) CreatedAt() time.Time {
	return tx.createdAt
}

func (tx Transaction) UpdatedAt() time.Time {
	return tx.updatedAt
}
