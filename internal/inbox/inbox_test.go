package inbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	t.Run("empty consumer name", func(t *testing.T) {
		i, err := New(NewInput{ConsumerName: "", MessageID: "msg-1"})
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, i)
	})

	t.Run("empty message id", func(t *testing.T) {
		i, err := New(NewInput{ConsumerName: "consumer-a", MessageID: ""})
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, i)
	})

	t.Run("success", func(t *testing.T) {
		hash := [32]byte{1, 2, 3}
		i, err := New(NewInput{ConsumerName: "consumer-a", MessageID: "msg-1", PayloadHash: hash})
		assert.NoError(t, err)
		if assert.NotNil(t, i) {
			assert.Equal(t, "consumer-a", i.ConsumerName())
			assert.Equal(t, "msg-1", i.MessageID())
			assert.Equal(t, hash, i.PayloadHash())
			assert.False(t, i.IsCompleted())
			assert.Nil(t, i.CompletedAt())
		}
	})
}

func TestFromPersistence(t *testing.T) {
	hash := [32]byte{9, 9, 9}
	completedAt := time.Now()
	createdAt := time.Now().Add(-time.Minute)

	i := FromPersistence("consumer-a", "msg-1", hash, &completedAt, createdAt)

	assert.Equal(t, "consumer-a", i.ConsumerName())
	assert.Equal(t, "msg-1", i.MessageID())
	assert.Equal(t, hash, i.PayloadHash())
	assert.Equal(t, &completedAt, i.CompletedAt())
	assert.Equal(t, createdAt, i.CreatedAt())
	assert.True(t, i.IsCompleted())
}

func TestInboxMarkCompleted(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		i := &Inbox{}
		err := i.MarkCompleted()
		assert.NoError(t, err)
		assert.True(t, i.IsCompleted())
		assert.NotNil(t, i.CompletedAt())
	})

	t.Run("already completed", func(t *testing.T) {
		now := time.Now()
		i := &Inbox{completedAt: &now}
		err := i.MarkCompleted()
		assert.ErrorIs(t, err, ErrAlreadyCompleted)
	})
}
