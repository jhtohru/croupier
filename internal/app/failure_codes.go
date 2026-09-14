package app

import "github.com/jhtohru/croupier/internal/wager"

// Failure codes assigned by SubmitWagerTransaction when rejecting a
// transaction. Kept distinct per the challenge's requirement that a reversal
// exceeding the available balance be distinguishable from a BET with
// insufficient balance.
const (
	FailureCodeInsufficientBalance    wager.FailureCode = "INSUFFICIENT_BALANCE"
	FailureCodeReversalExceedsBalance wager.FailureCode = "REVERSAL_EXCEEDS_BALANCE"
	FailureCodeInvalidReference       wager.FailureCode = "INVALID_REFERENCE"
	FailureCodeDuplicateReversal      wager.FailureCode = "DUPLICATE_REVERSAL"
	// FailureCodeReferenceNotFound is assigned by the PENDING_REFERENCE retry
	// worker (not yet implemented) once a reference never resolves within its
	// TTL/max attempts.
	FailureCodeReferenceNotFound wager.FailureCode = "REFERENCE_NOT_FOUND"
)
