package sqs

import (
	"context"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/jhtohru/croupier/internal/app"
	"github.com/jhtohru/croupier/internal/inbox"
)

type fakeWagerSubmitter struct {
	result *app.SubmitWagerTransactionResult
	err    error
	calls  []app.SubmitWagerTransactionInput
}

func (s *fakeWagerSubmitter) Submit(ctx context.Context, input app.SubmitWagerTransactionInput) (*app.SubmitWagerTransactionResult, error) {
	s.calls = append(s.calls, input)
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &app.SubmitWagerTransactionResult{}, nil
}

type fakeInboxRepository struct {
	entries map[string]*inbox.Inbox // key: consumerName + "|" + messageID
}

func newFakeInboxRepository() *fakeInboxRepository {
	return &fakeInboxRepository{entries: make(map[string]*inbox.Inbox)}
}

func inboxKey(consumerName, messageID string) string {
	return consumerName + "|" + messageID
}

func (r *fakeInboxRepository) FindByConsumerAndMessage(ctx context.Context, consumerName, messageID string) (*inbox.Inbox, error) {
	e, ok := r.entries[inboxKey(consumerName, messageID)]
	if !ok {
		return nil, app.ErrInboxEntryNotFound
	}
	return e, nil
}

func (r *fakeInboxRepository) Save(ctx context.Context, i *inbox.Inbox) error {
	r.entries[inboxKey(i.ConsumerName(), i.MessageID())] = i
	return nil
}

type fakeReceiveDeleter struct {
	receiveOut    *awssqs.ReceiveMessageOutput
	receiveErr    error
	receiveCalls  int
	deletedHandle []string
}

func (f *fakeReceiveDeleter) ReceiveMessage(ctx context.Context, params *awssqs.ReceiveMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	f.receiveCalls++
	if f.receiveErr != nil {
		return nil, f.receiveErr
	}
	return f.receiveOut, nil
}

func (f *fakeReceiveDeleter) DeleteMessage(ctx context.Context, params *awssqs.DeleteMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.deletedHandle = append(f.deletedHandle, *params.ReceiptHandle)
	return &awssqs.DeleteMessageOutput{}, nil
}

type fakeSender struct {
	sent []*awssqs.SendMessageInput
	err  error
}

func (f *fakeSender) SendMessage(ctx context.Context, params *awssqs.SendMessageInput, optFns ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	f.sent = append(f.sent, params)
	if f.err != nil {
		return nil, f.err
	}
	return &awssqs.SendMessageOutput{}, nil
}
