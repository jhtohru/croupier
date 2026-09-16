//go:build integration

// This file tests the one thing that's naturally cmd/croupier's own concern
// and nobody else's: that the HTTP and SQS ingestion paths, wired together
// exactly as main.go wires them (same *app.WagerSubmitter, same Postgres),
// actually agree on idempotency when the same operation arrives once
// through each. internal/httpapi and internal/sqs each already verify their
// own path in isolation against real infrastructure; this is the mandatory
// "mesma operação via HTTP e via SQS → duplicidade tratada corretamente"
// scenario from TODO.md's Fase 12, which is specifically about the two
// paths agreeing with each other.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/auth"
	"github.com/jhtohru/croupier/internal/httpapi"
	"github.com/jhtohru/croupier/internal/money"
	"github.com/jhtohru/croupier/internal/postgres"
	croupiersqs "github.com/jhtohru/croupier/internal/sqs"
	"github.com/jhtohru/croupier/internal/wager"
	"github.com/jhtohru/croupier/internal/wallet"
)

func testDatabaseURL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://croupier:croupier@localhost:5432/croupier?sslmode=disable"
}

func testSQSEndpoint() string {
	if v := os.Getenv("SQS_ENDPOINT"); v != "" {
		return v
	}
	return "http://localhost:4566"
}

func testKeycloakIssuerURL() string {
	if v := os.Getenv("KEYCLOAK_ISSUER_URL"); v != "" {
		return v
	}
	return "http://localhost:8080/realms/croupier"
}

func fetchProviderAToken(t *testing.T) string {
	t.Helper()
	resp, err := http.PostForm(testKeycloakIssuerURL()+"/protocol/openid-connect/token", map[string][]string{
		"grant_type":    {"client_credentials"},
		"client_id":     {"provider-a"},
		"client_secret": {"provider-a-secret"},
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.AccessToken)
	return body.AccessToken
}

// TestSameOperationOverHTTPAndSQSIsNotDuplicated submits one BET over real
// HTTP, then submits the exact same (providerId, externalTransactionId,
// content) again over real SQS — both against the same *app.WagerSubmitter
// and Postgres cmd/croupier's own main.go would wire them to — and confirms
// the wallet is debited exactly once.
func TestSameOperationOverHTTPAndSQSIsNotDuplicated(t *testing.T) {
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, testDatabaseURL())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Ping(ctx))

	wallets := postgres.NewWalletRepository(pool)
	wagers := postgres.NewWagerRepository(pool)
	outboxRepo := postgres.NewOutboxRepository(pool)
	inboxRepo := postgres.NewInboxRepository(pool)
	txManager := postgres.NewTxManager(pool)
	submitter := app.NewWagerSubmitter(wallets, wagers, outboxRepo, txManager)

	verifierCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	verifier, err := auth.NewVerifier(verifierCtx, testKeycloakIssuerURL())
	require.NoError(t, err)

	srv := httpapi.NewServer(httpapi.Deps{
		WagerSubmitter: submitter,
		Auth:           verifier,
	})

	sqsClient, err := croupiersqs.NewClient(ctx, "us-east-1", testSQSEndpoint())
	require.NoError(t, err)
	queueName := "wager-transactions-http-sqs-test-" + strings.ReplaceAll(uuid.New().String(), "-", "") + ".fifo"
	createOut, err := sqsClient.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName:  aws.String(queueName),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "true"},
	})
	require.NoError(t, err)
	queueURL := aws.ToString(createOut.QueueUrl)
	t.Cleanup(func() { _, _ = sqsClient.DeleteQueue(ctx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)}) })

	consumer := croupiersqs.NewConsumer(sqsClient, queueURL, "http-sqs-duplicate-test-consumer", submitter, inboxRepo)

	balance, err := money.FromMinorUnits("BRL", 10000)
	require.NoError(t, err)
	w, err := wallet.New(uuid.New(), balance)
	require.NoError(t, err)
	require.NoError(t, wallets.Save(ctx, w))

	providerID := "provider-a"
	externalTransactionID := "ext-" + uuid.New().String()
	token := fetchProviderAToken(t)
	amount, err := money.FromMinorUnits("BRL", 3000)
	require.NoError(t, err)

	// First delivery: real HTTP request against the real Server. No
	// providerId field — same as the production request shape
	// (submitWagerTransactionRequest, internal/httpapi/wagering.go), since
	// it's derived from the caller's own token, not the body.
	reqBody, err := json.Marshal(httpSubmitBody{
		ExternalTransactionID: externalTransactionID,
		PlayerID:              w.PlayerID(), WalletID: w.ID(),
		RoundID: "round-1", GameID: "game-1",
		Kind: wager.KindBet, Money: amount,
	})
	require.NoError(t, err)

	httpReq := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(string(reqBody)))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Idempotency-Key", providerID+":"+externalTransactionID) // mandatory, spec §9
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httpReq)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	afterHTTP, err := wallets.FindByID(ctx, w.ID())
	require.NoError(t, err)
	assert.Equal(t, mustMoneyFromMinorUnits(t, 7000), afterHTTP.Balance())

	// Second delivery: the exact same operation, over real SQS this time —
	// wrapped in the challenge spec §10 envelope, same as a real provider
	// would send.
	sqsBody, err := json.Marshal(sqsSubmitEnvelope{
		MessageID:  uuid.NewString(),
		Type:       "WagerTransactionRequested",
		OccurredAt: time.Now().UTC(),
		Data: sqsSubmitBody{
			ProviderID: providerID, ExternalTransactionID: externalTransactionID,
			IdempotencyKey: providerID + ":" + externalTransactionID,
			PlayerID:       w.PlayerID(), WalletID: w.ID(), RoundID: "round-1", GameID: "game-1",
			Kind: wager.KindBet, Money: amount,
		},
	})
	require.NoError(t, err)
	_, err = sqsClient.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(sqsBody)), MessageGroupId: aws.String(w.ID().String()),
	})
	require.NoError(t, err)

	// Run the real Consumer loop (same public API cmd/croupier's own
	// lifecycle.go uses) just long enough to pick up, process, and delete
	// this one message.
	runCtx, runCancel := context.WithTimeout(ctx, 20*time.Second)
	runErr := make(chan error, 1)
	go func() { runErr <- consumer.Run(runCtx) }()
	t.Cleanup(func() {
		runCancel()
		<-runErr
	})

	require.Eventually(t, func() bool {
		attrOut, err := sqsClient.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
			QueueUrl:       aws.String(queueURL),
			AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameApproximateNumberOfMessages, sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible},
		})
		require.NoError(t, err)
		return attrOut.Attributes["ApproximateNumberOfMessages"] == "0" && attrOut.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	}, 15*time.Second, 200*time.Millisecond, "the SQS delivery was never fully processed and removed from the queue")

	final, err := wallets.FindByID(ctx, w.ID())
	require.NoError(t, err)
	assert.Equal(t, mustMoneyFromMinorUnits(t, 7000), final.Balance(), "the SQS delivery of the same operation must not debit again")

	entries, err := wallets.AllLedgerEntries(ctx, w.ID())
	require.NoError(t, err)
	assert.Len(t, entries, 1, "exactly one debit across both transports")
}

func mustMoneyFromMinorUnits(t *testing.T, minorUnits int64) money.Money {
	t.Helper()
	m, err := money.FromMinorUnits("BRL", minorUnits)
	require.NoError(t, err)
	return m
}

// httpSubmitBody mirrors internal/httpapi's (unexported)
// submitWagerTransactionRequest — no providerId field, since the real
// handler derives it from the caller's own bearer token.
type httpSubmitBody struct {
	ExternalTransactionID string      `json:"externalTransactionId"`
	PlayerID              uuid.UUID   `json:"playerId"`
	WalletID              uuid.UUID   `json:"walletId"`
	RoundID               string      `json:"roundId"`
	GameID                string      `json:"gameId"`
	Kind                  wager.Kind  `json:"kind"`
	Money                 money.Money `json:"money"`
}

// sqsSubmitEnvelope/sqsSubmitBody mirror internal/sqs's (unexported)
// wagerTransactionEnvelope/wagerTransactionMessage — challenge spec §10's
// {messageId, type, occurredAt, data} shape. sqsSubmitBody does carry
// providerId and idempotencyKey, since an inbound SQS message has no bearer
// token to derive providerId from and no header to carry idempotencyKey in.
type sqsSubmitEnvelope struct {
	MessageID  string        `json:"messageId"`
	Type       string        `json:"type"`
	OccurredAt time.Time     `json:"occurredAt"`
	Data       sqsSubmitBody `json:"data"`
}

type sqsSubmitBody struct {
	ProviderID            string      `json:"providerId"`
	ExternalTransactionID string      `json:"externalTransactionId"`
	IdempotencyKey        string      `json:"idempotencyKey"`
	PlayerID              uuid.UUID   `json:"playerId"`
	WalletID              uuid.UUID   `json:"walletId"`
	RoundID               string      `json:"roundId"`
	GameID                string      `json:"gameId"`
	Kind                  wager.Kind  `json:"kind"`
	Money                 money.Money `json:"money"`
}
