// Package sqs implements the messaging adapters: Publisher (satisfies
// app.OutboxPublisher, ships outbox entries as SQS messages) and Consumer
// (polls an inbound queue of provider-submitted wager transactions, dedups
// via app.InboxRepository, and feeds the same app.WagerSubmitter.Submit the
// HTTP layer uses — both ingestion paths carry identical guarantees).
package sqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// NewClient builds an SQS client. endpoint overrides the SDK's default
// endpoint resolution — set it to LocalStack's URL locally, leave it empty
// for the real AWS endpoint in production. LocalStack never validates
// credentials, but the SDK still needs something to sign requests with,
// hence the static placeholder credentials whenever an endpoint override is
// in play — a real deployment (empty endpoint) uses the SDK's normal
// credential chain instead (env vars, IAM role, etc.).
func NewClient(ctx context.Context, region, endpoint string) (*sqs.Client, error) {
	optFns := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if endpoint != "" {
		optFns = append(optFns, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		))
	}
	cfg, err := config.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	}), nil
}
