package wager

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/jhtohru/croupier/internal/money"
)

const brl = "BRL"

func validInput(t *testing.T, kind Kind) NewTransactionInput {
	t.Helper()

	amount, err := money.FromMinorUnits(brl, 1000)
	if err != nil {
		t.Fatal(err)
	}

	input := NewTransactionInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-1",
		PlayerID:              uuid.New(),
		WalletID:              uuid.New(),
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  kind,
		Amount:                amount,
	}

	switch kind {
	case KindLoss:
		zero, err := money.Zero(brl)
		if err != nil {
			t.Fatal(err)
		}
		input.Amount = zero
	case KindRefund, KindRollback:
		ref := "referenced-ext-1"
		input.ReferenceExternalTransactionID = &ref
	}

	return input
}

func TestParseKind(t *testing.T) {
	t.Run("invalid kind", func(t *testing.T) {
		k, err := ParseKind("INVALID")
		assert.ErrorIs(t, err, ErrInvalidKind)
		assert.Zero(t, k)
	})

	t.Run("opening", func(t *testing.T) {
		k, err := ParseKind("OPENING")
		assert.NoError(t, err)
		assert.Equal(t, KindOpening, k)
	})

	t.Run("bet", func(t *testing.T) {
		k, err := ParseKind("BET")
		assert.NoError(t, err)
		assert.Equal(t, KindBet, k)
	})

	t.Run("win", func(t *testing.T) {
		k, err := ParseKind("WIN")
		assert.NoError(t, err)
		assert.Equal(t, KindWin, k)
	})

	t.Run("loss", func(t *testing.T) {
		k, err := ParseKind("LOSS")
		assert.NoError(t, err)
		assert.Equal(t, KindLoss, k)
	})

	t.Run("refund", func(t *testing.T) {
		k, err := ParseKind("REFUND")
		assert.NoError(t, err)
		assert.Equal(t, KindRefund, k)
	})

	t.Run("rollback", func(t *testing.T) {
		k, err := ParseKind("ROLLBACK")
		assert.NoError(t, err)
		assert.Equal(t, KindRollback, k)
	})
}

func TestNewTransaction(t *testing.T) {
	t.Run("invalid input", func(t *testing.T) {
		t.Run("empty provider id", func(t *testing.T) {
			input := validInput(t, KindBet)
			input.ProviderID = ""
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.Nil(t, tx)
		})

		t.Run("empty external transaction id", func(t *testing.T) {
			input := validInput(t, KindBet)
			input.ExternalTransactionID = ""
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.Nil(t, tx)
		})

		t.Run("nil player id", func(t *testing.T) {
			input := validInput(t, KindBet)
			input.PlayerID = uuid.Nil
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.Nil(t, tx)
		})

		t.Run("nil wallet id", func(t *testing.T) {
			input := validInput(t, KindBet)
			input.WalletID = uuid.Nil
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.Nil(t, tx)
		})

		t.Run("empty round id", func(t *testing.T) {
			input := validInput(t, KindBet)
			input.RoundID = ""
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.Nil(t, tx)
		})

		t.Run("empty game id", func(t *testing.T) {
			input := validInput(t, KindBet)
			input.GameID = ""
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.Nil(t, tx)
		})
	})

	t.Run("bet", func(t *testing.T) {
		t.Run("non-positive amount", func(t *testing.T) {
			input := validInput(t, KindBet)
			zero, err := money.Zero(brl)
			if err != nil {
				t.Fatal(err)
			}
			input.Amount = zero
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrNonPositiveAmount)
			assert.Nil(t, tx)
		})

		t.Run("unexpected reference", func(t *testing.T) {
			input := validInput(t, KindBet)
			ref := "some-ref"
			input.ReferenceExternalTransactionID = &ref
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrUnexpectedReference)
			assert.Nil(t, tx)
		})

		t.Run("success", func(t *testing.T) {
			start := time.Now()
			input := validInput(t, KindBet)
			tx, err := NewTransaction(input)
			assert.NoError(t, err)
			if assert.NotNil(t, tx) {
				assert.Equal(t, TxStatusPending, tx.status)
				assert.Equal(t, KindBet, tx.kind)
				assert.Equal(t, input.Amount, tx.amount)
				assert.Nil(t, tx.referenceExternalTransactionID)
				assert.False(t, tx.createdAt.Before(start))
				assert.True(t, tx.createdAt.Before(time.Now()))
				assert.Equal(t, tx.createdAt, tx.updatedAt)
			}
		})
	})

	t.Run("win", func(t *testing.T) {
		t.Run("non-positive amount", func(t *testing.T) {
			input := validInput(t, KindWin)
			zero, err := money.Zero(brl)
			if err != nil {
				t.Fatal(err)
			}
			input.Amount = zero
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrNonPositiveAmount)
			assert.Nil(t, tx)
		})

		t.Run("success without reference", func(t *testing.T) {
			input := validInput(t, KindWin)
			tx, err := NewTransaction(input)
			assert.NoError(t, err)
			if assert.NotNil(t, tx) {
				assert.Nil(t, tx.referenceExternalTransactionID)
			}
		})

		t.Run("success with reference", func(t *testing.T) {
			input := validInput(t, KindWin)
			ref := "bet-ext-1"
			input.ReferenceExternalTransactionID = &ref
			tx, err := NewTransaction(input)
			assert.NoError(t, err)
			if assert.NotNil(t, tx) {
				assert.Equal(t, &ref, tx.referenceExternalTransactionID)
			}
		})
	})

	t.Run("opening is rejected", func(t *testing.T) {
		input := validInput(t, KindOpening)
		tx, err := NewTransaction(input)
		assert.ErrorIs(t, err, ErrInvalidKind)
		assert.Nil(t, tx)
	})

	t.Run("loss", func(t *testing.T) {
		t.Run("non-zero amount", func(t *testing.T) {
			input := validInput(t, KindLoss)
			amount, err := money.FromMinorUnits(brl, 100)
			if err != nil {
				t.Fatal(err)
			}
			input.Amount = amount
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrLossAmountMustBeZero)
			assert.Nil(t, tx)
		})

		t.Run("success", func(t *testing.T) {
			input := validInput(t, KindLoss)
			tx, err := NewTransaction(input)
			assert.NoError(t, err)
			if assert.NotNil(t, tx) {
				assert.Equal(t, KindLoss, tx.kind)
				assert.True(t, tx.amount.IsZero())
			}
		})
	})

	t.Run("refund", func(t *testing.T) {
		t.Run("non-positive amount", func(t *testing.T) {
			input := validInput(t, KindRefund)
			zero, err := money.Zero(brl)
			if err != nil {
				t.Fatal(err)
			}
			input.Amount = zero
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrNonPositiveAmount)
			assert.Nil(t, tx)
		})

		t.Run("missing reference", func(t *testing.T) {
			input := validInput(t, KindRefund)
			input.ReferenceExternalTransactionID = nil
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrMissingReference)
			assert.Nil(t, tx)
		})

		t.Run("success", func(t *testing.T) {
			input := validInput(t, KindRefund)
			tx, err := NewTransaction(input)
			assert.NoError(t, err)
			if assert.NotNil(t, tx) {
				assert.Equal(t, input.ReferenceExternalTransactionID, tx.referenceExternalTransactionID)
			}
		})
	})

	t.Run("rollback", func(t *testing.T) {
		t.Run("non-positive amount", func(t *testing.T) {
			input := validInput(t, KindRollback)
			zero, err := money.Zero(brl)
			if err != nil {
				t.Fatal(err)
			}
			input.Amount = zero
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrNonPositiveAmount)
			assert.Nil(t, tx)
		})

		t.Run("missing reference", func(t *testing.T) {
			input := validInput(t, KindRollback)
			input.ReferenceExternalTransactionID = nil
			tx, err := NewTransaction(input)
			assert.ErrorIs(t, err, ErrMissingReference)
			assert.Nil(t, tx)
		})

		t.Run("success", func(t *testing.T) {
			input := validInput(t, KindRollback)
			tx, err := NewTransaction(input)
			assert.NoError(t, err)
			if assert.NotNil(t, tx) {
				assert.Equal(t, input.ReferenceExternalTransactionID, tx.referenceExternalTransactionID)
			}
		})
	})

	t.Run("invalid kind", func(t *testing.T) {
		input := validInput(t, KindBet)
		input.Kind = Kind("UNKNOWN")
		tx, err := NewTransaction(input)
		assert.ErrorIs(t, err, ErrInvalidKind)
		assert.Nil(t, tx)
	})
}

func TestNewOpeningTransaction(t *testing.T) {
	validOpeningInput := func(t *testing.T) NewOpeningInput {
		t.Helper()
		amount, err := money.FromMinorUnits(brl, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return NewOpeningInput{
			PlayerID: uuid.New(),
			WalletID: uuid.New(),
			Amount:   amount,
		}
	}

	t.Run("nil player id", func(t *testing.T) {
		input := validOpeningInput(t)
		input.PlayerID = uuid.Nil
		tx, err := NewOpeningTransaction(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, tx)
	})

	t.Run("nil wallet id", func(t *testing.T) {
		input := validOpeningInput(t)
		input.WalletID = uuid.Nil
		tx, err := NewOpeningTransaction(input)
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, tx)
	})

	t.Run("non-positive amount", func(t *testing.T) {
		input := validOpeningInput(t)
		zero, err := money.Zero(brl)
		if err != nil {
			t.Fatal(err)
		}
		input.Amount = zero
		tx, err := NewOpeningTransaction(input)
		assert.ErrorIs(t, err, ErrNonPositiveAmount)
		assert.Nil(t, tx)
	})

	t.Run("success", func(t *testing.T) {
		input := validOpeningInput(t)
		tx, err := NewOpeningTransaction(input)
		assert.NoError(t, err)
		if assert.NotNil(t, tx) {
			assert.NotEqual(t, uuid.Nil, tx.id)
			assert.Equal(t, TxStatusProcessed, tx.status)
			assert.Equal(t, KindOpening, tx.kind)
			assert.Equal(t, input.PlayerID, tx.playerID)
			assert.Equal(t, input.WalletID, tx.walletID)
			assert.Equal(t, input.Amount, tx.amount)
			assert.Empty(t, tx.providerID)
			assert.Empty(t, tx.externalTransactionID)
			assert.Empty(t, tx.roundID)
			assert.Empty(t, tx.gameID)
			assert.Nil(t, tx.referenceExternalTransactionID)
		}
	})
}

func TestTransactionGetters(t *testing.T) {
	id := uuid.New()
	playerID := uuid.New()
	walletID := uuid.New()
	referenceTransactionID := uuid.New()
	referenceExternalTransactionID := "ref-ext-1"
	amount, err := money.FromMinorUnits(brl, 500)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().Add(-time.Hour)
	updatedAt := time.Now()

	tx := Transaction{
		id:                             id,
		status:                         TxStatusProcessed,
		externalTransactionID:          "ext-1",
		providerID:                     "provider-a",
		playerID:                       playerID,
		walletID:                       walletID,
		roundID:                        "round-1",
		gameID:                         "game-1",
		kind:                           KindBet,
		amount:                         amount,
		referenceExternalTransactionID: &referenceExternalTransactionID,
		referenceTransactionID:         &referenceTransactionID,
		failureCode:                    FailureCode("SOME_CODE"),
		createdAt:                      createdAt,
		updatedAt:                      updatedAt,
	}

	assert.Equal(t, id, tx.ID())
	assert.Equal(t, TxStatusProcessed, tx.Status())
	assert.Equal(t, "ext-1", tx.ExternalTransactionID())
	assert.Equal(t, "provider-a", tx.ProviderID())
	assert.Equal(t, playerID, tx.PlayerID())
	assert.Equal(t, walletID, tx.WalletID())
	assert.Equal(t, "round-1", tx.RoundID())
	assert.Equal(t, "game-1", tx.GameID())
	assert.Equal(t, KindBet, tx.Kind())
	assert.Equal(t, amount, tx.Amount())
	assert.Equal(t, &referenceExternalTransactionID, tx.ReferenceExternalTransactionID())
	assert.Equal(t, &referenceTransactionID, tx.ReferenceTransactionID())
	assert.Equal(t, FailureCode("SOME_CODE"), tx.FailureCode())
	assert.Equal(t, createdAt, tx.CreatedAt())
	assert.Equal(t, updatedAt, tx.UpdatedAt())
}

func TestTransactionIdempotencyKey(t *testing.T) {
	input := validInput(t, KindBet)
	input.ProviderID = "provider-a"
	input.ExternalTransactionID = "ext-1"
	tx, err := NewTransaction(input)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "provider-a:ext-1", tx.IdempotencyKey())
}

func TestTransactionResolveReference(t *testing.T) {
	t.Run("from terminal status", func(t *testing.T) {
		tx := &Transaction{status: TxStatusProcessed}
		err := tx.ResolveReference(uuid.New())
		assert.ErrorIs(t, err, ErrInvalidTransition)
	})

	t.Run("from pending, first resolution", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPending}
		id := uuid.New()
		err := tx.ResolveReference(id)
		assert.NoError(t, err)
		assert.Equal(t, &id, tx.referenceTransactionID)
	})

	t.Run("from pending reference, first resolution", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPendingReference}
		id := uuid.New()
		err := tx.ResolveReference(id)
		assert.NoError(t, err)
		assert.Equal(t, &id, tx.referenceTransactionID)
	})

	t.Run("idempotent, same id already resolved", func(t *testing.T) {
		id := uuid.New()
		tx := &Transaction{status: TxStatusPendingReference, referenceTransactionID: &id}
		err := tx.ResolveReference(id)
		assert.NoError(t, err)
		assert.Equal(t, &id, tx.referenceTransactionID)
	})

	t.Run("conflicting id already resolved", func(t *testing.T) {
		id := uuid.New()
		tx := &Transaction{status: TxStatusPendingReference, referenceTransactionID: &id}
		err := tx.ResolveReference(uuid.New())
		assert.ErrorIs(t, err, ErrReferenceMismatch)
		assert.Equal(t, &id, tx.referenceTransactionID)
	})
}

func validReferencePair(t *testing.T) (tx Transaction, ref Transaction) {
	t.Helper()

	amount, err := money.FromMinorUnits(brl, 1000)
	if err != nil {
		t.Fatal(err)
	}

	extID := "bet-ext-1"
	providerID := "provider-a"
	playerID := uuid.New()
	walletID := uuid.New()
	roundID := "round-1"

	ref = Transaction{
		status:                TxStatusProcessed,
		kind:                  KindBet,
		providerID:            providerID,
		externalTransactionID: extID,
		playerID:              playerID,
		walletID:              walletID,
		roundID:               roundID,
		amount:                amount,
	}
	tx = Transaction{
		kind:                           KindRefund,
		providerID:                     providerID,
		playerID:                       playerID,
		walletID:                       walletID,
		roundID:                        roundID,
		amount:                         amount,
		referenceExternalTransactionID: &extID,
	}
	return tx, ref
}

func TestTransactionValidateReference(t *testing.T) {
	t.Run("missing reference", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		tx.referenceExternalTransactionID = nil
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrMissingReference)
	})

	t.Run("referenced transaction not processed", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		ref.status = TxStatusPending
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("provider mismatch", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		ref.providerID = "other-provider"
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("external transaction id mismatch", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		ref.externalTransactionID = "different-ext-id"
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("player mismatch", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		ref.playerID = uuid.New()
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("wallet mismatch", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		ref.walletID = uuid.New()
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("round mismatch", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		ref.roundID = "other-round"
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("currency mismatch", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		otherCurrency, err := money.FromMinorUnits("USD", 1000)
		if err != nil {
			t.Fatal(err)
		}
		ref.amount = otherCurrency
		err = tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("amount mismatch", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		otherAmount, err := money.FromMinorUnits(brl, 500)
		if err != nil {
			t.Fatal(err)
		}
		ref.amount = otherAmount
		err = tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("refund referencing win is rejected", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		ref.kind = KindWin
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("rollback referencing bet", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		tx.kind = KindRollback
		ref.kind = KindBet
		assert.NoError(t, tx.ValidateReference(ref))
	})

	t.Run("rollback referencing win", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		tx.kind = KindRollback
		ref.kind = KindWin
		assert.NoError(t, tx.ValidateReference(ref))
	})

	t.Run("rollback referencing refund", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		tx.kind = KindRollback
		ref.kind = KindRefund
		assert.NoError(t, tx.ValidateReference(ref))
	})

	t.Run("rollback referencing rollback is rejected", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		tx.kind = KindRollback
		ref.kind = KindRollback
		err := tx.ValidateReference(ref)
		assert.ErrorIs(t, err, ErrInvalidReference)
	})

	t.Run("valid", func(t *testing.T) {
		tx, ref := validReferencePair(t)
		err := tx.ValidateReference(ref)
		assert.NoError(t, err)
	})
}

func newHashableTransaction(t *testing.T) Transaction {
	t.Helper()

	amount, err := money.FromMinorUnits(brl, 1000)
	if err != nil {
		t.Fatal(err)
	}

	return Transaction{
		providerID:            "provider-a",
		externalTransactionID: "ext-1",
		playerID:              uuid.New(),
		walletID:              uuid.New(),
		roundID:               "round-1",
		gameID:                "game-1",
		kind:                  KindBet,
		amount:                amount,
	}
}

func TestTransactionPayloadHash(t *testing.T) {
	t.Run("deterministic for same content", func(t *testing.T) {
		tx := newHashableTransaction(t)
		assert.Equal(t, tx.PayloadHash(), tx.PayloadHash())
	})

	t.Run("changes when amount changes", func(t *testing.T) {
		tx := newHashableTransaction(t)
		before := tx.PayloadHash()
		other, err := money.FromMinorUnits(brl, 2000)
		if err != nil {
			t.Fatal(err)
		}
		tx.amount = other
		assert.NotEqual(t, before, tx.PayloadHash())
	})

	t.Run("changes when kind changes", func(t *testing.T) {
		tx := newHashableTransaction(t)
		before := tx.PayloadHash()
		tx.kind = KindWin
		assert.NotEqual(t, before, tx.PayloadHash())
	})

	t.Run("changes when reference changes", func(t *testing.T) {
		tx := newHashableTransaction(t)
		before := tx.PayloadHash()
		ref := "some-ref"
		tx.referenceExternalTransactionID = &ref
		assert.NotEqual(t, before, tx.PayloadHash())
	})

	t.Run("unaffected by provider id", func(t *testing.T) {
		tx := newHashableTransaction(t)
		before := tx.PayloadHash()
		tx.providerID = "different-provider"
		assert.Equal(t, before, tx.PayloadHash())
	})

	t.Run("unaffected by external transaction id", func(t *testing.T) {
		tx := newHashableTransaction(t)
		before := tx.PayloadHash()
		tx.externalTransactionID = "different-ext-id"
		assert.Equal(t, before, tx.PayloadHash())
	})
}

func TestTransactionMarkProcessed(t *testing.T) {
	t.Run("from pending", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPending}
		err := tx.MarkProcessed()
		assert.NoError(t, err)
		assert.Equal(t, TxStatusProcessed, tx.status)
	})

	t.Run("from pending reference", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPendingReference}
		err := tx.MarkProcessed()
		assert.NoError(t, err)
		assert.Equal(t, TxStatusProcessed, tx.status)
	})

	t.Run("from terminal", func(t *testing.T) {
		tx := &Transaction{status: TxStatusProcessed}
		err := tx.MarkProcessed()
		assert.ErrorIs(t, err, ErrTerminalTransaction)
		assert.Equal(t, TxStatusProcessed, tx.status)
	})
}

func TestTransactionMarkRejected(t *testing.T) {
	const code FailureCode = "INSUFFICIENT_BALANCE"

	t.Run("from pending", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPending}
		err := tx.MarkRejected(code)
		assert.NoError(t, err)
		assert.Equal(t, TxStatusRejected, tx.status)
		assert.Equal(t, code, tx.failureCode)
	})

	t.Run("from pending reference", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPendingReference}
		err := tx.MarkRejected(code)
		assert.NoError(t, err)
		assert.Equal(t, TxStatusRejected, tx.status)
		assert.Equal(t, code, tx.failureCode)
	})

	t.Run("from terminal", func(t *testing.T) {
		tx := &Transaction{status: TxStatusFailed}
		err := tx.MarkRejected(code)
		assert.ErrorIs(t, err, ErrTerminalTransaction)
		assert.Equal(t, TxStatusFailed, tx.status)
	})
}

func TestTransactionMarkPendingReference(t *testing.T) {
	t.Run("from pending", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPending}
		err := tx.MarkPendingReference()
		assert.NoError(t, err)
		assert.Equal(t, TxStatusPendingReference, tx.status)
	})

	t.Run("from pending reference", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPendingReference}
		err := tx.MarkPendingReference()
		assert.ErrorIs(t, err, ErrInvalidTransition)
		assert.Equal(t, TxStatusPendingReference, tx.status)
	})

	t.Run("from terminal", func(t *testing.T) {
		tx := &Transaction{status: TxStatusRejected}
		err := tx.MarkPendingReference()
		assert.ErrorIs(t, err, ErrTerminalTransaction)
		assert.Equal(t, TxStatusRejected, tx.status)
	})
}

func TestTransactionMarkFailed(t *testing.T) {
	const code FailureCode = "UNEXPECTED_ERROR"

	t.Run("from pending", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPending}
		err := tx.MarkFailed(code)
		assert.NoError(t, err)
		assert.Equal(t, TxStatusFailed, tx.status)
		assert.Equal(t, code, tx.failureCode)
	})

	t.Run("from pending reference", func(t *testing.T) {
		tx := &Transaction{status: TxStatusPendingReference}
		err := tx.MarkFailed(code)
		assert.NoError(t, err)
		assert.Equal(t, TxStatusFailed, tx.status)
		assert.Equal(t, code, tx.failureCode)
	})

	t.Run("from terminal", func(t *testing.T) {
		tx := &Transaction{status: TxStatusProcessed}
		err := tx.MarkFailed(code)
		assert.ErrorIs(t, err, ErrTerminalTransaction)
		assert.Equal(t, TxStatusProcessed, tx.status)
	})
}
