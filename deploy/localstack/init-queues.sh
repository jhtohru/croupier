#!/bin/sh
# Runs automatically inside the localstack container (mounted at
# /etc/localstack/init/ready.d/) once SQS is up — LocalStack's init-hooks
# mechanism blocks the container's readiness state until this finishes, so
# the queues always exist by the time the healthcheck (and anything waiting
# on it, like `docker compose up --wait`) reports healthy.
set -eu

DLQ_NAME="wager-transactions-dlq.fifo"
QUEUE_NAME="wager-transactions.fifo"
EVENTS_QUEUE_NAME="wallet-events.fifo"

awslocal sqs create-queue \
  --queue-name "$DLQ_NAME" \
  --attributes FifoQueue=true,ContentBasedDeduplication=true

DLQ_URL=$(awslocal sqs get-queue-url --queue-name "$DLQ_NAME" --query QueueUrl --output text)
DLQ_ARN=$(awslocal sqs get-queue-attributes --queue-url "$DLQ_URL" --attribute-names QueueArn --query Attributes.QueueArn --output text)

# The --attributes shorthand (Key=Value,Key2=Value2) can't parse a value
# that itself contains "=" and nested braces (RedrivePolicy's embedded
# JSON), so this needs the full --attributes JSON form instead.
# RedrivePolicy's value is itself a JSON-encoded string (per the SQS API),
# so its inner double quotes have to be escaped once more before going into
# the outer --attributes JSON document.
REDRIVE_POLICY=$(printf '{"deadLetterTargetArn":"%s","maxReceiveCount":"5"}' "$DLQ_ARN")
REDRIVE_POLICY_ESCAPED=$(printf '%s' "$REDRIVE_POLICY" | sed 's/"/\\"/g')
ATTRS=$(printf '{"FifoQueue":"true","ContentBasedDeduplication":"true","RedrivePolicy":"%s"}' "$REDRIVE_POLICY_ESCAPED")

# maxReceiveCount=5: a message that fails processing 5 times moves to the
# DLQ instead of retrying forever — see Fase 8/Fase 12 in TODO.md.
awslocal sqs create-queue \
  --queue-name "$QUEUE_NAME" \
  --attributes "$ATTRS"

# Outbound domain events (WagerTransactionProcessed, WalletBalanceChanged,
# ...) published by app.OutboxWorker via internal/sqs.Publisher — see
# ARCHITECTURE.md's "Mensageria (SQS)" section for why this is a queue
# separate from the inbound wager-transactions.fifo above.
awslocal sqs create-queue \
  --queue-name "$EVENTS_QUEUE_NAME" \
  --attributes FifoQueue=true,ContentBasedDeduplication=true

echo "localstack init: queues ready ($QUEUE_NAME, $DLQ_NAME, $EVENTS_QUEUE_NAME)"
