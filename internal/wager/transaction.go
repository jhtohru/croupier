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
	case KindWin, KindOpening:
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
	}, nil
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
	if ref.status != TxStatusProcessed ||
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

func (tx *Transaction) MarkPendingReference() error {
	if tx.status.IsTerminal() {
		return ErrTerminalTransaction
	}
	if tx.status != TxStatusPending {
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
